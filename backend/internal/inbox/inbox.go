// Package inbox makes event consumption idempotent. Receive records the event
// ID and runs the handler in one transaction, so a redelivered event is ignored
// and a handler failure rolls the ID back so the producer's retry reprocesses it.
package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// HandlerFunc applies an event's effects using tx. Anything it writes (including
// new outbox rows) commits atomically with the dedupe record.
type HandlerFunc func(ctx context.Context, tx *sql.Tx, ev events.Event) error

// Receive processes ev exactly once. It returns processed=false (and a nil
// error) when the event was already handled.
func Receive(ctx context.Context, db *sql.DB, ev events.Event, handle HandlerFunc) (processed bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("inbox: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO inbox (event_id, type, received_at) VALUES (?, ?, ?) ON CONFLICT(event_id) DO NOTHING`,
		ev.ID, ev.Type, time.Now().UnixMilli(),
	)
	if err != nil {
		return false, fmt.Errorf("inbox: record %s: %w", ev.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := handle(ctx, tx, ev); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("inbox: commit: %w", err)
	}
	return true, nil
}

// Handler returns an http.Handler for POST /internal/events. It answers 202 once
// the event is processed (or recognised as a duplicate), 400 for a malformed
// envelope, and 500 when processing fails so the producer retries.
func Handler(db *sql.DB, handle HandlerFunc) http.Handler {
	return HandlerAfter(db, handle, nil)
}

// HandlerAfter is Handler with a hook that runs after a new event's transaction
// has committed (not for duplicates). Use it to wake the outbox dispatcher: the
// events the handler queued are only visible to it once the commit is done.
func HandlerAfter(db *sql.DB, handle HandlerFunc, after func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev events.Event
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_request", "could not read body")
			return
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_request", "invalid event JSON")
			return
		}
		if err := ev.Validate(); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		processed, err := Receive(r.Context(), db, ev, handle)
		if err != nil {
			httpx.Logger(r.Context()).Error("event handling failed", "event_id", ev.ID, "type", ev.Type, "err", err)
			httpx.WriteError(w, r, http.StatusInternalServerError, "event_failed", "event could not be processed")
			return
		}
		if processed && after != nil {
			after()
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"event_id": ev.ID, "duplicate": !processed})
	})
}
