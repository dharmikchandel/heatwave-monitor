package gateway

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// Config holds everything the gateway needs.
type Config struct {
	// Base URLs of the backend services, without a trailing path.
	WeatherURL, ProcessingURL, PredictionURL, RiskURL, AlertURL, UserURL string

	UpstreamTimeout time.Duration // per call to one service; default 3s
	ClimateTimeout  time.Duration // for the whole composed climate response; default 5s
	ProbeTimeout    time.Duration // for each health probe behind /status; default 2s
	BreakerFailures int           // consecutive failures that open a breaker; default 5
	BreakerCooldown time.Duration // how long an open breaker refuses calls; default 10s

	RateLimit      RateConfig // all requests, per client
	WriteRateLimit RateConfig // POST/PUT/DELETE, per client, on top of RateLimit
	AuthRateLimit  RateConfig // sign-in, registration and password changes, per client, on top of both
	TrustProxy     bool       // take the client address from X-Forwarded-For and X-Forwarded-Proto
	CORSOrigins    []string

	SecureCookies bool          // always mark the session cookie Secure (otherwise only behind https)
	SessionCache  time.Duration // how long a resolved session is remembered; default 5s, negative disables

	Clock func() time.Time // for rate limiting and breakers; defaults to time.Now
}

const maxRequestBody = 1 << 20

// Gateway is the API gateway.
type Gateway struct {
	cfg Config
	log *slog.Logger

	weather, processing, prediction, risk, alert, user *Upstream

	cors     CORS
	limiter  *Limiter
	writes   *Limiter
	auths    *Limiter
	sessions *sessionCache

	rateLimited atomic.Int64
	authDenied  atomic.Int64
	climate     [3]atomic.Int64 // ok, partial, warming
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
	g.auths = NewLimiter(cfg.AuthRateLimit, cfg.Clock)
	switch {
	case cfg.SessionCache == 0:
		cfg.SessionCache = 5 * time.Second
	case cfg.SessionCache < 0:
		cfg.SessionCache = 0
	}
	g.cfg = cfg
	g.sessions = newSessionCache(cfg.SessionCache, cfg.Clock)

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
	g.user = up("user", cfg.UserURL)
	return g
}

// upstreams lists the backends in pipeline order.
func (g *Gateway) upstreams() []*Upstream {
	return []*Upstream{g.weather, g.processing, g.prediction, g.risk, g.alert, g.user}
}

// routeOpts says how a route is protected.
type routeOpts struct {
	auth   authLevel
	strict bool // sign-in style route: also counts against the stricter auth rate limit
}

// route registers a handler behind the standard chain:
// CORS headers -> security headers -> rate limits -> authentication -> handler.
func (g *Gateway) route(mux *http.ServeMux, pattern string, opts routeOpts, h http.HandlerFunc) {
	next := g.authenticate(opts.auth, h)
	next = g.rateLimit(opts.strict, next)
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

// rateLimit applies the general limit to every request, the stricter write limit to
// anything that changes state, and the strictest to sign-in style routes.
func (g *Gateway) rateLimit(strict bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ClientKey(r, g.cfg.TrustProxy)
		limiters := []*Limiter{g.limiter}
		if !safeMethod(r.Method) {
			limiters = append(limiters, g.writes)
		}
		if strict {
			limiters = append(limiters, g.auths)
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

// Register adds the gateway's routes to mux.
func (g *Gateway) Register(mux *http.ServeMux) {
	public, user, admin := routeOpts{}, routeOpts{auth: authUser}, routeOpts{auth: authAdmin}
	signIn := routeOpts{strict: true}
	changePassword := routeOpts{auth: authUser, strict: true}

	// Public: reads of shared data.
	g.route(mux, "GET /api/v1/locations", public, g.pass(g.weather, "/locations"))
	g.route(mux, "GET /api/v1/locations/{id}", public, g.passID(g.weather, "/locations/%d"))
	g.route(mux, "GET /api/v1/locations/{id}/climate", public, g.climateHandler)
	g.route(mux, "GET /api/v1/alerts", public, g.pass(g.alert, "/alerts"))
	g.route(mux, "GET /api/v1/alerts/{id}", public, g.passID(g.alert, "/alerts/%d"))
	g.route(mux, "GET /api/v1/model", public, g.pass(g.prediction, "/model"))
	g.route(mux, "GET /api/v1/status", public, g.statusHandler)

	// Accounts and sessions.
	g.route(mux, "POST /api/v1/auth/register", signIn, g.newSession("/register"))
	g.route(mux, "POST /api/v1/auth/login", signIn, g.newSession("/login"))
	g.route(mux, "POST /api/v1/auth/logout", public, g.logout)
	g.route(mux, "GET /api/v1/auth/session", public, g.session)
	g.route(mux, "POST /api/v1/auth/password", changePassword, g.newSession("/me/password"))
	g.route(mux, "DELETE /api/v1/auth/account", changePassword, g.deleteAccount)

	// Signed-in users: adding a city (the core feature of the search box), their watchlist,
	// the cities they want alerts for, and their inbox.
	g.route(mux, "POST /api/v1/locations", user, g.pass(g.weather, "/locations"))
	g.route(mux, "GET /api/v1/me/watchlist", user, g.pass(g.user, "/watchlist"))
	g.route(mux, "PUT /api/v1/me/watchlist/{id}", user, g.passID(g.user, "/watchlist/%d"))
	g.route(mux, "DELETE /api/v1/me/watchlist/{id}", user, g.passID(g.user, "/watchlist/%d"))
	g.route(mux, "GET /api/v1/me/subscriptions", user, g.pass(g.alert, "/my/subscriptions"))
	g.route(mux, "POST /api/v1/me/subscriptions", user, g.pass(g.alert, "/my/subscriptions"))
	g.route(mux, "DELETE /api/v1/me/subscriptions/{id}", user, g.passID(g.alert, "/my/subscriptions/%d"))
	g.route(mux, "GET /api/v1/me/notifications", user, g.pass(g.alert, "/my/notifications"))
	g.route(mux, "POST /api/v1/me/notifications/read", user, g.pass(g.alert, "/my/notifications/read"))
	g.route(mux, "POST /api/v1/me/notifications/{id}/read", user, g.passID(g.alert, "/my/notifications/%d/read"))

	// Administrators: anything that changes shared state, exposes operational detail or
	// manages accounts.
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
	g.route(mux, "GET /api/v1/admin/users", admin, g.pass(g.user, "/admin/users"))
	g.route(mux, "POST /api/v1/admin/users/{id}/disable", admin, g.setDisabled("disable"))
	g.route(mux, "POST /api/v1/admin/users/{id}/enable", admin, g.setDisabled("enable"))
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

// readBody reads a request's body, within the size limit. On failure it has already answered.
func (g *Gateway) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil || safeMethod(r.Method) {
		return nil, true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body must be at most %d bytes", maxRequestBody))
		} else {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_request", "could not read the request body")
		}
		return nil, false
	}
	return body, true
}

// relay writes an upstream's answer to the client as it came.
func (g *Gateway) relay(w http.ResponseWriter, res Result) {
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if ra := res.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(res.Status)
	w.Write(res.Body)
}

// forward sends the request to the upstream and relays the answer verbatim. It returns
// the status it answered with (0 when the call failed).
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, up *Upstream, path string) int {
	body, ok := g.readBody(w, r)
	if !ok {
		return 0
	}
	res, err := up.Do(r.Context(), r.Method, path, r.URL.RawQuery, body, g.outbound(r))
	if err != nil {
		g.writeCallError(w, r, err)
		return 0
	}
	g.relay(w, res)
	return res.Status
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
