package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func enqueue(t *testing.T, db *sql.DB, target, typ string, payload any) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	id, err := Enqueue(context.Background(), tx, target, typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

// receiver is a fake downstream service recording what it was sent.
type receiver struct {
	mu     sync.Mutex
	got    []events.Event
	status int // response code; 0 means 202
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.status != 0 && rc.status/100 != 2 {
		w.WriteHeader(rc.status)
		return
	}
	var ev events.Event
	json.NewDecoder(r.Body).Decode(&ev)
	rc.got = append(rc.got, ev)
	w.WriteHeader(http.StatusAccepted)
}

func (rc *receiver) setStatus(s int) { rc.mu.Lock(); rc.status = s; rc.mu.Unlock() }
func (rc *receiver) events() []events.Event {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]events.Event(nil), rc.got...)
}

func TestDeliversEnvelope(t *testing.T) {
	db := newDB(t)
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	id := enqueue(t, db, "processing", "weather.updated", map[string]int{"n": 7})
	d := &Dispatcher{DB: db, Source: "weather", Targets: map[string]string{"processing": srv.URL}}

	n, err := d.DispatchOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("DispatchOnce = %d, %v", n, err)
	}
	got := rc.events()
	if len(got) != 1 || got[0].ID != id || got[0].Type != "weather.updated" || got[0].Source != "weather" {
		t.Fatalf("unexpected delivery: %+v", got)
	}
	var p map[string]int
	if err := got[0].Decode(&p); err != nil || p["n"] != 7 {
		t.Errorf("payload = %v, %v", p, err)
	}
	if pending, _ := Pending(context.Background(), db); pending != 0 {
		t.Errorf("pending = %d, want 0", pending)
	}
	// Delivered rows are not sent again.
	if n, _ := d.DispatchOnce(context.Background()); n != 0 || len(rc.events()) != 1 {
		t.Errorf("redelivered a delivered event (n=%d, total=%d)", n, len(rc.events()))
	}
}

func TestRollbackLeavesNoEvent(t *testing.T) {
	db := newDB(t)
	tx, _ := db.Begin()
	if _, err := Enqueue(context.Background(), tx, "x", "t", 1); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if n, _ := Pending(context.Background(), db); n != 0 {
		t.Errorf("pending = %d after rollback, want 0", n)
	}
}

func TestRetriesWithBackoffThenSucceeds(t *testing.T) {
	db := newDB(t)
	rc := &receiver{status: http.StatusInternalServerError}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	enqueue(t, db, "risk", "heatwave.predicted", 1)
	d := &Dispatcher{DB: db, Source: "prediction", Targets: map[string]string{"risk": srv.URL}}
	ctx := context.Background()

	if n, err := d.DispatchOnce(ctx); err != nil || n != 0 {
		t.Fatalf("failing pass = %d, %v", n, err)
	}
	var attempts int
	var lastErr sql.NullString
	db.QueryRow(`SELECT attempts, last_error FROM outbox`).Scan(&attempts, &lastErr)
	if attempts != 1 || !lastErr.Valid {
		t.Fatalf("attempts=%d last_error=%v", attempts, lastErr)
	}

	// Backoff: an immediate second pass must not retry yet.
	rc.setStatus(0)
	if n, _ := d.DispatchOnce(ctx); n != 0 {
		t.Fatal("retried before backoff elapsed")
	}

	// Fast-forward the retry clock.
	db.Exec(`UPDATE outbox SET next_attempt_at = 0`)
	if n, err := d.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("retry pass = %d, %v", n, err)
	}
	if len(rc.events()) != 1 {
		t.Errorf("deliveries = %d, want 1", len(rc.events()))
	}
}

