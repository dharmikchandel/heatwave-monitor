// Package outbox implements the transactional outbox: a service records an
// event in the same SQL transaction as the data change that caused it, and a
// background Dispatcher POSTs pending events to the target service, retrying
// with exponential backoff until it answers 2xx.
//
// Guarantees: at-least-once delivery, and in-order delivery per target (a
// failing event blocks later events for that same target until it succeeds).
// Consumers dedupe on Event.ID via package inbox.
package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
)

// Enqueue records an event for target inside tx. It only becomes visible to the
// dispatcher when tx commits; if tx rolls back, the event never exists.
func Enqueue(ctx context.Context, tx *sql.Tx, target, eventType string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("outbox: marshal %s payload: %w", eventType, err)
	}
	id := events.NewID()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outbox (id, target, type, payload, created_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, target, eventType, string(body), now, now,
	); err != nil {
		return "", fmt.Errorf("outbox: insert %s: %w", eventType, err)
	}
	return id, nil
}

// Dispatcher delivers pending outbox rows. Targets maps a target name (as passed
// to Enqueue) to the full URL events are POSTed to.
type Dispatcher struct {
	DB      *sql.DB
	Source  string            // this service's name, sent as Event.Source
	Targets map[string]string // target name -> URL
	Client  *http.Client      // optional; defaults to a 5s-timeout client
	Log     *slog.Logger      // optional

	Interval   time.Duration // how often to poll for due rows; default 2s
	MaxBackoff time.Duration // retry delay cap; default 1m
	BatchSize  int           // rows fetched per pass; default 50
	Retention  time.Duration // delivered rows older than this are purged; default 24h

	wake chan struct{}
}

func (d *Dispatcher) init() {
	if d.Client == nil {
		d.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Interval <= 0 {
		d.Interval = 2 * time.Second
	}
	if d.MaxBackoff <= 0 {
		d.MaxBackoff = time.Minute
	}
	if d.BatchSize <= 0 {
		d.BatchSize = 50
	}
	if d.Retention <= 0 {
		d.Retention = 24 * time.Hour
	}
	if d.wake == nil {
		d.wake = make(chan struct{}, 1)
	}
}

// Notify asks the dispatcher to run a pass immediately (call it after the
// transaction that enqueued an event commits). It never blocks.
func (d *Dispatcher) Notify() {
	d.init()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run dispatches until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.init()
	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()

	for {
		if _, err := d.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("outbox dispatch failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.wake:
		case <-purge.C:
			if err := d.Purge(ctx); err != nil && ctx.Err() == nil {
				d.Log.Error("outbox purge failed", "err", err)
			}
		}
	}
}

type row struct {
	id, target, typ, payload string
	createdAt                int64
	attempts                 int
}

// DispatchOnce runs a single delivery pass and returns how many events were
// delivered.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	d.init()
	rows, err := d.due(ctx)
	if err != nil {
		return 0, err
	}

	delivered := 0
	blocked := map[string]bool{} // targets that failed this pass: keep order, retry later
	for _, r := range rows {
		if blocked[r.target] {
			continue
		}
		if err := d.deliver(ctx, r); err != nil {
			blocked[r.target] = true
			if markErr := d.markFailed(ctx, r, err); markErr != nil {
				return delivered, markErr
			}
			d.Log.Warn("outbox delivery failed", "event_id", r.id, "type", r.typ, "target", r.target, "attempts", r.attempts+1, "err", err)
			continue
		}
		if _, err := d.DB.ExecContext(ctx,
			`UPDATE outbox SET delivered_at = ?, last_error = NULL WHERE id = ?`, time.Now().UnixMilli(), r.id,
		); err != nil {
			return delivered, fmt.Errorf("outbox: mark delivered: %w", err)
		}
		delivered++
	}
	return delivered, nil
}

// due loads the oldest pending rows whose retry time has arrived. Rows are read
// fully before any delivery so the single DB connection is free again.
func (d *Dispatcher) due(ctx context.Context) ([]row, error) {
	// Fetch all pending rows' heads per target ordering, but only act on those due.
	// A not-yet-due row blocks later rows for its target, to preserve ordering.
	res, err := d.DB.QueryContext(ctx,
		`SELECT id, target, type, payload, created_at, attempts, next_attempt_at
		   FROM outbox WHERE delivered_at IS NULL ORDER BY created_at, id LIMIT ?`, d.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox: query pending: %w", err)
	}
	defer res.Close()

	now := time.Now().UnixMilli()
	var out []row
	notDue := map[string]bool{}
	for res.Next() {
		var r row
		var next int64
		if err := res.Scan(&r.id, &r.target, &r.typ, &r.payload, &r.createdAt, &r.attempts, &next); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		if notDue[r.target] {
			continue
		}
		if next > now {
			notDue[r.target] = true
			continue
		}
		out = append(out, r)
	}
	return out, res.Err()
}

func (d *Dispatcher) deliver(ctx context.Context, r row) error {
	url, ok := d.Targets[r.target]
	if !ok {
		return fmt.Errorf("unknown target %q", r.target)
	}
	body, err := json.Marshal(events.Event{
		ID:        r.id,
		Type:      r.typ,
		Source:    d.Source,
		CreatedAt: time.UnixMilli(r.createdAt).UTC(),
		Payload:   json.RawMessage(r.payload),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-ID", r.id)

	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("target answered %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (d *Dispatcher) markFailed(ctx context.Context, r row, cause error) error {
	attempts := r.attempts + 1
	next := time.Now().Add(backoff(attempts, d.MaxBackoff)).UnixMilli()
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE outbox SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
		attempts, next, cause.Error(), r.id,
	); err != nil {
		return fmt.Errorf("outbox: mark failed: %w", err)
	}
	return nil
}

// backoff returns 1s, 2s, 4s, ... capped at max.
func backoff(attempts int, max time.Duration) time.Duration {
	if attempts > 10 {
		return max
	}
	delay := time.Second << (attempts - 1)
	if delay > max {
		return max
	}
	return delay
}

// Purge deletes delivered rows older than the retention window.
func (d *Dispatcher) Purge(ctx context.Context) error {
	d.init()
	cutoff := time.Now().Add(-d.Retention).UnixMilli()
	if _, err := d.DB.ExecContext(ctx,
		`DELETE FROM outbox WHERE delivered_at IS NOT NULL AND delivered_at < ?`, cutoff,
	); err != nil {
		return fmt.Errorf("outbox: purge: %w", err)
	}
	return nil
}

// Pending returns the number of undelivered events (useful for /metrics).
func Pending(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE delivered_at IS NULL`).Scan(&n)
	return n, err
}
