package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func climate(g *gw) (resp, map[string]any) {
	g.t.Helper()
	r := g.get("/api/v1/locations/7/climate")
	return r, r.json()
}

func sources(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m["sources"].(map[string]any) {
		out[k] = v.(string)
	}
	return out
}

func TestClimateComposesEveryService(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r, c := climate(g)
	if r.Code != 200 || c["status"] != "ok" || c["degraded"] != false {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	if c["location"].(map[string]any)["name"] != "Mumbai" {
		t.Errorf("location = %v", c["location"])
	}
	if c["weather"].(map[string]any)["current"].(map[string]any)["temperatureC"] != 39.1 {
		t.Errorf("weather = %v", c["weather"])
	}
	if c["prediction"].(map[string]any)["method"] != "model" {
		t.Errorf("prediction = %v (must be the inner prediction object, not the service envelope)", c["prediction"])
	}
	if c["risk"].(map[string]any)["alertLevel"] != "extreme-danger" {
		t.Errorf("risk = %v (must be the assessment)", c["risk"])
	}
	if a := c["alerts"].([]any); len(a) != 1 || a[0].(map[string]any)["status"] != "open" {
		t.Errorf("alerts = %v", c["alerts"])
	}
	for name, state := range sources(c) {
		if state != "ok" {
			t.Errorf("source %s = %s", name, state)
		}
	}
	if c["generatedAt"] == nil {
		t.Error("no generatedAt")
	}

	// What each backend was asked.
	for _, w := range []struct {
		f           *fake
		path, query string
	}{
		{b.weather, "/locations/7", ""},
		{b.processing, "/locations/7/metrics", ""},
		{b.prediction, "/locations/7/prediction", ""},
		{b.risk, "/locations/7/risk", ""},
		{b.alert, "/alerts", "locationId=7&status=open&limit=20"},
	} {
		if got := w.f.last(); got.path != w.path || got.query != w.query || got.method != "GET" {
			t.Errorf("backend saw %+v, want GET %s?%s", got, w.path, w.query)
		}
	}
}

func TestClimateHourlyOptOutIsForwarded(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	g.get("/api/v1/locations/7/climate?hourly=false")
	if q := b.processing.last().query; q != "hourly=false" {
		t.Errorf("processing query = %q", q)
	}
	g.get("/api/v1/locations/7/climate?hourly=true&evil=1")
	if q := b.processing.last().query; q != "" {
		t.Errorf("only the hourly opt-out is forwarded, got %q", q)
	}
}

func TestClimateSurvivesAnyOneServiceBeingDown(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(b *backends)
		source string
		lost   string // the field that goes null
	}{
		{"processing", func(b *backends) { b.processing.Close() }, "processing", "weather"},
		{"prediction", func(b *backends) { b.prediction.Close() }, "prediction", "prediction"},
		{"risk", func(b *backends) { b.risk.Close() }, "risk", "risk"},
		{"alert", func(b *backends) { b.alert.Close() }, "alerts", "alerts"},
		{"weather", func(b *backends) { b.weather.Close() }, "weather", ""},
	}
	for _, tc := range cases {
		b := healthyBackends(t)
		tc.break_(b)
		g := newGW(t, b.config())
		r, c := climate(g)
		if r.Code != 200 || c["status"] != "partial" || c["degraded"] != true {
			t.Errorf("%s down: %d status=%v degraded=%v", tc.name, r.Code, c["status"], c["degraded"])
			continue
		}
		if sources(c)[tc.source] != "unavailable" {
			t.Errorf("%s down: sources = %v", tc.name, sources(c))
		}
		for _, field := range []string{"location", "weather", "prediction", "risk", "alerts"} {
			if field == tc.lost {
				if c[field] != nil {
					t.Errorf("%s down: %s should be null, got %v", tc.name, field, c[field])
				}
			} else if c[field] == nil {
				t.Errorf("%s down: %s is missing although its service is fine", tc.name, field)
			}
		}
	}
}

