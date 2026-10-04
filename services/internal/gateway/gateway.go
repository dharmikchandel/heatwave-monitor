package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
)

// Config holds everything the gateway needs.
type Config struct {
	// Base URLs of the backend services, without a trailing path.
	WeatherURL, ProcessingURL, PredictionURL, RiskURL, AlertURL string

	UpstreamTimeout time.Duration // per call to one service; default 3s
	ClimateTimeout  time.Duration // for the whole composed climate response; default 5s
	ProbeTimeout    time.Duration // for each health probe behind /status; default 2s
	BreakerFailures int           // consecutive failures that open a breaker; default 5
	BreakerCooldown time.Duration // how long an open breaker refuses calls; default 10s

	RateLimit      RateConfig // all requests, per client
	WriteRateLimit RateConfig // POST/PUT/DELETE, per client, on top of RateLimit
	TrustProxy     bool       // take the client address from X-Forwarded-For
	CORSOrigins    []string
	AdminToken     string // bearer token for admin routes; empty disables them

	Clock func() time.Time // for rate limiting and breakers; defaults to time.Now
}

const maxRequestBody = 1 << 20

// Gateway is the API gateway.
type Gateway struct {
	cfg Config
	log *slog.Logger

	weather, processing, prediction, risk, alert *Upstream

	cors    CORS
	limiter *Limiter
	writes  *Limiter

	rateLimited atomic.Int64
	adminDenied atomic.Int64
	climate     [3]atomic.Int64 // ok, partial, warming
	tokenHash   [32]byte
}

