package risk

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/inbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Service{DB: db, Clock: func() time.Time { return t0.Add(time.Minute) }}
}

// predicted wraps the sample in a heatwave.predicted event for the given location and observation time.
func predicted(t *testing.T, id string, locID, obsID int64, fetchedAt time.Time) events.Event {
	t.Helper()
	in := loadSample(t)
	in.Weather.Location.ID = locID
	in.Weather.ObservationID = obsID
	in.Weather.FetchedAt = fetchedAt
	payload, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: id, Type: contracts.EventHeatwavePredicted, Source: "prediction", CreatedAt: fetchedAt, Payload: payload}
}

func rawEvent(id string, payload string) events.Event {
	return events.Event{ID: id, Type: contracts.EventHeatwavePredicted, Payload: json.RawMessage(payload)}
}

func receive(t *testing.T, s *Service, ev events.Event) (bool, error) {
	t.Helper()
	return inbox.Receive(context.Background(), s.DB, ev, s.HandleEvent)
}

func count(t *testing.T, s *Service, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAssessesStoresAndQueuesForAlerting(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	if processed, err := receive(t, s, predicted(t, "e1", 7, 42, t0)); err != nil || !processed {
		t.Fatalf("receive = %v, %v", processed, err)
	}

	got, err := s.Latest(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location.ID != 7 || got.ObservationID != 42 || !got.FetchedAt.Equal(t0) ||
		got.Assessment.AlertLevel != engine.ExtremeDanger || len(got.Assessment.Days) != 7 {
		t.Errorf("stored assessment = %+v", got)
	}

	var target, typ, payload string
	if err := s.DB.QueryRow(`SELECT target, type, payload FROM outbox`).Scan(&target, &typ, &payload); err != nil {
		t.Fatalf("no outbox event: %v", err)
	}
	if target != contracts.TargetAlert || typ != contracts.EventRiskAssessed {
		t.Errorf("routed as %s/%s", target, typ)
	}
	var out contracts.RiskAssessed
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatal(err)
	}
	if out.Weather.Location.ID != 7 || out.Prediction.Method != "model" || out.Assessment.Peak.Date != "2026-05-05" ||
		!out.Assessment.HeatwaveExpected {
		t.Errorf("forwarded payload = %+v", out.Assessment.Peak)
	}
	if a, r, st := s.Counts(); a != 1 || r != 0 || st != 0 {
		t.Errorf("counts = %d/%d/%d", a, r, st)
	}
	if s.LevelCounts()[engine.ExtremeDanger] != 1 {
		t.Errorf("level counts = %v", s.LevelCounts())
	}
}

func TestRedeliveredEventIsAssessedOnce(t *testing.T) {
	s := newSvc(t)
	ev := predicted(t, "dup", 1, 1, t0)
	for i := 0; i < 3; i++ {
		if _, err := receive(t, s, ev); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 1 || count(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("a redelivered event produced duplicate assessments or alerts")
	}
}

func TestBadInputIsRecordedAndConsumedNotRetried(t *testing.T) {
	s, ctx := newSvc(t), context.Background()

	noLoc := predicted(t, "l", 0, 1, t0)
	noForecast := loadSample(t)
	for i := range noForecast.Weather.Days {
		noForecast.Weather.Days[i].Forecast = false
	}
	nfPayload, _ := json.Marshal(noForecast)
	badProb := loadSample(t)
	badProb.Prediction.Days[0].Probability = 7
	bpPayload, _ := json.Marshal(badProb)

	cases := []struct {
		name   string
		ev     events.Event
		reason string
	}{
		{"undecodable payload", rawEvent("u", `"just a string"`), ReasonUndecoded},
		{"no location id", noLoc, ReasonInvalid},
		{"no forecast days", rawEvent("f", string(nfPayload)), ReasonNoForecast},
		{"probability out of range", rawEvent("p", string(bpPayload)), ReasonInvalid},
	}
	for _, c := range cases {
		processed, err := receive(t, s, c.ev)
		if err != nil || !processed {
			t.Errorf("%s: receive = %v, %v; bad data must be consumed without error", c.name, processed, err)
		}
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 0 || count(t, s, `SELECT COUNT(*) FROM outbox`) != 0 {
		t.Error("rejected input leaked into assessments or alerts")
	}
	list, err := s.RecentRejections(ctx, 10)
	if err != nil || len(list) != len(cases) {
		t.Fatalf("rejections = %d, %v", len(list), err)
	}
	reasons := map[string]string{}
	for _, r := range list {
		reasons[r.EventID] = r.Reason
	}
	for _, c := range cases {
		if reasons[c.ev.ID] != c.reason {
			t.Errorf("%s: reason = %q, want %q", c.name, reasons[c.ev.ID], c.reason)
		}
	}
	for _, r := range list {
		if r.EventID == "f" && (r.LocationID == nil || *r.LocationID != 1) {
			t.Errorf("rejection lost its location: %+v", r)
		}
		if r.EventID == "u" && r.LocationID != nil {
			t.Error("an undecodable payload has no location to record")
		}
	}
}

func TestOutOfOrderDeliveryNeverReplacesNewerAssessments(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	receive(t, s, predicted(t, "new", 1, 2, t0.Add(10*time.Minute)))
	receive(t, s, predicted(t, "old", 1, 1, t0))
	receive(t, s, predicted(t, "same", 1, 3, t0.Add(10*time.Minute)))

	if got, _ := s.Latest(ctx, 1); got.ObservationID != 2 {
		t.Errorf("latest observation = %d, want 2", got.ObservationID)
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 1 || count(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("stale data was stored or alerted on")
	}
	if _, _, stale := s.Counts(); stale != 2 {
		t.Errorf("stale = %d, want 2", stale)
	}
	receive(t, s, predicted(t, "other", 2, 4, t0)) // another location has its own clock
	receive(t, s, predicted(t, "newest", 1, 5, t0.Add(20*time.Minute)))
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 3 {
		t.Errorf("assessments = %d, want 3", count(t, s, `SELECT COUNT(*) FROM assessments`))
	}
}

func TestUnknownEventTypeIsIgnored(t *testing.T) {
	s := newSvc(t)
	ev := events.Event{ID: "x", Type: "something.else", Payload: json.RawMessage(`{}`)}
	if processed, err := receive(t, s, ev); err != nil || !processed {
		t.Errorf("= %v, %v", processed, err)
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`)+count(t, s, `SELECT COUNT(*) FROM rejections`) != 0 {
		t.Error("an unknown event type had side effects")
	}
}

func TestTransientFailureRollsBackSoTheRetryWorks(t *testing.T) {
	s := newSvc(t)
	ev := predicted(t, "retry", 1, 1, t0)
	if _, err := s.DB.Exec(`ALTER TABLE outbox RENAME TO outbox_away`); err != nil {
		t.Fatal(err)
	}
	if _, err := receive(t, s, ev); err == nil {
		t.Fatal("expected an error while the outbox is unavailable")
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 0 || count(t, s, `SELECT COUNT(*) FROM inbox`) != 0 {
		t.Fatal("a failed attempt left an assessment or dedupe record behind; the retry would be skipped")
	}
	s.DB.Exec(`ALTER TABLE outbox_away RENAME TO outbox`)
	if processed, err := receive(t, s, ev); err != nil || !processed {
		t.Fatalf("retry = %v, %v", processed, err)
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 1 || count(t, s, `SELECT COUNT(*) FROM outbox`) != 1 {
		t.Error("retry did not complete the work")
	}
}

func TestPurgeRemovesOnlyExpiredData(t *testing.T) {
	s, ctx := newSvc(t), context.Background()
	now := s.Clock()
	ins := func(age time.Duration) {
		s.DB.Exec(`INSERT INTO assessments (location_id, observation_id, fetched_at, assessed_at, now_level, peak_level, alert_level, payload)
		           VALUES (1, 1, ?, ?, 'normal', 'normal', 'normal', '{}')`, now.Add(-age).UnixMilli(), now.UnixMilli())
	}
	ins(48 * time.Hour)
	ins(time.Hour)
	s.DB.Exec(`INSERT INTO rejections (event_id, reason, detail, created_at) VALUES ('a', 'r', 'd', ?)`, now.Add(-10*24*time.Hour).UnixMilli())
	s.DB.Exec(`INSERT INTO rejections (event_id, reason, detail, created_at) VALUES ('b', 'r', 'd', ?)`, now.Add(-time.Hour).UnixMilli())
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, s, `SELECT COUNT(*) FROM assessments`) != 1 || count(t, s, `SELECT COUNT(*) FROM rejections`) != 1 {
		t.Error("purge removed the wrong rows")
	}
}

func TestLatestForUnknownLocation(t *testing.T) {
	if _, err := newSvc(t).Latest(context.Background(), 99); err != ErrNotFound {
		t.Errorf("err = %v", err)
	}
}

// ---- HTTP ----

func newAPI(t *testing.T, d *outbox.Dispatcher) (*Service, http.Handler) {
	t.Helper()
	s := newSvc(t)
	app := httpx.New("risk")
	(&API{Svc: s, Dispatcher: d}).Register(app.Mux)
	return s, app.Handler()
}

func call(h http.Handler, method, path string, body []byte) (int, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(body)))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func post(t *testing.T, h http.Handler, ev events.Event) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(ev)
	return call(h, "POST", "/internal/events", body)
}

func TestEventEndpointThenRiskEndpoint(t *testing.T) {
	_, h := newAPI(t, nil)
	if status, _ := call(h, "GET", "/locations/3/risk", nil); status != 404 {
		t.Errorf("before any data = %d", status)
	}
	if status, resp := post(t, h, predicted(t, "e1", 3, 9, t0)); status != http.StatusAccepted || resp["duplicate"] != false {
		t.Fatalf("event = %d %v", status, resp)
	}
	if status, resp := post(t, h, predicted(t, "e1", 3, 9, t0)); status != http.StatusAccepted || resp["duplicate"] != true {
		t.Errorf("redelivery = %d %v", status, resp)
	}

	status, body := call(h, "GET", "/locations/3/risk", nil)
	if status != 200 || body["observationId"].(float64) != 9 {
		t.Fatalf("risk = %d %v", status, body)
	}
	a := body["assessment"].(map[string]any)
	if a["alertLevel"] != "extreme-danger" || a["heatwaveExpected"] != true || len(a["days"].([]any)) != 7 {
		t.Errorf("assessment = %v", a)
	}
	if now := a["now"].(map[string]any); now["level"] != "extreme-caution" || now["reason"] == "" {
		t.Errorf("now = %v", now)
	}
	if len(a["rationale"].([]any)) < 3 {
		t.Errorf("rationale = %v", a["rationale"])
	}
}

func TestEndpointValidation(t *testing.T) {
	_, h := newAPI(t, nil)
	for _, id := range []string{"abc", "0", "-2"} {
		if status, _ := call(h, "GET", "/locations/"+id+"/risk", nil); status != 400 {
			t.Errorf("id %q = %d", id, status)
		}
	}
	for name, body := range map[string]string{
		"not json": `{`, "no id": `{"type":"heatwave.predicted","payload":{}}`, "no payload": `{"id":"x","type":"heatwave.predicted"}`,
	} {
		if status, _ := call(h, "POST", "/internal/events", []byte(body)); status != 400 {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}
	for _, q := range []string{"limit=0", "limit=abc", "limit=501"} {
		if status, _ := call(h, "GET", "/rejections?"+q, nil); status != 400 {
			t.Errorf("?%s = %d", q, status)
		}
	}
	post(t, h, rawEvent("bad", `"nope"`))
	status, resp := call(h, "GET", "/rejections?limit=1", nil)
	if list, _ := resp["rejections"].([]any); status != 200 || len(list) != 1 {
		t.Errorf("rejections = %d %v", status, resp)
	}
}

func TestAssessedEventReachesAlertingPromptly(t *testing.T) {
	var (
		mu  sync.Mutex
		got []events.Event
	)
	alert := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev events.Event
		json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer alert.Close()

	s := newSvc(t)
	d := &outbox.Dispatcher{DB: s.DB, Source: "risk", Targets: map[string]string{contracts.TargetAlert: alert.URL}, Interval: time.Hour}
	app := httpx.New("risk")
	(&API{Svc: s, Dispatcher: d}).Register(app.Mux)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	if status, _ := post(t, app.Handler(), predicted(t, "e1", 1, 1, t0)); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Type != contracts.EventRiskAssessed || got[0].Source != "risk" {
		t.Fatalf("alert service received %+v; the dispatcher (interval 1h) must be woken after commit", got)
	}
	var payload contracts.RiskAssessed
	if err := got[0].Decode(&payload); err != nil || payload.Assessment.AlertLevel != engine.ExtremeDanger {
		t.Errorf("payload = %+v, %v", payload.Assessment.AlertLevel, err)
	}
}
