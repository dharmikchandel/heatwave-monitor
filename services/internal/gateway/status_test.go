package gateway

import (
	"net/http"
	"testing"
	"time"
)

func statusOf(g *gw) (resp, map[string]any, map[string]map[string]any) {
	g.t.Helper()
	r := g.get("/api/v1/status")
	body := r.json()
	svcs := map[string]map[string]any{}
	if list, ok := body["services"].([]any); ok {
		for _, s := range list {
			m := s.(map[string]any)
			svcs[m["name"].(string)] = m
		}
	}
	return r, body, svcs
}

func TestStatusAllHealthy(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r, body, svcs := statusOf(g)
	if r.Code != 200 || body["status"] != "ok" || len(svcs) != 5 {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	order := []string{}
	for _, s := range body["services"].([]any) {
		order = append(order, s.(map[string]any)["name"].(string))
	}
	if !eq(order, []string{"weather", "processing", "prediction", "risk", "alert"}) {
		t.Errorf("services listed as %v, want pipeline order", order)
	}
	for name, s := range svcs {
		if s["status"] != "ok" || s["ready"] != true || s["circuit"] != "closed" || s["latencyMs"].(float64) <= 0 {
			t.Errorf("%s = %v", name, s)
		}
		if s["checks"].(map[string]any)["database"] != "ok" {
			t.Errorf("%s checks = %v", name, s["checks"])
		}
	}
	if body["checkedAt"] == nil {
		t.Error("no checkedAt")
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStatusDistinguishesNotReadyFromDown(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(jsonHandler(503, `{"status":"unavailable","checks":{"database":"disk I/O error"}}`))
	b.alert.Close()
	g := newGW(t, b.config())
	r, body, svcs := statusOf(g)
	if r.Code != 200 || body["status"] != "degraded" {
		t.Fatalf("= %d %s", r.Code, r.Body)
	}
	if s := svcs["risk"]; s["status"] != "unavailable" || s["ready"] != false || s["checks"].(map[string]any)["database"] != "disk I/O error" {
		t.Errorf("risk = %v (reachable but not ready, with the reason)", s)
	}
	if s := svcs["alert"]; s["status"] != "down" || s["ready"] != false || s["error"] == nil {
		t.Errorf("alert = %v", s)
	}
	if svcs["weather"]["status"] != "ok" {
		t.Errorf("weather = %v", svcs["weather"])
	}
	if e, _ := svcs["alert"]["error"].(string); e != "not reachable" {
		t.Errorf("error = %q; it must not reveal internal addresses", e)
	}
}

func TestStatusAllDown(t *testing.T) {
	b := healthyBackends(t)
	for _, f := range []*fake{b.weather, b.processing, b.prediction, b.risk, b.alert} {
		f.Close()
	}
	g := newGW(t, b.config())
	if r, body, _ := statusOf(g); r.Code != 200 || body["status"] != "down" {
		t.Errorf("= %d %s", r.Code, r.Body)
	}
}

func TestStatusReportsTheTruthWhileACircuitIsOpen(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for i := 0; i < 3; i++ { // trip the weather breaker
		b.weather.set(failing)
		g.get("/api/v1/locations")
	}
	b.weather.set(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(200, `{"status":"ready","checks":{"database":"ok"}}`)(w, r)
	})
	_, _, svcs := statusOf(g)
	if s := svcs["weather"]; s["circuit"] != "open" || s["status"] != "ok" || s["ready"] != true {
		t.Errorf("weather = %v; the probe bypasses the breaker, so it shows the service is back while the breaker has not yet noticed", s)
	}
}

func TestSlowServicesAreProbedInParallelWithATimeout(t *testing.T) {
	b := healthyBackends(t)
	b.risk.set(hang)
	b.prediction.set(hang)
	cfg := b.config()
	cfg.ProbeTimeout = 200 * time.Millisecond
	g := newGW(t, cfg)
	started := time.Now()
	_, body, svcs := statusOf(g)
	if elapsed := time.Since(started); elapsed > 600*time.Millisecond {
		t.Errorf("took %v; probes must run concurrently and honour the timeout", elapsed)
	}
	if body["status"] != "degraded" || svcs["risk"]["status"] != "down" || svcs["prediction"]["status"] != "down" || svcs["weather"]["status"] != "ok" {
		t.Errorf("status = %v", body)
	}
}
