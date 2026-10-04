// Package weather implements the weather data service: a registry of watched
// locations, pluggable weather sources (Open-Meteo or simulated), a poller, and
// the HTTP API. Each fetch is stored and announced to the processing service
// through the transactional outbox.
package weather

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
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

// ErrNotFound is returned when a location or observation does not exist.
var ErrNotFound = errors.New("not found")

// Location is a registry record: the event-facing identity plus fetch status.
type Location struct {
	contracts.Location
	Active        bool       `json:"active"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastFetchedAt *time.Time `json:"lastFetchedAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

// Observation is a stored snapshot.
type Observation struct {
	ID         int64                 `json:"id"`
	LocationID int64                 `json:"locationId"`
	FetchedAt  time.Time             `json:"fetchedAt"`
	Source     string                `json:"source"`
	Snapshot   contracts.RawSnapshot `json:"snapshot"`
}

// GeoKey normalises coordinates to 2 decimals (about 1 km), so nearby requests
// for the same place share one registry entry — the same granularity the
// frontend uses for its cache key.
func GeoKey(lat, lon float64) string {
	round := func(v float64) float64 { return math.Round(v*100)/100 + 0 } // +0 turns -0 into 0
	return fmt.Sprintf("%.2f,%.2f", round(lat), round(lon))
}

const locationColumns = `id, name, country, admin1, latitude, longitude, timezone, active, created_at, last_fetched_at, last_error`

type scanner interface{ Scan(dest ...any) error }

func scanLocation(s scanner) (Location, error) {
	var (
		l       Location
		active  int
		created int64
		fetched sql.NullInt64
		lastErr sql.NullString
	)
	if err := s.Scan(&l.ID, &l.Name, &l.Country, &l.Admin1, &l.Latitude, &l.Longitude, &l.Timezone, &active, &created, &fetched, &lastErr); err != nil {
		return Location{}, err
	}
	l.Active = active == 1
	l.CreatedAt = time.UnixMilli(created).UTC()
	if fetched.Valid {
		t := time.UnixMilli(fetched.Int64).UTC()
		l.LastFetchedAt = &t
	}
	l.LastError = lastErr.String
	return l, nil
}

// NewLocation is the input for registering a location.
type NewLocation struct {
	Name      string
	Country   string
	Admin1    string
	Latitude  float64
	Longitude float64
	Timezone  string
}

// ValidationError reports bad caller input.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

func (n *NewLocation) validate() error {
	n.Name = strings.TrimSpace(n.Name)
	switch {
	case n.Name == "":
		return ValidationError("name is required")
	case len([]rune(n.Name)) > 100:
		return ValidationError("name must be at most 100 characters")
	case math.IsNaN(n.Latitude) || n.Latitude < -90 || n.Latitude > 90:
		return ValidationError("latitude must be between -90 and 90")
	case math.IsNaN(n.Longitude) || n.Longitude < -180 || n.Longitude > 180:
		return ValidationError("longitude must be between -180 and 180")
	}
	return nil
}

// ErrTooManyLocations is returned when the registry is at its configured limit.
var ErrTooManyLocations = errors.New("location limit reached")

// AddLocation registers a location, or returns the existing one for the same
// ~1 km cell (reactivating it if it had been removed). created reports whether
// a new record was inserted.
func (s *Service) AddLocation(ctx context.Context, in NewLocation) (loc Location, created bool, err error) {
	if err := in.validate(); err != nil {
		return Location{}, false, err
	}
	key := GeoKey(in.Latitude, in.Longitude)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Location{}, false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM locations WHERE geo_key = ?`, key).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE locations SET active = 1 WHERE id = ?`, id); err != nil {
			return Location{}, false, err
		}
	case errors.Is(err, sql.ErrNoRows):
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM locations WHERE active = 1`).Scan(&active); err != nil {
			return Location{}, false, err
		}
		if s.MaxLocations > 0 && active >= s.MaxLocations {
			return Location{}, false, ErrTooManyLocations
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO locations (geo_key, name, country, admin1, latitude, longitude, timezone, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			key, in.Name, strings.TrimSpace(in.Country), strings.TrimSpace(in.Admin1),
			in.Latitude, in.Longitude, strings.TrimSpace(in.Timezone), s.now().UnixMilli())
		if err != nil {
			return Location{}, false, err
		}
		id, _ = res.LastInsertId()
		created = true
	default:
		return Location{}, false, err
	}

	loc, err = scanLocation(tx.QueryRowContext(ctx, `SELECT `+locationColumns+` FROM locations WHERE id = ?`, id))
	if err != nil {
		return Location{}, false, err
	}
	return loc, created, tx.Commit()
}

// GetLocation returns one location (active or not).
func (s *Service) GetLocation(ctx context.Context, id int64) (Location, error) {
	loc, err := scanLocation(s.DB.QueryRowContext(ctx, `SELECT `+locationColumns+` FROM locations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Location{}, ErrNotFound
	}
	return loc, err
}

// ListLocations returns active locations ordered by name.
func (s *Service) ListLocations(ctx context.Context) ([]Location, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+locationColumns+` FROM locations WHERE active = 1 ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RemoveLocation stops watching a location. History is kept; adding the same
// place again reactivates it.
func (s *Service) RemoveLocation(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE locations SET active = 0 WHERE id = ? AND active = 1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LatestObservation returns the newest stored snapshot for a location.
func (s *Service) LatestObservation(ctx context.Context, locationID int64) (Observation, error) {
	var (
		o       Observation
		fetched int64
		payload string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, location_id, fetched_at, source, payload FROM observations
		 WHERE location_id = ? ORDER BY fetched_at DESC, id DESC LIMIT 1`, locationID,
	).Scan(&o.ID, &o.LocationID, &fetched, &o.Source, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{}, ErrNotFound
	}
	if err != nil {
		return Observation{}, err
	}
	o.FetchedAt = time.UnixMilli(fetched).UTC()
	if err := json.Unmarshal([]byte(payload), &o.Snapshot); err != nil {
		return Observation{}, fmt.Errorf("decode stored snapshot %d: %w", o.ID, err)
	}
	return o, nil
}

// PurgeObservations deletes snapshots older than the retention window.
func (s *Service) PurgeObservations(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM observations WHERE fetched_at < ?`, s.now().Add(-s.Retention).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Service) getSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Service) setSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
