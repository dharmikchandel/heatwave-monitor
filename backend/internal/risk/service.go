package risk

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
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

// ErrNotFound is returned when no assessment exists for a location.
var ErrNotFound = errors.New("not found")

// Service consumes heatwave.predicted events, stores the assessment and queues
// risk.assessed for the alert service.
type Service struct {
	DB  *sql.DB
	Log *slog.Logger

	Clock              func() time.Time // defaults to time.Now
	Retention          time.Duration    // assessments kept this long; default 24h
	RejectionRetention time.Duration    // rejection records kept this long; default 7d

	assessed atomic.Int64
	rejected atomic.Int64
	stale    atomic.Int64

	mu      sync.Mutex
	byLevel map[engine.RiskLevel]int64 // assessments by alert level
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

// Counts returns how many events were assessed, rejected and dropped as stale since start.
func (s *Service) Counts() (assessed, rejected, stale int64) {
	return s.assessed.Load(), s.rejected.Load(), s.stale.Load()
}

// LevelCounts returns how many assessments had each alert level since start.
func (s *Service) LevelCounts() map[engine.RiskLevel]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[engine.RiskLevel]int64, len(s.byLevel))
	for k, v := range s.byLevel {
		out[k] = v
	}
	return out
}

// HandleEvent is the inbox handler; it runs inside the inbox transaction, so the
// stored assessment, any rejection record and the outgoing event commit together
// with the dedupe record. Only transient failures return an error (and so get
// retried); bad data is recorded and consumed.
func (s *Service) HandleEvent(ctx context.Context, tx *sql.Tx, ev events.Event) error {
	if ev.Type != contracts.EventHeatwavePredicted {
		s.log().Warn("ignoring unknown event type", "type", ev.Type, "event_id", ev.ID)
		return nil
	}

	var in contracts.HeatwavePredicted
	if err := ev.Decode(&in); err != nil {
		return s.reject(ctx, tx, ev, nil, &RejectError{Reason: ReasonUndecoded, Detail: err.Error()})
	}
	loc := in.Weather.Location
	if loc.ID <= 0 {
		return s.reject(ctx, tx, ev, &in, reject(ReasonInvalid, "event has no location id"))
	}

	assessment, err := Assess(in.Weather, in.Prediction, s.now())
	if err != nil {
		var rej *RejectError
		if errors.As(err, &rej) {
			return s.reject(ctx, tx, ev, &in, rej)
		}
		return err
	}

	// Never let an older observation replace a newer assessment.
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(fetched_at) FROM assessments WHERE location_id = ?`, loc.ID).Scan(&latest); err != nil {
		return fmt.Errorf("check latest assessment: %w", err)
	}
	if latest.Valid && in.Weather.FetchedAt.UnixMilli() <= latest.Int64 {
		s.stale.Add(1)
		s.log().Info("dropping stale observation", "location_id", loc.ID, "observation_id", in.Weather.ObservationID)
		return nil
	}

	out := contracts.RiskAssessed{Weather: in.Weather, Prediction: in.Prediction, Assessment: assessment}
	stored, err := json.Marshal(storedAssessment{
		Location:      loc,
		ObservationID: in.Weather.ObservationID,
		FetchedAt:     in.Weather.FetchedAt.UTC(),
		Assessment:    assessment,
	})
	if err != nil {
		return fmt.Errorf("encode assessment: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO assessments (location_id, observation_id, fetched_at, assessed_at, now_level, peak_level, alert_level, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		loc.ID, in.Weather.ObservationID, in.Weather.FetchedAt.UnixMilli(), assessment.AssessedAt.UnixMilli(),
		string(assessment.Now.Level), string(assessment.Peak.Level), string(assessment.AlertLevel), string(stored)); err != nil {
		return fmt.Errorf("store assessment: %w", err)
	}
	if _, err := outbox.Enqueue(ctx, tx, contracts.TargetAlert, contracts.EventRiskAssessed, out); err != nil {
		return err
	}

	s.assessed.Add(1)
	s.mu.Lock()
	if s.byLevel == nil {
		s.byLevel = map[engine.RiskLevel]int64{}
	}
	s.byLevel[assessment.AlertLevel]++
	s.mu.Unlock()
	s.log().Info("assessed risk",
		"location_id", loc.ID, "location", loc.Name, "observation_id", in.Weather.ObservationID,
		"now", assessment.Now.Level, "peak", assessment.Peak.Level, "peak_date", assessment.Peak.Date,
		"alert_level", assessment.AlertLevel, "heatwave_expected", assessment.HeatwaveExpected)
	return nil
}

func (s *Service) reject(ctx context.Context, tx *sql.Tx, ev events.Event, in *contracts.HeatwavePredicted, rej *RejectError) error {
	var locID, obsID any
	if in != nil {
		locID, obsID = in.Weather.Location.ID, in.Weather.ObservationID
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

// storedAssessment is what the API serves: the verdict plus where it came from.
type storedAssessment struct {
	Location      contracts.Location   `json:"location"`
	ObservationID int64                `json:"observationId"`
	FetchedAt     time.Time            `json:"fetchedAt"`
	Assessment    contracts.Assessment `json:"assessment"`
}

// Latest returns the newest assessment for a location.
func (s *Service) Latest(ctx context.Context, locationID int64) (storedAssessment, error) {
	var payload string
	err := s.DB.QueryRowContext(ctx,
		`SELECT payload FROM assessments WHERE location_id = ? ORDER BY fetched_at DESC, id DESC LIMIT 1`, locationID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return storedAssessment{}, ErrNotFound
	}
	if err != nil {
		return storedAssessment{}, err
	}
	var out storedAssessment
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return storedAssessment{}, fmt.Errorf("decode stored assessment: %w", err)
	}
	return out, nil
}

// Rejection is a dropped input, kept for diagnosis.
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

// Purge deletes expired assessments and rejection records.
func (s *Service) Purge(ctx context.Context) error {
	keep, keepRej := s.Retention, s.RejectionRetention
	if keep <= 0 {
		keep = 24 * time.Hour
	}
	if keepRej <= 0 {
		keepRej = 7 * 24 * time.Hour
	}
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM assessments WHERE fetched_at < ?`, now.Add(-keep).UnixMilli()); err != nil {
		return fmt.Errorf("purge assessments: %w", err)
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