func TestLocationFallsBackToOtherServicesWhenTheRegistryIsDown(t *testing.T) {
	b := healthyBackends(t)
	b.weather.Close()
	g := newGW(t, b.config())
	if _, c := climate(g); c["location"].(map[string]any)["name"] != "Mumbai" {
		t.Errorf("location from processing = %v", c["location"])
	}

	b.processing.Close()
	if _, c := climate(g); c["location"] == nil || c["location"].(map[string]any)["name"] != "Mumbai" {
		t.Errorf("location from risk = %v", c["location"])
	}

	b.risk.Close()
	r, c := climate(g)
	if r.Code != 200 || c["location"] != nil || c["prediction"] == nil || c["degraded"] != true {
		t.Errorf("with only prediction and alerts left: %d %s", r.Code, r.Body)
	}
}

func TestClimateEverythingDownIs503(t *testing.T) {
	b := healthyBackends(t)
	for _, f := range []*fake{b.weather, b.processing, b.prediction, b.risk, b.alert} {
		f.Close()
	}
	g := newGW(t, b.config())
	if r := g.get("/api/v1/locations/7/climate"); r.Code != 503 || r.errCode() != "unavailable" {
		t.Errorf("= %d %s", r.Code, r.Body)
	}
}

func TestAnUnknownLocationIs404EvenIfOtherServicesHaveData(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(jsonHandler(404, `{"error":{"code":"not_found"}}`))
	g := newGW(t, b.config())
	if r := g.get("/api/v1/locations/7/climate"); r.Code != 404 || r.errCode() != "not_found" {
		t.Errorf("= %d %s", r.Code, r.Body)
	}
}

func TestANewLocationIsWarmingNotBroken(t *testing.T) {
	b := healthyBackends(t)
	nodata := jsonHandler(404, `{"error":{"code":"no_data"}}`)
	for _, f := range []*fake{b.processing, b.prediction, b.risk} {
		f.set(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/readyz" {
				jsonHandler(200, `{}`)(w, r)
				return
			}
			nodata(w, r)
		})
	}
	b.alert.set(jsonHandler(200, `{"alerts":[]}`))
	g := newGW(t, b.config())
	r, c := climate(g)
	if r.Code != 200 || c["status"] != "warming" || c["degraded"] != false {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	s := sources(c)
	if s["processing"] != "no_data" || s["prediction"] != "no_data" || s["risk"] != "no_data" || s["weather"] != "ok" {
		t.Errorf("sources = %v", s)
	}
	if a, ok := c["alerts"].([]any); !ok || len(a) != 0 {
		t.Errorf("alerts = %v, want an empty list", c["alerts"])
	}
	if c["location"] == nil {
		t.Error("the location is known to the registry and must be returned while warming up")
	}
}

func TestAPipelineThatIsHalfWayThroughIsPartialButNotDegraded(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(jsonHandler(404, `{"error":{"code":"no_data"}}`))
	g := newGW(t, b.config())
	_, c := climate(g)
	if c["status"] != "partial" || c["degraded"] != false || c["risk"] != nil || c["weather"] == nil {
		t.Errorf("status=%v degraded=%v risk=%v", c["status"], c["degraded"], c["risk"])
	}
}

func TestMalformedAnswersMarkTheSourceAsErrored(t *testing.T) {
	cases := map[string]struct {
		set    func(*backends)
		source string
	}{
		"prediction is not JSON":       {func(b *backends) { b.prediction.set(jsonHandler(200, `not json`)) }, "prediction"},
		"prediction lacks its payload": {func(b *backends) { b.prediction.set(jsonHandler(200, `{"locationId":7}`)) }, "prediction"},
		"risk lacks the assessment":    {func(b *backends) { b.risk.set(jsonHandler(200, `{"location":{}}`)) }, "risk"},
		"alerts is not an object":      {func(b *backends) { b.alert.set(jsonHandler(200, `[1,2]`)) }, "alerts"},
		"weather is not JSON":          {func(b *backends) { b.processing.set(jsonHandler(200, `{broken`)) }, "processing"},
		"unexpected status":            {func(b *backends) { b.prediction.set(jsonHandler(418, `{}`)) }, "prediction"},
	}
	for name, c := range cases {
		b := healthyBackends(t)
		c.set(b)
		g := newGW(t, b.config())
		r, body := climate(g)
		if r.Code != 200 || sources(body)[c.source] != "error" || body["degraded"] != true {
			t.Errorf("%s: %d sources=%v degraded=%v", name, r.Code, sources(body), body["degraded"])
		}
	}
}

