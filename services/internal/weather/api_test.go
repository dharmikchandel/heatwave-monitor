package weather

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
)

type env struct {
	t   *testing.T
	svc *Service
	h   http.Handler
}

func newEnv(t *testing.T, src Source) *env {
	t.Helper()
	svc := newService(t, src)
	app := httpx.New("weather")
	(&API{Svc: svc}).Register(app.Mux)
	return &env{t: t, svc: svc, h: app.Handler()}
}

func (e *env) do(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			e.t.Fatalf("%s %s: response is not JSON: %q", method, path, rec.Body)
		}
	}
	return rec.Code, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAddLocationFetchesInBackgroundAndServesRaw(t *testing.T) {
	src := &fakeSource{}
	e := newEnv(t, src)

	status, loc := e.do("POST", "/locations", `{"name":"Mumbai","country":"India","latitude":19.076,"longitude":72.8777,"timezone":"Asia/Kolkata"}`)
	if status != http.StatusCreated || loc["name"] != "Mumbai" {
		t.Fatalf("POST = %d %v", status, loc)
	}
	id := int64(loc["id"].(float64))
	e.svc.Wait() // the initial fetch runs in the background

	status, raw := e.do("GET", "/locations/1/raw", "")
	if status != 200 || raw["source"] != "fake" || raw["locationId"].(float64) != float64(id) {
		t.Fatalf("raw = %d %v", status, raw)
	}
	if _, ok := raw["snapshot"].(map[string]any)["daily"]; !ok {
		t.Error("raw response has no snapshot.daily")
	}

	// The same place again is idempotent and does not fetch again.
	status, again := e.do("POST", "/locations", `{"name":"Mumbai","latitude":19.0761,"longitude":72.8779}`)
	e.svc.Wait()
	if status != http.StatusOK || again["id"] != loc["id"] {
		t.Errorf("second POST = %d %v", status, again)
	}
	if src.callCount() != 1 {
		t.Errorf("source called %d times, want 1", src.callCount())
	}
}

func TestAddLocationRejectsBadInput(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	cases := map[string]string{
		"empty name":      `{"name":"","latitude":1,"longitude":1}`,
		"latitude range":  `{"name":"x","latitude":123,"longitude":1}`,
		"longitude range": `{"name":"x","latitude":1,"longitude":999}`,
		"unknown field":   `{"name":"x","latitude":1,"longitude":1,"elevation":5}`,
		"wrong type":      `{"name":"x","latitude":"north","longitude":1}`,
		"not json":        `{`,
	}
	for name, body := range cases {
		status, resp := e.do("POST", "/locations", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d (%v), want 400", name, status, resp)
		}
	}
	if n := count(t, e.svc.DB, `SELECT COUNT(*) FROM locations`); n != 0 {
		t.Errorf("%d locations created from bad input", n)
	}
}

func TestAddLocationLimit(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	e.svc.MaxLocations = 1
	e.do("POST", "/locations", `{"name":"a","latitude":1,"longitude":1}`)
	status, resp := e.do("POST", "/locations", `{"name":"b","latitude":2,"longitude":2}`)
	e.svc.Wait()
	if status != http.StatusConflict || errCode(resp) != "location_limit" {
		t.Errorf("= %d %v", status, resp)
	}
}

func TestLocationLifecycle(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	e.do("POST", "/locations", `{"name":"Zeta","latitude":1,"longitude":1}`)
	e.do("POST", "/locations", `{"name":"alpha","latitude":2,"longitude":2}`)
	e.svc.Wait()

	status, list := e.do("GET", "/locations", "")
	locs := list["locations"].([]any)
	if status != 200 || len(locs) != 2 || locs[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("list = %d %v (want 2, case-insensitive name order)", status, list)
	}

	if status, loc := e.do("GET", "/locations/1", ""); status != 200 || loc["name"] != "Zeta" {
		t.Errorf("get = %d %v", status, loc)
	}
	if status, _ := e.do("DELETE", "/locations/1", ""); status != http.StatusNoContent {
		t.Errorf("delete = %d", status)
	}
	if status, resp := e.do("DELETE", "/locations/1", ""); status != 404 || errCode(resp) != "not_found" {
		t.Errorf("second delete = %d %v", status, resp)
	}
	if _, list := e.do("GET", "/locations", ""); len(list["locations"].([]any)) != 1 {
		t.Error("removed location still listed")
	}
	if status, _ := e.do("POST", "/locations/1/refresh", ""); status != 404 {
		t.Errorf("refresh of removed location = %d, want 404", status)
	}
}

