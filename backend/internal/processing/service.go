package processing

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/outbox"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the service's SQL migrations for sqlitex.Open.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	return sub
}

// ErrNotFound is returned when no processed data exists for a location.
var ErrNotFound = errors.New("not found")

// Service consumes weather.updated events, stores the processed result and
// queues weather.processed for the prediction service.
type Service struct {
	DB  *sql.DB
	Log *slog.Logger

	Clock              func() time.Time // defaults to time.Now
	Retention          time.Duration    // processed snapshots kept this long; default 24h
	RejectionRetention time.Duration    // rejection records kept this long; default 7d

	processed atomic.Int64
	rejected  atomic.Int64
	stale     atomic.Int64
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Counts returns how many events were processed, rejected and dropped as stale
// since start.
func (s *Service) Counts() (processed, rejected, stale int64) {
	return s.processed.Load(), s.rejected.Load(), s.stale.Load()
}

// HandleEvent is the inbox handler. It runs inside the inbox transaction, so the
// stored snapshot, any rejection record and the outgoing event commit together
// with the dedupe record — or not at all. It returns an error only for transient
// failures worth retrying; bad data is recorded and consumed (nil).
func (s *Service) HandleEvent(ctx context.Context, tx *sql.Tx, ev events.Event) error {
	if ev.Type != contracts.EventWeatherUpdated {
		s.log().Warn("ignoring unknown event type", "type", ev.Type, "event_id", ev.ID)
		return nil
	}

	var in contracts.WeatherUpdated
	if err := ev.Decode(&in); err != nil {
		return s.reject(ctx, tx, ev, nil, &RejectError{Reason: ReasonUndecodable, Detail: err.Error()})
	}
	if in.Location.ID <= 0 {
		return s.reject(ctx, tx, ev, &in, reject(ReasonInvalid, "event has no location id"))
	}

	result, err := Process(in.Snapshot)
	if err != nil {
		var rej *RejectError
		if errors.As(err, &rej) {
			return s.reject(ctx, tx, ev, &in, rej)
		}
		return err
	}

	// Drop out-of-order deliveries: never let older data replace newer.
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(fetched_at) FROM snapshots WHERE location_id = ?`, in.Location.ID).Scan(&latest); err != nil {
		return fmt.Errorf("check latest snapshot: %w", err)
	}
	if latest.Valid && in.FetchedAt.UnixMilli() <= latest.Int64 {
		s.stale.Add(1)
		s.log().Info("dropping stale observation", "location_id", in.Location.ID, "observation_id", in.ObservationID)
		return nil
	}

	result.ObservationID = in.ObservationID
	result.Location = in.Location
	result.Source = in.Source
	result.FetchedAt = in.FetchedAt.UTC()
	result.ProcessedAt = s.now().UTC()

	full, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode processed weather: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshots (location_id, observation_id, source, fetched_at, processed_at, quality, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.Location.ID, in.ObservationID, in.Source, in.FetchedAt.UnixMilli(), result.ProcessedAt.UnixMilli(),
		result.Quality.Score, string(full)); err != nil {
		return fmt.Errorf("store snapshot: %w", err)
	}

	forward := result
	forward.Hourly = nil // downstream works from the daily series
	if _, err := outbox.Enqueue(ctx, tx, contracts.TargetPrediction, contracts.EventWeatherProcessed, forward); err != nil {
		return err
	}

	s.processed.Add(1)
	s.log().Info("processed observation",
		"location_id", in.Location.ID, "location", in.Location.Name, "observation_id", in.ObservationID,
		"detail", Summary(result.Quality))
	return nil
}

// reject records why a snapshot was dropped and consumes the event.
func (s *Service) reject(ctx context.Context, tx *sql.Tx, ev events.Event, in *contracts.WeatherUpdated, rej *RejectError) error {
	var locID, obsID any
	if in != nil {
		locID, obsID = in.Location.ID, in.ObservationID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO rejections (event_id, location_id, observation_id, reason, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ev.ID, locID, obsID, rej.Reason, rej.Detail, s.now().UnixMilli()); err != nil {
		return fmt.Errorf("record rejection: %w", err)
	}
	s.rejected.Add(1)
	s.log().Warn("rejected observation", "event_id", ev.ID, "location_id", locID, "reason", rej.Reason, "detail", rej.Detail)
	return nil
}

// Latest returns the newest processed snapshot for a location.
func (s *Service) Latest(ctx context.Context, locationID int64) (contracts.ProcessedWeather, error) {
	var payload string
	err := s.DB.QueryRowContext(ctx,
		`SELECT payload FROM snapshots WHERE location_id = ? ORDER BY fetched_at DESC, id DESC LIMIT 1`, locationID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return contracts.ProcessedWeather{}, ErrNotFound
	}
	if err != nil {
		return contracts.ProcessedWeather{}, err
	}
	var out contracts.ProcessedWeather
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return contracts.ProcessedWeather{}, fmt.Errorf("decode stored snapshot: %w", err)
	}
	return out, nil
}

// Rejection is a dropped observation, kept for diagnosis.
type Rejection struct {
	EventID       string    `json:"eventId"`
	LocationID    *int64    `json:"locationId,omitempty"`
	ObservationID *int64    `json:"observationId,omitempty"`
	Reason        string    `json:"reason"`
	Detail        string    `json:"detail"`
	At            time.Time `json:"at"`
}

// RecentRejections returns up to limit rejections, newest first.
func (s *Service) RecentRejections(ctx context.Context, limit int) ([]Rejection, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT event_id, location_id, observation_id, reason, detail, created_at FROM rejections ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rejection{}
	for rows.Next() {
		var (
			r        Rejection
			loc, obs sql.NullInt64
			at       int64
		)
		if err := rows.Scan(&r.EventID, &loc, &obs, &r.Reason, &r.Detail, &at); err != nil {
			return nil, err
		}
		if loc.Valid {
			r.LocationID = &loc.Int64
		}
		if obs.Valid {
			r.ObservationID = &obs.Int64
		}
		r.At = time.UnixMilli(at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// Purge deletes expired snapshots and rejection records.
func (s *Service) Purge(ctx context.Context) error {
	keep, keepRej := s.Retention, s.RejectionRetention
	if keep <= 0 {
		keep = 24 * time.Hour
	}
	if keepRej <= 0 {
		keepRej = 7 * 24 * time.Hour
	}
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM snapshots WHERE fetched_at < ?`, now.Add(-keep).UnixMilli()); err != nil {
		return fmt.Errorf("purge snapshots: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM rejections WHERE created_at < ?`, now.Add(-keepRej).UnixMilli()); err != nil {
		return fmt.Errorf("purge rejections: %w", err)
	}
	return nil
}

// Run purges expired data hourly until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Purge(ctx); err != nil && ctx.Err() == nil {
				s.log().Error("purge failed", "err", err)
			}
		}
	}
}