// New builds a gateway. Zero-valued settings take their defaults.
func New(cfg Config, log *slog.Logger) *Gateway {
	if cfg.UpstreamTimeout <= 0 {
		cfg.UpstreamTimeout = 3 * time.Second
	}
	if cfg.ClimateTimeout <= 0 {
		cfg.ClimateTimeout = 5 * time.Second
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	g := &Gateway{cfg: cfg, log: log, cors: CORS{Origins: cfg.CORSOrigins}}
	g.limiter = NewLimiter(cfg.RateLimit, cfg.Clock)
	g.writes = NewLimiter(cfg.WriteRateLimit, cfg.Clock)
	if cfg.AdminToken != "" {
		g.tokenHash = sha256.Sum256([]byte(cfg.AdminToken))
	}

	up := func(name, base string) *Upstream {
		return &Upstream{
			Name:    name,
			BaseURL: strings.TrimRight(base, "/"),
			Client:  newClient(cfg.UpstreamTimeout),
			Breaker: &Breaker{Threshold: cfg.BreakerFailures, Cooldown: cfg.BreakerCooldown, Now: cfg.Clock},
			Timeout: cfg.UpstreamTimeout,
		}
	}
	g.weather = up("weather", cfg.WeatherURL)
	g.processing = up("processing", cfg.ProcessingURL)
	g.prediction = up("prediction", cfg.PredictionURL)
	g.risk = up("risk", cfg.RiskURL)
	g.alert = up("alert", cfg.AlertURL)
	return g
}

// upstreams lists the backends in pipeline order.
func (g *Gateway) upstreams() []*Upstream {
	return []*Upstream{g.weather, g.processing, g.prediction, g.risk, g.alert}
}

// routeOpts says how a route is protected.
type routeOpts struct {
	admin bool // requires the admin token
}

// route registers a handler behind the standard chain:
// CORS headers -> security headers -> rate limits -> (admin gate) -> handler.
func (g *Gateway) route(mux *http.ServeMux, pattern string, opts routeOpts, h http.HandlerFunc) {
	var next http.Handler = h
	if opts.admin {
		next = g.adminGate(next)
	}
	next = g.rateLimit(next)
	next = secureHeaders(next)
	mux.Handle(pattern, g.cors.middleware(next))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// rateLimit applies the general limit to every request and the stricter write
// limit to anything that changes state.
func (g *Gateway) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ClientKey(r, g.cfg.TrustProxy)
		limiters := []*Limiter{g.limiter}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			limiters = append(limiters, g.writes)
		}
		for _, l := range limiters {
			if ok, wait, _ := l.Allow(key); !ok {
				g.rateLimited.Add(1)
				secs := int(wait.Seconds()) + 1
				w.Header().Set("Retry-After", fmt.Sprint(secs))
				httpx.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("too many requests; retry in %d seconds", secs))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// adminGate requires "Authorization: Bearer <ADMIN_TOKEN>". With no token
// configured the admin routes are closed to everyone, so a forgotten setting can
// never leave them open.
func (g *Gateway) adminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.cfg.AdminToken == "" {
			g.adminDenied.Add(1)
			httpx.WriteError(w, r, http.StatusForbidden, "admin_disabled", "admin endpoints are disabled: no ADMIN_TOKEN is configured")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got)) // compare fixed-size digests: constant time and no length leak
		if !ok || subtle.ConstantTimeCompare(sum[:], g.tokenHash[:]) != 1 {
			g.adminDenied.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="heatwave-monitor admin"`)
			httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "a valid admin token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Register adds the gateway's routes to mux.
func (g *Gateway) Register(mux *http.ServeMux) {
	public, admin := routeOpts{}, routeOpts{admin: true}

	// Public: reads, plus adding a city (the core feature of the search box).
	g.route(mux, "GET /api/v1/locations", public, g.pass(g.weather, "/locations"))
	g.route(mux, "POST /api/v1/locations", public, g.pass(g.weather, "/locations"))
	g.route(mux, "GET /api/v1/locations/{id}", public, g.passID(g.weather, "/locations/%d"))
	g.route(mux, "GET /api/v1/locations/{id}/climate", public, g.climateHandler)
	g.route(mux, "GET /api/v1/alerts", public, g.pass(g.alert, "/alerts"))
	g.route(mux, "GET /api/v1/alerts/{id}", public, g.passID(g.alert, "/alerts/%d"))
	g.route(mux, "GET /api/v1/model", public, g.pass(g.prediction, "/model"))
	g.route(mux, "GET /api/v1/status", public, g.statusHandler)

	// Admin: anything that changes shared state, or exposes operational detail.
	g.route(mux, "DELETE /api/v1/locations/{id}", admin, g.passID(g.weather, "/locations/%d"))
	g.route(mux, "POST /api/v1/locations/{id}/refresh", admin, g.passID(g.weather, "/locations/%d/refresh"))
	g.route(mux, "GET /api/v1/simulation", admin, g.pass(g.weather, "/simulation"))
	g.route(mux, "PUT /api/v1/simulation", admin, g.pass(g.weather, "/simulation"))
	g.route(mux, "POST /api/v1/alerts/{id}/ack", admin, g.passID(g.alert, "/alerts/%d/ack"))
	g.route(mux, "GET /api/v1/subscriptions", admin, g.pass(g.alert, "/subscriptions"))
	g.route(mux, "POST /api/v1/subscriptions", admin, g.pass(g.alert, "/subscriptions"))
	g.route(mux, "DELETE /api/v1/subscriptions/{id}", admin, g.passID(g.alert, "/subscriptions/%d"))
	g.route(mux, "GET /api/v1/notifications", admin, g.pass(g.alert, "/notifications"))
	g.route(mux, "GET /api/v1/rejections/{service}", admin, g.rejections)
}

// pass proxies to a fixed upstream path.
func (g *Gateway) pass(up *Upstream, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { g.forward(w, r, up, path) }
}

// passID proxies to an upstream path containing the validated numeric {id}.
// Validating first means an id can never smuggle extra path segments upstream.
func (g *Gateway) passID(up *Upstream, format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpx.PathID(w, r, "id")
		if !ok {
			return
		}
		g.forward(w, r, up, fmt.Sprintf(format, id))
	}
}

func (g *Gateway) rejections(w http.ResponseWriter, r *http.Request) {
	ups := map[string]*Upstream{"processing": g.processing, "prediction": g.prediction, "risk": g.risk, "alert": g.alert}
	up, ok := ups[r.PathValue("service")]
	if !ok {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "unknown service; use processing, prediction, risk or alert")
		return
	}
	g.forward(w, r, up, "/rejections")
}

// forward sends the request to the upstream and relays the answer verbatim.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, up *Upstream, path string) {
	var body []byte
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body must be at most %d bytes", maxRequestBody))
			} else {
				httpx.WriteError(w, r, http.StatusBadRequest, "bad_request", "could not read the request body")
			}
			return
		}
	}

	res, err := up.Do(r.Context(), r.Method, path, r.URL.RawQuery, body, r.Header)
	if err != nil {
		g.writeCallError(w, r, err)
		return
	}
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(res.Status)
	w.Write(res.Body)
}

// writeCallError turns a failed upstream call into a uniform error response that
// says which service is the problem without revealing internal addresses.
func (g *Gateway) writeCallError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *CallError
	if !errors.As(err, &ce) {
		httpx.WriteError(w, r, http.StatusBadGateway, "upstream_unavailable", "a backend service failed")
		return
	}
	switch ce.Kind {
	case KindCanceled:
		return // the client has gone; nothing to answer
	case KindTimeout:
		httpx.WriteError(w, r, http.StatusGatewayTimeout, "upstream_timeout", fmt.Sprintf("the %s service did not answer in time", ce.Upstream))
	case KindCircuitOpen:
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", fmt.Sprintf("the %s service is temporarily unavailable", ce.Upstream))
	default:
		httpx.WriteError(w, r, http.StatusBadGateway, "upstream_unavailable", fmt.Sprintf("the %s service is unavailable", ce.Upstream))
	}
	g.log.Warn("upstream call failed", "request_id", httpx.RequestID(r.Context()), "upstream", ce.Upstream, "kind", ce.Kind, "err", ce.Err)
}

// Wrap answers CORS preflight requests and turns the router's plain-text 404 and
// 405 responses into the uniform JSON error. (Preflight is handled here rather
// than as a route: a registered OPTIONS pattern would make the router answer 405
// instead of 404 for every unknown path under it.)
func (g *Gateway) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			g.cors.preflight(w, r)
			return
		}
		next.ServeHTTP(&jsonErrorWriter{ResponseWriter: w, r: r}, r)
	})
}

type jsonErrorWriter struct {
	http.ResponseWriter
	r        *http.Request
	replaced bool
}

func (j *jsonErrorWriter) WriteHeader(code int) {
	isPlain := strings.HasPrefix(j.Header().Get("Content-Type"), "text/plain")
	if (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) && isPlain {
		j.replaced = true
		j.Header().Del("Content-Length")
		errCode, msg := "not_found", "no such endpoint"
		if code == http.StatusMethodNotAllowed {
			errCode, msg = "method_not_allowed", "this endpoint does not support that method"
		}
		httpx.WriteError(j.ResponseWriter, j.r, code, errCode, msg)
		return
	}
	j.ResponseWriter.WriteHeader(code)
}

func (j *jsonErrorWriter) Write(b []byte) (int, error) {
	if j.replaced {
		return len(b), nil // swallow the router's plain-text body
	}
	return j.ResponseWriter.Write(b)
}

func (j *jsonErrorWriter) Unwrap() http.ResponseWriter { return j.ResponseWriter }
