// Package alert implements the alert and notification service. It turns the risk
// service's assessments into alert episodes (open, escalate, resolve), decides who
// must be told, and delivers notifications by webhook or log.
//
// Design goals: never spam (repeating a level, or dropping and returning to a
// level already announced, tells nobody anything new), never miss an escalation,
// and never lose a notification (deliveries are stored and retried).
package alert

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
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

// Errors the API maps to HTTP statuses.
var (
	ErrNotFound    = errors.New("not found")
	ErrTooManySubs = errors.New("subscription limit reached")
	ErrValidation  = errors.New("validation failed")
)

// validationError wraps a message so callers can use errors.Is(err, ErrValidation).
func validationError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// Service owns alert state. All settings have sensible defaults.
type Service struct {
	DB  *sql.DB
	Log *slog.Logger

	Clock func() time.Time // defaults to time.Now

	// OpenLevel is the lowest risk level that opens an alert (default Danger: a heatwave warning).
	OpenLevel engine.RiskLevel
	// ResolveAfter is how long the risk must stay below OpenLevel before the alert is
	// resolved; it stops a flickering forecast from opening and closing alerts. 0 resolves at once.
	ResolveAfter time.Duration
	// Cooldown: if risk returns to OpenLevel within this long after an alert was resolved,
	// that same alert is reopened instead of a new one being announced. 0 disables reopening.
	Cooldown time.Duration
	// AllowedHosts, when non-empty, restricts webhook targets to these hostnames.
	AllowedHosts     []string
	MaxSubscriptions int           // operator subscriptions (webhook/log); 0 = unlimited
	MaxUserSubs      int           // in-app subscriptions per user; default 20
	Retention        time.Duration // resolved alerts kept this long; default 30d

	handled  atomic.Int64
	rejected atomic.Int64
	stale    atomic.Int64
	opened   atomic.Int64
	resolved atomic.Int64
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

func (s *Service) maxUserSubs() int {
	if s.MaxUserSubs > 0 {
		return s.MaxUserSubs
	}
	return 20
}

func (s *Service) openLevel() engine.RiskLevel {
	if s.OpenLevel != "" {
		return s.OpenLevel
	}
	return engine.Danger
}

// Counts returns lifetime counters since start.
func (s *Service) Counts() (handled, rejected, stale, opened, resolved int64) {
	return s.handled.Load(), s.rejected.Load(), s.stale.Load(), s.opened.Load(), s.resolved.Load()
}

// Purge deletes old resolved alerts (with their history and notifications) and old rejections.
func (s *Service) Purge(ctx context.Context) error {
	keep := s.Retention
	if keep <= 0 {
		keep = 30 * 24 * time.Hour
	}
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM alerts WHERE status = 'resolved' AND resolved_at < ?`, now.Add(-keep).UnixMilli()); err != nil {
		return fmt.Errorf("purge alerts: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM rejections WHERE created_at < ?`, now.Add(-7*24*time.Hour).UnixMilli()); err != nil {
		return fmt.Errorf("purge rejections: %w", err)
	}
	return nil
}

// Run purges old data hourly until ctx is cancelled.
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

// OpenAlertCount returns how many alerts are currently open (for /metrics).
func (s *Service) OpenAlertCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE status = 'open'`).Scan(&n)
	return n, err
}

// NotificationCounts returns notifications by delivery status (for /metrics).
func (s *Service) NotificationCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM notifications GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
