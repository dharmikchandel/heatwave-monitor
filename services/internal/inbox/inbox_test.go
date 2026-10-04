package inbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
)

const testSchema = `CREATE TABLE applied (n INTEGER)`

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func event(id string) events.Event {
	return events.Event{ID: id, Type: "t", Source: "s", CreatedAt: time.Now(), Payload: json.RawMessage(`{"a":1}`)}
}

func insert(ctx context.Context, tx *sql.Tx, _ events.Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO applied (n) VALUES (1)`)
	return err
}

func count(db *sql.DB) (n int) {
	db.QueryRow(`SELECT COUNT(*) FROM applied`).Scan(&n)
	return
}

func TestReceiveProcessesOnce(t *testing.T) {
	db, ctx := newDB(t), context.Background()
	for i := 0; i < 3; i++ {
		processed, err := Receive(ctx, db, event("dup"), insert)
		if err != nil {
			t.Fatal(err)
		}
		if processed != (i == 0) {
			t.Errorf("delivery %d: processed = %v", i+1, processed)
		}
	}
	if n := count(db); n != 1 {
		t.Errorf("handler effects applied %d times, want 1", n)
	}
}

func TestHandlerFailureRollsBackSoRetryWorks(t *testing.T) {
	db, ctx := newDB(t), context.Background()
	boom := errors.New("boom")

	if _, err := Receive(ctx, db, event("e1"), func(ctx context.Context, tx *sql.Tx, ev events.Event) error {
		insert(ctx, tx, ev)
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n := count(db); n != 0 {
		t.Fatalf("failed handler left %d rows behind", n)
	}

	processed, err := Receive(ctx, db, event("e1"), insert)
	if err != nil || !processed {
		t.Fatalf("retry = %v, %v; want processed", processed, err)
	}
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/events", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPHandler(t *testing.T) {
	db := newDB(t)
	h := Handler(db, insert)

	body, _ := json.Marshal(event("h1"))
	if rec := post(t, h, string(body)); rec.Code != http.StatusAccepted {
		t.Fatalf("first = %d %s", rec.Code, rec.Body)
	}
	rec := post(t, h, string(body))
	var resp struct{ Duplicate bool }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusAccepted || !resp.Duplicate || count(db) != 1 {
		t.Errorf("duplicate = %d %s, rows=%d", rec.Code, rec.Body, count(db))
	}

	for name, bad := range map[string]string{
		"not json":     `{`,
		"missing id":   `{"type":"t","payload":{}}`,
		"missing type": `{"id":"x","payload":{}}`,
		"no payload":   `{"id":"x","type":"t"}`,
	} {
		if rec := post(t, h, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}

	failing := Handler(db, func(context.Context, *sql.Tx, events.Event) error { return errors.New("db down") })
	fb, _ := json.Marshal(event("h2"))
	if rec := post(t, failing, string(fb)); rec.Code != http.StatusInternalServerError {
		t.Errorf("handler error = %d, want 500 so the producer retries", rec.Code)
	}
}