func TestPreservesOrderPerTargetAndIsolatesTargets(t *testing.T) {
	db := newDB(t)
	bad := &receiver{status: http.StatusServiceUnavailable}
	good := &receiver{}
	badSrv, goodSrv := httptest.NewServer(bad), httptest.NewServer(good)
	defer badSrv.Close()
	defer goodSrv.Close()

	enqueue(t, db, "bad", "e", "bad-1")
	enqueue(t, db, "bad", "e", "bad-2")
	enqueue(t, db, "good", "e", "good-1")

	d := &Dispatcher{DB: db, Source: "s", Targets: map[string]string{"bad": badSrv.URL, "good": goodSrv.URL}}
	n, _ := d.DispatchOnce(context.Background())
	if n != 1 || len(good.events()) != 1 {
		t.Fatalf("healthy target blocked by failing one: delivered=%d", n)
	}
	var attempts int
	db.QueryRow(`SELECT attempts FROM outbox WHERE payload = '"bad-2"'`).Scan(&attempts)
	if attempts != 0 {
		t.Error("bad-2 was attempted ahead of failing bad-1; order not preserved")
	}

	// Once the target recovers, events arrive in order.
	bad.setStatus(0)
	db.Exec(`UPDATE outbox SET next_attempt_at = 0`)
	d.DispatchOnce(context.Background())
	var order []string
	for _, ev := range bad.events() {
		var s string
		ev.Decode(&s)
		order = append(order, s)
	}
	if len(order) != 2 || order[0] != "bad-1" || order[1] != "bad-2" {
		t.Errorf("order = %v", order)
	}
}

func TestUnknownTargetIsRetriedNotDropped(t *testing.T) {
	db := newDB(t)
	enqueue(t, db, "nowhere", "e", 1)
	d := &Dispatcher{DB: db, Source: "s", Targets: map[string]string{}}
	if n, err := d.DispatchOnce(context.Background()); n != 0 || err != nil {
		t.Fatalf("= %d, %v", n, err)
	}
	if n, _ := Pending(context.Background(), db); n != 1 {
		t.Errorf("pending = %d, want 1 (event must survive a config error)", n)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	max := 30 * time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := backoff(i+1, max); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(500, max); got != max {
		t.Errorf("backoff(500) = %v, want cap", got)
	}
}

func TestPurgeRemovesOnlyOldDelivered(t *testing.T) {
	db := newDB(t)
	enqueue(t, db, "t", "e", "old-delivered")
	enqueue(t, db, "t", "e", "new-delivered")
	enqueue(t, db, "t", "e", "pending")
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	db.Exec(`UPDATE outbox SET delivered_at = ? WHERE payload = '"old-delivered"'`, old)
	db.Exec(`UPDATE outbox SET delivered_at = ? WHERE payload = '"new-delivered"'`, time.Now().UnixMilli())

	d := &Dispatcher{DB: db}
	if err := d.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	if n != 2 {
		t.Errorf("rows after purge = %d, want 2", n)
	}
}

func TestRunDeliversOnNotifyAndStopsOnCancel(t *testing.T) {
	db := newDB(t)
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	d := &Dispatcher{DB: db, Source: "s", Targets: map[string]string{"t": srv.URL}, Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond) // let the initial pass finish with an empty outbox
	enqueue(t, db, "t", "e", 1)
	d.Notify()

	deadline := time.Now().Add(2 * time.Second)
	for len(rc.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(rc.events()) != 1 {
		t.Fatal("Notify did not trigger a prompt delivery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

// Events enqueued in the same millisecond must still be delivered in the order
// they were enqueued: IDs are time-prefixed but random within a millisecond, so
// ordering by ID alone would shuffle them.
func TestSameMillisecondEventsKeepEnqueueOrder(t *testing.T) {
	db := newDB(t)
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := Enqueue(context.Background(), tx, "t", "e", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	d := &Dispatcher{DB: db, Source: "s", Targets: map[string]string{"t": srv.URL}}
	if delivered, err := d.DispatchOnce(context.Background()); err != nil || delivered != n {
		t.Fatalf("DispatchOnce = %d, %v", delivered, err)
	}
	for i, ev := range rc.events() {
		var got int
		ev.Decode(&got)
		if got != i {
			t.Fatalf("event %d arrived as #%d: order was not preserved", i, got)
		}
	}
}