func TestASlowServiceCostsOnlyItsOwnPart(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(hang)
	cfg := b.config()
	cfg.UpstreamTimeout = 200 * time.Millisecond
	g := newGW(t, cfg)

	started := time.Now()
	r, c := climate(g)
	elapsed := time.Since(started)
	if r.Code != 200 || sources(c)["risk"] != "timeout" || c["risk"] != nil || c["weather"] == nil || c["prediction"] == nil {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	if elapsed > 700*time.Millisecond {
		t.Errorf("took %v; a slow service must not hold the page hostage for longer than its timeout", elapsed)
	}
}

func TestTheWholeCompositionHasItsOwnDeadline(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(hang)
	b.alert.set(hang)
	cfg := b.config()
	cfg.UpstreamTimeout = 5 * time.Second
	cfg.ClimateTimeout = 250 * time.Millisecond
	g := newGW(t, cfg)
	started := time.Now()
	r, c := climate(g)
	if r.Code != 200 || time.Since(started) > time.Second || sources(c)["risk"] != "timeout" || sources(c)["alerts"] != "timeout" || c["weather"] == nil {
		t.Errorf("= %d after %v: %v", r.Code, time.Since(started), sources(c))
	}
}

func TestBackendsAreCalledInParallel(t *testing.T) {
	b := healthyBackends(t)
	slow := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			jsonHandler(200, body)(w, r)
		}
	}
	b.weather.set(slow(locJSON))
	b.processing.set(slow(wxJSON))
	b.prediction.set(slow(predJSON))
	b.risk.set(slow(riskJSON))
	b.alert.set(slow(altJSON))
	g := newGW(t, b.config())
	started := time.Now()
	if r, c := climate(g); r.Code != 200 || c["status"] != "ok" {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	if elapsed := time.Since(started); elapsed > 450*time.Millisecond {
		t.Errorf("took %v for five 150ms calls; they must run concurrently (~150ms, not ~750ms)", elapsed)
	}
}

func TestAnOpenCircuitDegradesOnlyThatPart(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(failing)
	g := newGW(t, b.config())
	for i := 0; i < 3; i++ {
		climate(g) // each call fails the risk service once; the third opens its breaker
	}
	before := b.risk.count()
	r, c := climate(g)
	if r.Code != 200 || sources(c)["risk"] != "unavailable" || c["weather"] == nil || b.risk.count() != before {
		t.Errorf("= %d sources=%v, risk calls %d -> %d (an open circuit must not call the backend)", r.Code, sources(c), before, b.risk.count())
	}
}

func TestClimateRequestIDReachesEveryBackend(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	g.get("/api/v1/locations/7/climate", "X-Request-ID", "trace-9")
	for name, f := range map[string]*fake{"weather": b.weather, "processing": b.processing, "prediction": b.prediction, "risk": b.risk, "alert": b.alert} {
		if got := f.last().header.Get("X-Request-ID"); got != "trace-9" {
			t.Errorf("%s saw request id %q", name, got)
		}
	}
}

func TestClimateIDValidation(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, id := range []string{"abc", "0", "-1", "1.5"} {
		if r := g.get("/api/v1/locations/" + id + "/climate"); r.Code != 400 || r.errCode() != "invalid_id" {
			t.Errorf("id %q = %d %s", id, r.Code, r.Body)
		}
	}
	if b.weather.count() != 0 {
		t.Error("an invalid id reached a backend")
	}
}

func TestAClientThatLeavesGetsNoResponseAndNoPanic(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(hang)
	cfg := b.config()
	cfg.UpstreamTimeout = 5 * time.Second
	cfg.ClimateTimeout = 5 * time.Second
	g := newGW(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/locations/7/climate", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { g.h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler kept working after the client left")
	}
	if g.g.risk.Breaker.State() != StateClosed {
		t.Error("a departed client counted against the risk service")
	}
}

func TestClimateCounters(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	climate(g)
	b.risk.Close()
	climate(g)
	text := g.get("/metrics").Body.String()
	if !strings.Contains(text, `gateway_climate_total{status="ok"} 1`) || !strings.Contains(text, `gateway_climate_total{status="partial"} 1`) {
		t.Errorf("metrics:\n%s", text)
	}
}
