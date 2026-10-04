package processing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/weather"
)

func newAPI(t *testing.T, d *outbox.Dispatcher) (*Service, http.Handler) {
	t.Helper()
	s := newSvc(t)
	app := httpx.New("processing")
	(&API{Svc: s, Dispatcher: d}).Register(app.Mux)
	return s, app.Handler()
}

func call(h http.Handler, method, path string, body []byte) (int, map[string]any) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func post(t *testing.T, h http.Handler, ev events.Event) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return call(h, "POST", "/internal/events", body)
}

func TestEventEndpointThenMetricsEndpoint(t *testing.T) {
	_, h := newAPI(t, nil)

	if status, _ := call(h, "GET", "/locations/3/metrics", nil); status != 404 {
		t.Errorf("before any data = %d, want 404", status)
	}

	status, resp := post(t, h, updated(t, "e1", 3, 9, t0, sim(t, weather.ScenarioBuilding, 0)))
	if status != http.StatusAccepted || resp["duplicate"] != false {
		t.Fatalf("event = %d %v", status, resp)
	}
	if status, resp := post(t, h, updated(t, "e1", 3, 9, t0, sim(t, weather.ScenarioBuilding, 0))); status != http.StatusAccepted || resp["duplicate"] != true {
		t.Errorf("redelivery = %d %v, want 202 duplicate", status, resp)
	}

	status, full := call(h, "GET", "/locations/3/metrics", nil)
	if status != 200 || full["hourly"] == nil || len(full["days"].([]any)) != 10 {
		t.Fatalf("metrics = %d, hourly present = %v", status, full["hourly"] != nil)
	}
	if cur := full["current"].(map[string]any); cur["heatIndexC"] == nil || cur["temperatureC"] == nil {
		t.Errorf("current = %v", cur)
	}

	status, slim := call(h, "GET", "/locations/3/metrics?hourly=false", nil)
	if status != 200 || slim["hourly"] != nil || slim["days"] == nil {
		t.Errorf("hourly=false: status %d, hourly = %v", status, slim["hourly"] != nil)
	}
}

func TestEventEndpointRejectsMalformedEnvelopes(t *testing.T) {
	_, h := newAPI(t, nil)
	for name, body := range map[string]string{
		"not json":   `{`,
		"no id":      `{"type":"weather.updated","payload":{}}`,
		"no payload": `{"id":"x","type":"weather.updated"}`,
	} {
		if status, _ := call(h, "POST", "/internal/events", []byte(body)); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
}

func TestMetricsEndpointValidatesID(t *testing.T) {
	_, h := newAPI(t, nil)
	for _, id := range []string{"abc", "0", "-2"} {
		if status, _ := call(h, "GET", "/locations/"+id+"/metrics", nil); status != 400 {
			t.Errorf("id %q = %d, want 400", id, status)
		}
	}
}

func TestRejectionsEndpoint(t *testing.T) {
	_, h := newAPI(t, nil)
	bad := sim(t, weather.ScenarioNormal, 0)
	bad.Hourly.Temperature2m = nil
	post(t, h, updated(t, "bad1", 2, 5, t0, bad))

	status, resp := call(h, "GET", "/rejections", nil)
	list, _ := resp["rejections"].([]any)
	if status != 200 || len(list) != 1 || list[0].(map[string]any)["reason"] != ReasonInvalid {
		t.Fatalf("= %d %v", status, resp)
	}
	for _, q := range []string{"limit=0", "limit=abc", "limit=501"} {
		if status, _ := call(h, "GET", "/rejections?"+q, nil); status != 400 {
			t.Errorf("?%s = %d, want 400", q, status)
		}
	}
	if status, resp := call(h, "GET", "/rejections?limit=1", nil); status != 200 || len(resp["rejections"].([]any)) != 1 {
		t.Errorf("limit=1 = %d %v", status, resp)
	}
}

func TestProcessedEventIsDeliveredDownstreamPromptly(t *testing.T) {
	var (
		mu  sync.Mutex
		got []events.Event
	)
	prediction := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev events.Event
		json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer prediction.Close()

	s := newSvc(t)
	d := &outbox.Dispatcher{DB: s.DB, Source: "processing", Targets: map[string]string{contracts.TargetPrediction: prediction.URL}, Interval: time.Hour}
	app := httpx.New("processing")
	(&API{Svc: s, Dispatcher: d}).Register(app.Mux)

	ctx, cancel := contextWithCancel(t)
	defer cancel()
	go d.Run(ctx)
	time.Sleep(50 * time.Millisecond) // initial (empty) pass

	if status, _ := post(t, app.Handler(), updated(t, "e1", 1, 1, t0, sim(t, weather.ScenarioNormal, 0))); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(got) == 1
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Type != contracts.EventWeatherProcessed || got[0].Source != "processing" {
		t.Fatalf("prediction received %+v; the dispatcher (interval 1h) must have been woken after commit", got)
	}
}
