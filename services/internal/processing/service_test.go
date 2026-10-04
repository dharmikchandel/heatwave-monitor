package processing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/inbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/weather"
)

var t0 = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

func newSvc(t *testing.T) *Service {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Service{DB: db, Clock: func() time.Time { return t0.Add(time.Minute) }}
}

// updated builds a weather.updated event for a location observed at fetchedAt.
func updated(t *testing.T, id string, locID int64, obsID int64, fetchedAt time.Time, snap contracts.RawSnapshot) events.Event {
	t.Helper()
	loc := testLoc
	loc.ID = locID
	payload, err := json.Marshal(contracts.WeatherUpdated{ObservationID: obsID, Location: loc, Source: "simulated", FetchedAt: fetchedAt, Snapshot: snap})
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: id, Type: contracts.EventWeatherUpdated, Source: "weather", CreatedAt: fetchedAt, Payload: payload}
}

func receive(t *testing.T, s *Service, ev events.Event) (processed bool, err error) {
	t.Helper()
	return inbox.Receive(context.Background(), s.DB, ev, s.HandleEvent)
}

func n(t *testing.T, s *Service, q string, args ...any) int {
	t.Helper()
	var c int
	if err := s.DB.QueryRow(q, args...).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProcessesEventStoresSnapshotAndQueuesForPrediction(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	ev := updated(t, "e1", 7, 42, t0, sim(t, weather.ScenarioBuilding, 0))

	if processed, err := receive(t, s, ev); err != nil || !processed {
		t.Fatalf("receive = %v, %v", processed, err)
	}

	got, err := s.Latest(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservationID != 42 || got.Location.ID != 7 || got.Location.Name != "Mumbai" || got.Source != "simulated" ||
		!got.FetchedAt.Equal(t0) || got.ProcessedAt.IsZero() || got.Hourly == nil || len(got.Days) != 10 {
		t.Errorf("stored snapshot = %+v", got)
	}

	var target, typ, payload string
	if err := s.DB.QueryRow(`SELECT target, type, payload FROM outbox`).Scan(&target, &typ, &payload); err != nil {
		t.Fatalf("no outbox event: %v", err)
	}
	if target != contracts.TargetPrediction || typ != contracts.EventWeatherProcessed {
		t.Errorf("routed as %s/%s", target, typ)
	}
	var out contracts.ProcessedWeather
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hourly != nil {
		t.Error("the hourly series must not be forwarded downstream")
	}
	if out.Location.ID != 7 || out.ObservationID != 42 || len(out.Days) != 10 || out.Quality.Score != 1 {
		t.Errorf("forwarded payload = %+v", out)
	}
	if p, r, st := s.Counts(); p != 1 || r != 0 || st != 0 {
		t.Errorf("counts = %d/%d/%d", p, r, st)
	}
}

func TestRedeliveredEventIsProcessedOnce(t *testing.T) {
	s := newSvc(t)
	ev := updated(t, "dup", 1, 1, t0, sim(t, weather.ScenarioNormal, 0))
	for i := 0; i < 3; i++ {
		if _, err := receive(t, s, ev); err != nil {
			t.Fatal(err)
		}
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 1 || n(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("a redelivered event produced duplicate snapshots or downstream events")
	}
}

func TestBadDataIsRecordedAndConsumedNotRetried(t *testing.T) {
	s, ctx := newSvc(t), context.Background()

	ragged := sim(t, weather.ScenarioNormal, 0)
	ragged.Hourly.Temperature2m = ragged.Hourly.Temperature2m[:5]
	sparse := sim(t, weather.ScenarioNormal, 0)
	for i := range sparse.Hourly.Temperature2m {
		sparse.Hourly.Temperature2m[i] = nil
	}

	cases := []struct {
		name   string
		ev     events.Event
		reason string
	}{
		{"undecodable payload", events.Event{ID: "u", Type: contracts.EventWeatherUpdated, Payload: json.RawMessage(`"just a string"`)}, ReasonUndecodable},
		{"missing location id", updated(t, "l", 0, 1, t0, sim(t, weather.ScenarioNormal, 0)), ReasonInvalid},
		{"ragged snapshot", updated(t, "r", 1, 2, t0, ragged), ReasonInvalid},
		{"no usable temperature", updated(t, "s", 1, 3, t0, sparse), ReasonTooMuchMissing},
	}
	for _, c := range cases {
		processed, err := receive(t, s, c.ev)
		if err != nil || !processed {
			t.Errorf("%s: receive = %v, %v; bad data must be consumed without error so the producer stops retrying", c.name, processed, err)
		}
	}

	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 0 || n(t, s, `SELECT COUNT(*) FROM outbox`) != 0 {
		t.Error("rejected data leaked into snapshots or downstream events")
	}
	list, err := s.RecentRejections(ctx, 10)
	if err != nil || len(list) != len(cases) {
		t.Fatalf("rejections = %d, %v", len(list), err)
	}
	byEvent := map[string]Rejection{}
	for _, r := range list {
		byEvent[r.EventID] = r
	}
	for _, c := range cases {
		if got := byEvent[c.ev.ID].Reason; got != c.reason {
			t.Errorf("%s: reason = %q, want %q", c.name, got, c.reason)
		}
	}
	if r := byEvent["s"]; r.LocationID == nil || *r.LocationID != 1 || r.ObservationID == nil || *r.ObservationID != 3 {
		t.Errorf("rejection lost its identifiers: %+v", r)
	}
	if byEvent["u"].LocationID != nil {
		t.Error("an undecodable payload has no location to record")
	}
	if _, rejected, _ := s.Counts(); rejected != int64(len(cases)) {
		t.Errorf("rejected counter = %d", rejected)
	}
}

func TestOutOfOrderDeliveryNeverReplacesNewerData(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	newer := sim(t, weather.ScenarioExtreme, 0)
	older := sim(t, weather.ScenarioNormal, 0)

	receive(t, s, updated(t, "new", 1, 2, t0.Add(10*time.Minute), newer))
	receive(t, s, updated(t, "old", 1, 1, t0, older))                      // delivered late
	receive(t, s, updated(t, "same", 1, 3, t0.Add(10*time.Minute), older)) // same instant as the newest

	got, _ := s.Latest(ctx, 1)
	if got.ObservationID != 2 {
		t.Errorf("latest observation = %d, want 2 (the newest)", got.ObservationID)
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 1 || n(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("stale data was stored or forwarded")
	}
	if _, _, stale := s.Counts(); stale != 2 {
		t.Errorf("stale counter = %d, want 2", stale)
	}

	// Another location is independent, and genuinely newer data is accepted.
	receive(t, s, updated(t, "other", 2, 4, t0, older))
	receive(t, s, updated(t, "newest", 1, 5, t0.Add(20*time.Minute), newer))
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 3 {
		t.Errorf("snapshots = %d, want 3 (loc1 newest, loc1 earlier, loc2)", n(t, s, `SELECT COUNT(*) FROM snapshots`))
	}
	if got, _ := s.Latest(ctx, 1); got.ObservationID != 5 {
		t.Errorf("latest = %d, want 5", got.ObservationID)
	}
}

func TestUnknownEventTypeIsIgnored(t *testing.T) {
	s := newSvc(t)
	ev := events.Event{ID: "x", Type: "something.else", Payload: json.RawMessage(`{}`)}
	if processed, err := receive(t, s, ev); err != nil || !processed {
		t.Errorf("= %v, %v", processed, err)
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`)+n(t, s, `SELECT COUNT(*) FROM rejections`) != 0 {
		t.Error("an unknown event type had side effects")
	}
}

func TestTransientFailureRollsBackSoRetryWorks(t *testing.T) {
	s := newSvc(t)
	ev := updated(t, "retry", 1, 1, t0, sim(t, weather.ScenarioNormal, 0))

	if _, err := s.DB.Exec(`ALTER TABLE outbox RENAME TO outbox_away`); err != nil {
		t.Fatal(err)
	}
	if _, err := receive(t, s, ev); err == nil {
		t.Fatal("expected an error while the outbox is unavailable")
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 0 || n(t, s, `SELECT COUNT(*) FROM inbox`) != 0 {
		t.Fatal("a failed attempt left a snapshot or a dedupe record behind; the retry would be skipped")
	}

	if _, err := s.DB.Exec(`ALTER TABLE outbox_away RENAME TO outbox`); err != nil {
		t.Fatal(err)
	}
	if processed, err := receive(t, s, ev); err != nil || !processed {
		t.Fatalf("retry = %v, %v", processed, err)
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 1 || n(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("retry did not complete the work")
	}
}

func TestPurgeRemovesOnlyExpiredData(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	s.Retention, s.RejectionRetention = 24*time.Hour, 7*24*time.Hour
	now := s.Clock()
	insert := func(table string, col string, age time.Duration) {
		t.Helper()
		var q string
		if table == "snapshots" {
			q = `INSERT INTO snapshots (location_id, observation_id, source, fetched_at, processed_at, quality, payload) VALUES (1, 1, 's', ?, ?, 1, '{}')`
			s.DB.Exec(q, now.Add(-age).UnixMilli(), now.UnixMilli())
		} else {
			q = `INSERT INTO rejections (event_id, reason, detail, created_at) VALUES ('e', 'r', 'd', ?)`
			s.DB.Exec(q, now.Add(-age).UnixMilli())
		}
	}
	insert("snapshots", "", 48*time.Hour)
	insert("snapshots", "", time.Hour)
	insert("rejections", "", 10*24*time.Hour)
	insert("rejections", "", 24*time.Hour)

	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if n(t, s, `SELECT COUNT(*) FROM snapshots`) != 1 || n(t, s, `SELECT COUNT(*) FROM rejections`) != 1 {
		t.Errorf("after purge: %d snapshots, %d rejections; want 1 and 1",
			n(t, s, `SELECT COUNT(*) FROM snapshots`), n(t, s, `SELECT COUNT(*) FROM rejections`))
	}
}

func TestLatestForUnknownLocation(t *testing.T) {
	s := newSvc(t)
	if _, err := s.Latest(context.Background(), 99); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(context.Background())
}
