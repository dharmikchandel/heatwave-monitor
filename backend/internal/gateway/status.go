package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// ServiceStatus is one backend's health as seen by the gateway.
type ServiceStatus struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"` // "ok", "unavailable" (reachable but not ready) or "down"
	Ready     bool              `json:"ready"`
	LatencyMs float64           `json:"latencyMs"`
	Checks    map[string]string `json:"checks,omitempty"`
	Error     string            `json:"error,omitempty"`
	Circuit   string            `json:"circuit"` // the gateway's breaker for this service
}

// SystemStatus is the whole system's health.
type SystemStatus struct {
	// Status is "ok" (every service ready), "degraded" (some not) or "down" (none reachable).
	Status    string          `json:"status"`
	Services  []ServiceStatus `json:"services"`
	CheckedAt time.Time       `json:"checkedAt"`
}

// statusHandler probes every backend's /readyz in parallel. It talks to the
// services directly, bypassing the breakers, so it reports the truth even while
// a breaker is open.
func (g *Gateway) statusHandler(w http.ResponseWriter, r *http.Request) {
	ups := g.upstreams()
	out := make([]ServiceStatus, len(ups))

	var wg sync.WaitGroup
	for i, up := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = g.probe(r.Context(), up)
		}()
	}
	wg.Wait()

	ready := 0
	for _, s := range out {
		if s.Ready {
			ready++
		}
	}
	status := "degraded"
	switch {
	case ready == len(out):
		status = "ok"
	case ready == 0:
		status = "down"
	}
	httpx.WriteJSON(w, http.StatusOK, SystemStatus{Status: status, Services: out, CheckedAt: time.Now().UTC()})
}

func (g *Gateway) probe(ctx context.Context, up *Upstream) ServiceStatus {
	s := ServiceStatus{Name: up.Name, Circuit: up.Breaker.State()}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.ProbeTimeout)
	defer cancel()

	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.BaseURL+"/readyz", nil)
	if err != nil {
		s.Status, s.Error = "down", "invalid service address"
		return s
	}
	resp, err := up.Client.Do(req)
	s.LatencyMs = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		s.Status, s.Error = "down", "not reachable"
		return s
	}
	defer resp.Body.Close()

	var body struct {
		Checks map[string]string `json:"checks"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	json.Unmarshal(data, &body)
	s.Checks = body.Checks
	s.Ready = resp.StatusCode == http.StatusOK
	if s.Ready {
		s.Status = "ok"
	} else {
		s.Status = "unavailable"
	}
	return s
}

// Collector writes the gateway's metrics.
func (g *Gateway) Collector(w io.Writer) {
	io.WriteString(w, "# HELP gateway_upstream_requests_total Calls to backend services by outcome.\n# TYPE gateway_upstream_requests_total counter\n")
	for _, up := range g.upstreams() {
		ok, failed, rejected := up.Counts()
		writef(w, "gateway_upstream_requests_total{upstream=%q,result=\"ok\"} %d\n", up.Name, ok)
		writef(w, "gateway_upstream_requests_total{upstream=%q,result=\"error\"} %d\n", up.Name, failed)
		writef(w, "gateway_upstream_requests_total{upstream=%q,result=\"circuit_open\"} %d\n", up.Name, rejected)
	}
	io.WriteString(w, "# HELP gateway_circuit_open Whether the breaker for a backend is open (1) or not (0).\n# TYPE gateway_circuit_open gauge\n")
	for _, up := range g.upstreams() {
		open := 0
		if up.Breaker.State() != StateClosed {
			open = 1
		}
		writef(w, "gateway_circuit_open{upstream=%q} %d\n", up.Name, open)
	}
	io.WriteString(w, "# HELP gateway_rate_limited_total Requests refused by rate limiting.\n# TYPE gateway_rate_limited_total counter\n")
	writef(w, "gateway_rate_limited_total %d\n", g.rateLimited.Load())
	io.WriteString(w, "# HELP gateway_auth_denied_total Requests refused for missing, invalid or insufficient credentials.\n# TYPE gateway_auth_denied_total counter\n")
	writef(w, "gateway_auth_denied_total %d\n", g.authDenied.Load())
	io.WriteString(w, "# HELP gateway_climate_total Composed climate responses by status.\n# TYPE gateway_climate_total counter\n")
	for i, name := range []string{"ok", "partial", "warming"} {
		writef(w, "gateway_climate_total{status=%q} %d\n", name, g.climate[i].Load())
	}
}

func writef(w io.Writer, format string, args ...any) { fmt.Fprintf(w, format, args...) }