func TestIDValidationAndNotFound(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	for _, path := range []string{"/locations/abc", "/locations/0", "/locations/-3", "/locations/1.5"} {
		if status, resp := e.do("GET", path, ""); status != 400 || errCode(resp) != "invalid_id" {
			t.Errorf("GET %s = %d %v, want 400 invalid_id", path, status, resp)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/locations/999"}, {"GET", "/locations/999/raw"}, {"POST", "/locations/999/refresh"}, {"DELETE", "/locations/999"},
	} {
		if status, resp := e.do(c.method, c.path, ""); status != 404 || errCode(resp) != "not_found" {
			t.Errorf("%s %s = %d %v, want 404 not_found", c.method, c.path, status, resp)
		}
	}
}

func TestRawBeforeFirstFetch(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	loc := addLoc(t, e.svc, "a", 1, 1)
	status, resp := e.do("GET", "/locations/1/raw", "")
	if status != 404 || errCode(resp) != "no_observation" {
		t.Errorf("= %d %v (id %d)", status, resp, loc.ID)
	}
}

func TestRefreshEndpoint(t *testing.T) {
	src := &fakeSource{}
	e := newEnv(t, src)
	addLoc(t, e.svc, "a", 1, 1)

	status, resp := e.do("POST", "/locations/1/refresh", "")
	if status != 200 || resp["source"] != "fake" || resp["observationId"] == nil {
		t.Fatalf("= %d %v", status, resp)
	}

	src.mu.Lock()
	src.fn = func(contracts.Location) (contracts.RawSnapshot, error) {
		return contracts.RawSnapshot{}, &SourceError{Status: 429, Msg: "Too many requests"}
	}
	src.mu.Unlock()
	status, resp = e.do("POST", "/locations/1/refresh", "")
	if status != http.StatusBadGateway || errCode(resp) != "source_unavailable" {
		t.Fatalf("= %d %v, want 502 source_unavailable", status, resp)
	}
	if msg := resp["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "Too many requests") {
		t.Errorf("message hides the upstream reason: %q", msg)
	}
	if _, loc := e.do("GET", "/locations/1", ""); loc["lastError"] == nil {
		t.Error("lastError not visible on the location")
	}
}

func TestSimulationEndpointsOnRealSource(t *testing.T) {
	e := newEnv(t, &fakeSource{})
	status, resp := e.do("GET", "/simulation", "")
	if status != 200 || resp["enabled"] != false || resp["source"] != "fake" {
		t.Errorf("GET = %d %v", status, resp)
	}
	if status, resp := e.do("PUT", "/simulation", `{"scenario":"extreme"}`); status != http.StatusConflict || errCode(resp) != "not_simulated" {
		t.Errorf("PUT = %d %v, want 409 not_simulated", status, resp)
	}
}

func TestSimulationEndpoints(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.Source = &Simulated{Scenarios: e.svc, Now: noon}
	addLoc(t, e.svc, "a", 1, 1)
	addLoc(t, e.svc, "b", 2, 2)

	status, resp := e.do("GET", "/simulation", "")
	if status != 200 || resp["enabled"] != true || resp["scenario"] != "normal" {
		t.Fatalf("GET = %d %v", status, resp)
	}

	// Global change refreshes every location so it flows through right away.
	status, resp = e.do("PUT", "/simulation", `{"scenario":"extreme"}`)
	if status != 200 || resp["refreshed"].(float64) != 2 {
		t.Fatalf("PUT = %d %v", status, resp)
	}
	if n := count(t, e.svc.DB, `SELECT COUNT(*) FROM outbox`); n != 2 {
		t.Errorf("%d events queued, want 2", n)
	}
	_, raw := e.do("GET", "/locations/1/raw", "")
	cur := raw["snapshot"].(map[string]any)["current"].(map[string]any)
	if cur["apparentTemperature"].(float64) < 40 {
		t.Errorf("extreme scenario produced apparent temperature %v", cur["apparentTemperature"])
	}

	// Per-location override, without refreshing.
	status, resp = e.do("PUT", "/simulation", `{"scenario":"normal","locationId":2,"refresh":false}`)
	if status != 200 || resp["refreshed"].(float64) != 0 {
		t.Fatalf("PUT override = %d %v", status, resp)
	}
	_, resp = e.do("GET", "/simulation", "")
	if resp["scenario"] != "extreme" || resp["overrides"].(map[string]any)["2"] != "normal" {
		t.Errorf("state = %v", resp)
	}

	if status, resp := e.do("PUT", "/simulation", `{"scenario":"apocalypse"}`); status != 400 || errCode(resp) != "validation_failed" {
		t.Errorf("bad scenario = %d %v", status, resp)
	}
	if status, _ := e.do("PUT", "/simulation", `{"scenario":"normal","locationId":999}`); status != 404 {
		t.Errorf("unknown location = %d, want 404", status)
	}
	if status, _ := e.do("PUT", "/simulation", `{"scenario":"normal","bogus":1}`); status != 400 {
		t.Errorf("unknown field = %d, want 400", status)
	}
}

func TestStaleContextDoesNotLeakIntoBackgroundFetch(t *testing.T) {
	// The initial fetch must survive the HTTP request finishing (its context is cancelled).
	src := &fakeSource{}
	e := newEnv(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/locations", strings.NewReader(`{"name":"a","latitude":1,"longitude":1}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	cancel() // request is over
	e.svc.Wait()

	if src.callCount() != 1 || count(t, e.svc.DB, `SELECT COUNT(*) FROM observations`) != 1 {
		t.Errorf("background fetch was cancelled with the request (calls=%d)", src.callCount())
	}
}
