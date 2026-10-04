package httpx

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Metrics keeps a minimal set of Prometheus-format counters (no client library:
// request counts and latency sums are all the services need).
type Metrics struct {
	service string
	started time.Time

	mu         sync.Mutex
	requests   map[reqKey]uint64
	durations  map[routeKey]float64
	collectors []func(io.Writer)
}

type routeKey struct{ method, route string }
type reqKey struct {
	routeKey
	code int
}

// NewMetrics returns an empty registry for the named service.
func NewMetrics(service string) *Metrics {
	return &Metrics{
		service:   service,
		started:   time.Now(),
		requests:  map[reqKey]uint64{},
		durations: map[routeKey]float64{},
	}
}

// AddCollector registers a function that appends extra metric lines at scrape time.
func (m *Metrics) AddCollector(fn func(io.Writer)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectors = append(m.collectors, fn)
}

func (m *Metrics) observe(method, route string, code int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rk := routeKey{method, route}
	m.requests[reqKey{rk, code}]++
	m.durations[rk] += d.Seconds()
}

// WriteText renders the registry in Prometheus text exposition format.
func (m *Metrics) WriteText(w io.Writer) {
	m.mu.Lock()
	reqs := make([]reqKey, 0, len(m.requests))
	for k := range m.requests {
		reqs = append(reqs, k)
	}
	sort.Slice(reqs, func(i, j int) bool {
		a, b := reqs[i], reqs[j]
		if a.route != b.route {
			return a.route < b.route
		}
		if a.method != b.method {
			return a.method < b.method
		}
		return a.code < b.code
	})
	counts := make(map[reqKey]uint64, len(reqs))
	for _, k := range reqs {
		counts[k] = m.requests[k]
	}
	durs := make(map[routeKey]float64, len(m.durations))
	for k, v := range m.durations {
		durs[k] = v
	}
	collectors := append([]func(io.Writer){}, m.collectors...)
	m.mu.Unlock()

	fmt.Fprintf(w, "# HELP service_up Whether the service is running.\n# TYPE service_up gauge\nservice_up{service=%q} 1\n", m.service)
	fmt.Fprintf(w, "# HELP process_start_time_seconds Start time of the process since the unix epoch.\n# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds{service=%q} %d\n", m.service, m.started.Unix())

	fmt.Fprintf(w, "# HELP http_requests_total HTTP requests handled.\n# TYPE http_requests_total counter\n")
	for _, k := range reqs {
		fmt.Fprintf(w, "http_requests_total{service=%q,method=%q,route=%q,code=\"%d\"} %d\n", m.service, k.method, k.route, k.code, counts[k])
	}

	fmt.Fprintf(w, "# HELP http_request_duration_seconds_sum Total time spent handling requests.\n# TYPE http_request_duration_seconds_sum counter\n")
	routes := make([]routeKey, 0, len(durs))
	for k := range durs {
		routes = append(routes, k)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].route != routes[j].route {
			return routes[i].route < routes[j].route
		}
		return routes[i].method < routes[j].method
	})
	for _, k := range routes {
		fmt.Fprintf(w, "http_request_duration_seconds_sum{service=%q,method=%q,route=%q} %g\n", m.service, k.method, k.route, durs[k])
	}

	for _, c := range collectors {
		c(w)
	}
}
