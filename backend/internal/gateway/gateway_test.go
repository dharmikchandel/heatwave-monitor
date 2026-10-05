package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicReadsAreProxiedVerbatim(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(jsonHandler(200, `{"locations":[`+locJSON+`]}`))
	g := newGW(t, b.config())

	r := g.get("/api/v1/locations?limit=3")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"name":"Mumbai"`) || r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET /locations = %d %s", r.Code, r.Body)
	}
	if c := b.weather.last(); c.path != "/locations" || c.query != "limit=3" || c.method != "GET" {
		t.Errorf("upstream saw %+v", c)
	}

	b.weather.set(jsonHandler(200, locJSON))
	if r := g.get("/api/v1/locations/7"); r.Code != 200 || b.weather.last().path != "/locations/7" {
		t.Errorf("GET /locations/7 = %d, upstream path %s", r.Code, b.weather.last().path)
	}
	b.alert.set(jsonHandler(200, altJSON))
	if r := g.get("/api/v1/alerts?status=open"); r.Code != 200 || b.alert.last().path != "/alerts" || b.alert.last().query != "status=open" {
		t.Errorf("GET /alerts = %d %+v", r.Code, b.alert.last())
	}
	if r := g.get("/api/v1/alerts/3"); r.Code != 200 || b.alert.last().path != "/alerts/3" {
		t.Errorf("GET /alerts/3 = %d %s", r.Code, b.alert.last().path)
	}
	b.prediction.set(jsonHandler(200, `{"loaded":true,"version":"lr-x"}`))
	if r := g.get("/api/v1/model"); r.Code != 200 || b.prediction.last().path != "/model" {
		t.Errorf("GET /model = %d", r.Code)
	}
}

func TestUpstreamErrorsPassThroughUnchanged(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, status := range []int{400, 404, 409, 422} {
		b.weather.set(jsonHandler(status, `{"error":{"code":"x","message":"from the service"}}`))
		r := g.get("/api/v1/locations/7")
		if r.Code != status || !strings.Contains(r.Body.String(), "from the service") {
			t.Errorf("upstream %d came back as %d %s", status, r.Code, r.Body)
		}
	}
}

func TestPostBodyAndContentTypeAreForwarded(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(jsonHandler(201, locJSON))
	g := newGW(t, b.config())

	body := `{"name":"Nagpur","latitude":21.1,"longitude":79.1}`
	r := g.do("POST", "/api/v1/locations", body, "Content-Type", "application/json", "Authorization", "Bearer "+userToken)
	if r.Code != 201 {
		t.Fatalf("POST = %d %s", r.Code, r.Body)
	}
	c := b.weather.last()
	if c.method != "POST" || c.body != body || c.header.Get("Content-Type") != "application/json" {
		t.Errorf("upstream saw %+v", c)
	}
}

func TestClientHeadersNeverReachBackends(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	spoof := []string{
		"Cookie", sessionCookie + "=abc; other=1", "Authorization", "Bearer something", "X-Custom", "x",
		"X-User-ID", "1", "X-User-Role", "admin", "X-Client-IP", "6.6.6.6",
	}
	for _, path := range []string{"/api/v1/locations", "/api/v1/locations/7/climate", "/api/v1/alerts", "/api/v1/model"} {
		g.get(path, spoof...)
	}
	g.do("POST", "/api/v1/auth/login", `{}`, spoof...)
	g.do("POST", "/api/v1/auth/logout", ``, spoof...)
	g.get("/api/v1/auth/session", spoof...)

	// And on authenticated routes: the identity is the gateway's, never the client's.
	g.do("GET", "/api/v1/subscriptions", "", "Authorization", "Bearer "+adminToken, "X-User-ID", "999", "X-User-Role", "user", "X-Client-IP", "6.6.6.6", "Cookie", "a=b", "X-Custom", "x")
	g.do("GET", "/api/v1/me/watchlist", "", "Authorization", "Bearer "+userToken, "X-User-ID", "1", "X-User-Role", "admin")

	seen := 0
	for name, f := range map[string]*fake{"weather": b.weather, "processing": b.processing, "prediction": b.prediction, "risk": b.risk, "alert": b.alert, "user": b.user} {
		for _, c := range f.calls {
			seen++
			if c.header.Get("Cookie") != "" || c.header.Get("X-Custom") != "" {
				t.Errorf("%s: a client header leaked upstream: %v", name, c.header)
			}
			if c.path == "/sessions/resolve" || c.path == "/readyz" {
				continue
			}
			if ip := c.header.Get("X-Client-IP"); ip != "" && ip != "198.51.100.1" {
				t.Errorf("%s %s: X-Client-IP = %q, want the real client address, not the one the client claimed", name, c.path, ip)
			}
			// Only the user service is ever given a credential, and only to end that very session.
			if got := c.header.Get("Authorization"); got != "" && !(name == "user" && c.path == "/logout" && got == "Bearer something") {
				t.Errorf("%s %s: Authorization %q leaked upstream", name, c.path, got)
			}
		}
	}
	if seen == 0 {
		t.Fatal("nothing reached the backends")
	}

	for _, c := range b.alert.calls {
		if c.path == "/subscriptions" && (c.header.Get("X-User-ID") != "1" || c.header.Get("X-User-Role") != "admin") {
			t.Errorf("admin route: identity headers = %q/%q, want 1/admin", c.header.Get("X-User-ID"), c.header.Get("X-User-Role"))
		}
	}
	for _, c := range b.user.calls {
		if c.path == "/watchlist" && (c.header.Get("X-User-ID") != "2" || c.header.Get("X-User-Role") != "user") {
			t.Errorf("user route: identity headers = %q/%q, want 2/user (a client cannot choose them)", c.header.Get("X-User-ID"), c.header.Get("X-User-Role"))
		}
	}
	// Public routes carry no identity at all, whatever the client claimed.
	for _, f := range []*fake{b.weather, b.processing, b.prediction, b.risk} {
		for _, c := range f.calls {
			if c.header.Get("X-User-ID") != "" || c.header.Get("X-User-Role") != "" {
				t.Errorf("%s: identity headers present on a public route: %v", c.path, c.header)
			}
		}
	}
}

func TestRequestIDsAreForwardedAndReturned(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r := g.get("/api/v1/locations", "X-Request-ID", "trace-42")
	if r.Header().Get("X-Request-ID") != "trace-42" || b.weather.last().header.Get("X-Request-ID") != "trace-42" {
		t.Errorf("response id %q, upstream id %q", r.Header().Get("X-Request-ID"), b.weather.last().header.Get("X-Request-ID"))
	}
	r = g.get("/api/v1/locations")
	if id := r.Header().Get("X-Request-ID"); id == "" || b.weather.last().header.Get("X-Request-ID") != id {
		t.Errorf("a generated id must reach the backend too: %q vs %q", id, b.weather.last().header.Get("X-Request-ID"))
	}
}

func TestPathIDsCannotSmuggleUpstreamPaths(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, id := range []string{"abc", "0", "-1", "1.5", "7%2F..%2Fsubscriptions", "..", "7%3Fadmin=1"} {
		for _, path := range []string{"/api/v1/locations/" + id, "/api/v1/alerts/" + id, "/api/v1/locations/" + id + "/climate"} {
			// 307: the router normalises ".." itself, redirecting without ever calling a backend.
			if r := g.get(path); r.Code != 400 && r.Code != 404 && r.Code != 307 {
				t.Errorf("GET %s = %d, want 400/404/307", path, r.Code)
			}
		}
	}
	if n := b.weather.count() + b.alert.count() + b.processing.count(); n != 0 {
		t.Errorf("%d upstream calls were made for malformed ids", n)
	}
}

func TestOversizedRequestBodiesAreRefused(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r := g.user("POST", "/api/v1/locations", strings.Repeat("x", maxRequestBody+10))
	if r.Code != http.StatusRequestEntityTooLarge || r.errCode() != "body_too_large" {
		t.Errorf("= %d %s", r.Code, r.Body)
	}
	if b.weather.count() != 0 {
		t.Error("an oversized body reached the backend")
	}
}

func TestUnknownRoutesAndWrongMethodsAnswerWithJSON(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	r := g.get("/api/v1/nope")
	if r.Code != 404 || r.errCode() != "not_found" || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") || r.json()["request_id"] == nil {
		t.Errorf("404 = %d %s (%s)", r.Code, r.Body, r.Header().Get("Content-Type"))
	}
	r = g.do("PATCH", "/api/v1/locations", "")
	if r.Code != 405 || r.errCode() != "method_not_allowed" || r.Header().Get("Allow") == "" {
		t.Errorf("405 = %d %s allow=%q", r.Code, r.Body, r.Header().Get("Allow"))
	}
	if r := g.get("/"); r.Code != 404 || r.errCode() != "not_found" {
		t.Errorf("root = %d %s", r.Code, r.Body)
	}
	if r := g.get("/healthz"); r.Code != 200 {
		t.Errorf("ops endpoints must keep working: %d", r.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	r := g.get("/api/v1/locations")
	if r.Header().Get("X-Content-Type-Options") != "nosniff" || r.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", r.Header())
	}
}

// ---- access control ----

var adminRoutes = []struct{ method, path string }{
	{"DELETE", "/api/v1/locations/7"},
	{"POST", "/api/v1/locations/7/refresh"},
	{"GET", "/api/v1/simulation"},
	{"PUT", "/api/v1/simulation"},
	{"POST", "/api/v1/alerts/3/ack"},
	{"GET", "/api/v1/subscriptions"},
	{"POST", "/api/v1/subscriptions"},
	{"DELETE", "/api/v1/subscriptions/2"},
	{"GET", "/api/v1/notifications"},
	{"GET", "/api/v1/rejections/risk"},
	{"GET", "/api/v1/admin/users"},
	{"POST", "/api/v1/admin/users/2/disable"},
	{"POST", "/api/v1/admin/users/2/enable"},
}

var userRoutes = []struct{ method, path string }{
	{"POST", "/api/v1/locations"},
	{"GET", "/api/v1/me/watchlist"},
	{"PUT", "/api/v1/me/watchlist/7"},
	{"DELETE", "/api/v1/me/watchlist/7"},
	{"GET", "/api/v1/me/subscriptions"},
	{"POST", "/api/v1/me/subscriptions"},
	{"DELETE", "/api/v1/me/subscriptions/3"},
	{"GET", "/api/v1/me/notifications"},
	{"POST", "/api/v1/me/notifications/read"},
	{"POST", "/api/v1/me/notifications/5/read"},
	{"POST", "/api/v1/auth/password"},
	{"DELETE", "/api/v1/auth/account"},
}

// nonUserCalls counts calls that reached the services behind the gateway, other than
// the user service's session lookups and health checks.
func (b *backends) nonUserCalls() int {
	n := 0
	for _, f := range []*fake{b.weather, b.processing, b.prediction, b.risk, b.alert} {
		n += f.count()
	}
	for _, c := range b.user.calls {
		if c.path != "/sessions/resolve" {
			n++
		}
	}
	return n
}

func TestProtectedRoutesNeedASession(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())

	for _, rt := range append(adminRoutes, userRoutes...) {
		if r := g.do(rt.method, rt.path, "{}"); r.Code != 401 || r.errCode() != "unauthorized" || r.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s with no credentials = %d %s", rt.method, rt.path, r.Code, r.Body)
		}
		for _, bad := range []string{"Bearer wrong", "Bearer ", "Bearer", "Basic " + userToken, userToken, "bearer " + userToken, "Bearer " + userToken + "X"} {
			if r := g.do(rt.method, rt.path, "{}", "Authorization", bad); r.Code != 401 {
				t.Errorf("%s %s with %q = %d, want 401", rt.method, rt.path, bad, r.Code)
			}
		}
		if r := g.do(rt.method, rt.path, "{}", "Cookie", sessionCookie+"=wrong"); r.Code != 401 {
			t.Errorf("%s %s with a bad cookie = %d, want 401", rt.method, rt.path, r.Code)
		}
	}
	if n := b.nonUserCalls(); n != 0 {
		t.Errorf("%d requests without a valid session reached a backend", n)
	}
}

func TestOrdinaryUsersCannotUseAdminRoutes(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, rt := range adminRoutes {
		if r := g.user(rt.method, rt.path, "{}"); r.Code != 403 || r.errCode() != "forbidden" {
			t.Errorf("%s %s as a user = %d %s, want 403 forbidden", rt.method, rt.path, r.Code, r.Body)
		}
	}
	if n := b.nonUserCalls(); n != 0 {
		t.Errorf("%d admin requests from an ordinary user reached a backend", n)
	}
}

func TestAdminRoutesAndUserRoutesWorkWithTheRightSession(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	for _, rt := range adminRoutes {
		if r := g.admin(rt.method, rt.path, "{}"); r.Code == 401 || r.Code == 403 || r.Code >= 500 {
			t.Errorf("%s %s as an admin = %d %s", rt.method, rt.path, r.Code, r.Body)
		}
	}
	for _, rt := range userRoutes {
		for who, call := range map[string]func(string, string, string) resp{"user": g.user, "admin": g.admin} {
			if r := call(rt.method, rt.path, "{}"); r.Code == 401 || r.Code == 403 || r.Code >= 500 {
				t.Errorf("%s %s as %s = %d %s", rt.method, rt.path, who, r.Code, r.Body)
			}
		}
	}
}

func TestProtectedRoutesFailClosedWhenTheUserServiceIsDown(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	b.user.Close()
	for _, rt := range []struct{ method, path string }{{"GET", "/api/v1/subscriptions"}, {"GET", "/api/v1/me/watchlist"}} {
		if r := g.do(rt.method, rt.path, "", "Authorization", "Bearer "+adminToken); r.Code != 502 && r.Code != 503 {
			t.Errorf("%s %s = %d %s, want a 5xx (never let anyone through)", rt.method, rt.path, r.Code, r.Body)
		}
	}
	if b.alert.count() != 0 {
		t.Error("a request reached the alert service although the session could not be verified")
	}
	// Public pages do not depend on the user service at all.
	for _, path := range []string{"/api/v1/locations", "/api/v1/locations/7/climate", "/api/v1/alerts"} {
		if r := g.get(path); r.Code != 200 {
			t.Errorf("GET %s = %d with the user service down", path, r.Code)
		}
	}
}

func TestAdminRoutesMapToTheRightBackend(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	cases := []struct {
		method, path string
		up           *fake
		wantPath     string
	}{
		{"GET", "/api/v1/admin/users", b.user, "/admin/users"},
		{"POST", "/api/v1/admin/users/4/disable", b.user, "/admin/users/4/disable"},
		{"POST", "/api/v1/admin/users/4/enable", b.user, "/admin/users/4/enable"},
		{"DELETE", "/api/v1/locations/7", b.weather, "/locations/7"},
		{"POST", "/api/v1/locations/7/refresh", b.weather, "/locations/7/refresh"},
		{"PUT", "/api/v1/simulation", b.weather, "/simulation"},
		{"POST", "/api/v1/alerts/3/ack", b.alert, "/alerts/3/ack"},
		{"POST", "/api/v1/subscriptions", b.alert, "/subscriptions"},
		{"DELETE", "/api/v1/subscriptions/2", b.alert, "/subscriptions/2"},
		{"GET", "/api/v1/notifications?status=failed", b.alert, "/notifications"},
		{"GET", "/api/v1/rejections/risk", b.risk, "/rejections"},
		{"GET", "/api/v1/rejections/processing", b.processing, "/rejections"},
		{"GET", "/api/v1/rejections/prediction", b.prediction, "/rejections"},
		{"GET", "/api/v1/rejections/alert", b.alert, "/rejections"},
	}
	for _, c := range cases {
		g.admin(c.method, c.path, "{}")
		if got := c.up.last(); got.path != c.wantPath || got.method != c.method {
			t.Errorf("%s %s -> %s %s, want %s %s", c.method, c.path, got.method, got.path, c.method, c.wantPath)
		}
	}
	if r := g.admin("GET", "/api/v1/rejections/weather", ""); r.Code != 404 {
		t.Errorf("unknown service = %d", r.Code)
	}
	if r := g.admin("GET", "/api/v1/rejections/..%2fadmin", ""); r.Code != 404 {
		t.Errorf("a service name is looked up, never used as a path: %d", r.Code)
	}
}

// ---- rate limiting ----

func TestRateLimitingReturns429WithRetryAfter(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	clock := newClock()
	cfg.Clock = clock.now
	cfg.RateLimit = RateConfig{PerMinute: 60, Burst: 3}
	g := newGW(t, cfg)

	for i := 0; i < 3; i++ {
		if r := g.get("/api/v1/locations"); r.Code != 200 {
			t.Fatalf("request %d = %d", i+1, r.Code)
		}
	}
	r := g.get("/api/v1/locations")
	if r.Code != 429 || r.errCode() != "rate_limited" || r.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit = %d %s retry-after=%q", r.Code, r.Body, r.Header().Get("Retry-After"))
	}
	if b.weather.count() != 3 {
		t.Errorf("the backend saw %d requests; a limited request must not reach it", b.weather.count())
	}
	other := g.do("GET", "/api/v1/locations", "")
	other2 := httptest.NewRequest("GET", "/api/v1/locations", nil)
	other2.RemoteAddr = "203.0.113.9:1"
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, other2)
	if other.Code != 429 || rec.Code != 200 {
		t.Errorf("limits are per client: same client %d, other client %d", other.Code, rec.Code)
	}
	clock.advance(2 * time.Second)
	if r := g.get("/api/v1/locations"); r.Code != 200 {
		t.Errorf("after waiting = %d", r.Code)
	}
}

func TestWritesHaveAStricterLimit(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(jsonHandler(201, locJSON))
	cfg := b.config()
	clock := newClock()
	cfg.Clock = clock.now
	cfg.RateLimit = RateConfig{PerMinute: 6000, Burst: 1000}
	cfg.WriteRateLimit = RateConfig{PerMinute: 6, Burst: 2}
	g := newGW(t, cfg)

	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, g.user("POST", "/api/v1/locations", `{"name":"x","latitude":1,"longitude":1}`).Code)
	}
	if codes[0] != 201 || codes[1] != 201 || codes[2] != 429 || codes[3] != 429 {
		t.Errorf("POST statuses = %v, want two accepted then limited", codes)
	}
	for i := 0; i < 20; i++ {
		if r := g.get("/api/v1/locations"); r.Code == 429 {
			t.Fatalf("reads were limited by the write limit")
		}
	}
}

func TestRateLimitComesBeforeTheAdminCheck(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	cfg.Clock = newClock().now
	cfg.RateLimit = RateConfig{PerMinute: 60, Burst: 3}
	g := newGW(t, cfg)
	codes := []int{}
	for i := 0; i < 6; i++ { // password guessing must run into the limiter, not into unlimited 401s
		codes = append(codes, g.do("GET", "/api/v1/subscriptions", "", "Authorization", "Bearer guess").Code)
	}
	if codes[0] != 401 || codes[5] != 429 {
		t.Errorf("statuses = %v", codes)
	}
}

func TestForwardedForIsHonouredOnlyWhenTrusted(t *testing.T) {
	for _, trust := range []bool{false, true} {
		b := healthyBackends(t)
		cfg := b.config()
		cfg.Clock = newClock().now
		cfg.RateLimit = RateConfig{PerMinute: 60, Burst: 1}
		cfg.TrustProxy = trust
		g := newGW(t, cfg)
		a := g.get("/api/v1/locations", "X-Forwarded-For", "203.0.113.1").Code
		bb := g.get("/api/v1/locations", "X-Forwarded-For", "203.0.113.2").Code
		if trust && (a != 200 || bb != 200) {
			t.Errorf("trusted proxy: distinct clients must have distinct limits (%d, %d)", a, bb)
		}
		if !trust && (a != 200 || bb != 429) {
			t.Errorf("untrusted: a spoofed header must not dodge the limit (%d, %d)", a, bb)
		}
	}
}

// ---- CORS through the full stack ----

func TestCORSHeadersReachBrowsersEvenOnErrors(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	cfg.CORSOrigins = []string{"https://app.example.org"}
	cfg.Clock = newClock().now
	cfg.RateLimit = RateConfig{PerMinute: 60, Burst: 1}
	g := newGW(t, cfg)

	r := g.get("/api/v1/locations", "Origin", "https://app.example.org")
	if r.Header().Get("Access-Control-Allow-Origin") != "https://app.example.org" {
		t.Errorf("headers = %v", r.Header())
	}
	r = g.get("/api/v1/locations", "Origin", "https://app.example.org") // now rate limited
	if r.Code != 429 || r.Header().Get("Access-Control-Allow-Origin") == "" || r.Header().Get("Access-Control-Expose-Headers") == "" {
		t.Errorf("an error response must carry CORS headers or the page cannot read it: %d %v", r.Code, r.Header())
	}
	pre := g.do("OPTIONS", "/api/v1/subscriptions", "", "Origin", "https://app.example.org", "Access-Control-Request-Method", "POST")
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("preflight = %d %v", pre.Code, pre.Header())
	}
	if r := g.get("/api/v1/locations", "Origin", "https://evil.example.com"); r.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a foreign origin was allowed")
	}
}

// ---- upstream failures ----

func TestUpstreamDownTimeoutAndOpenCircuit(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	cfg.UpstreamTimeout = 150 * time.Millisecond
	g := newGW(t, cfg)

	b.weather.Close()
	r := g.get("/api/v1/locations")
	if r.Code != 502 || r.errCode() != "upstream_unavailable" || !strings.Contains(r.Body.String(), "weather") {
		t.Errorf("down = %d %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "127.0.0.1") {
		t.Error("an internal address leaked into the error message")
	}

	b.alert.set(hang)
	started := time.Now()
	r = g.get("/api/v1/alerts")
	if r.Code != 504 || r.errCode() != "upstream_timeout" || time.Since(started) > time.Second {
		t.Errorf("slow = %d %s after %v", r.Code, r.Body, time.Since(started))
	}

	// After enough failures the breaker opens and answers instantly, without calling the backend.
	b.risk.set(failing)
	for i := 0; i < 3; i++ {
		g.admin("GET", "/api/v1/rejections/risk", "")
	}
	before := b.risk.count()
	started = time.Now()
	r = g.admin("GET", "/api/v1/rejections/risk", "")
	if r.Code != 503 || r.errCode() != "upstream_unavailable" || b.risk.count() != before || time.Since(started) > 100*time.Millisecond {
		t.Errorf("open circuit = %d %s; backend calls %d -> %d", r.Code, r.Body, before, b.risk.count())
	}
}

func TestAnUpstreamFiveHundredIsRelayedAndCountsAgainstTheBreaker(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(failing)
	g := newGW(t, b.config())
	if r := g.get("/api/v1/locations"); r.Code != 500 {
		t.Errorf("a backend 500 should be relayed, got %d", r.Code)
	}
	g.get("/api/v1/locations")
	g.get("/api/v1/locations")
	if g.g.weather.Breaker.State() != StateOpen {
		t.Errorf("breaker = %s after 3 failures", g.g.weather.Breaker.State())
	}
}

func TestClientErrorsNeverTripTheBreaker(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(jsonHandler(404, `{"error":{"code":"not_found"}}`))
	g := newGW(t, b.config())
	for i := 0; i < 20; i++ {
		g.get("/api/v1/locations/7")
	}
	if g.g.weather.Breaker.State() != StateClosed {
		t.Error("404s are the backend working correctly and must not open the breaker")
	}
}

func TestBackendRedirectsAreNotFollowed(t *testing.T) {
	b := healthyBackends(t)
	internal := newFake(t, jsonHandler(200, `{"secret":"internal"}`))
	b.weather.set(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, internal.URL, http.StatusFound) })
	g := newGW(t, b.config())
	r := g.get("/api/v1/locations")
	if internal.count() != 0 || strings.Contains(r.Body.String(), "internal") {
		t.Errorf("the gateway followed a backend redirect (internal calls %d)", internal.count())
	}
}

func TestClientDisconnectDoesNotCountAgainstTheBackend(t *testing.T) {
	b := healthyBackends(t)
	b.weather.set(hang)
	cfg := b.config()
	cfg.UpstreamTimeout = 5 * time.Second
	g := newGW(t, cfg)

	for i := 0; i < 6; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest("GET", "/api/v1/locations", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { g.h.ServeHTTP(rec, req); close(done) }()
		time.Sleep(30 * time.Millisecond)
		cancel()
		<-done
	}
	if g.g.weather.Breaker.State() != StateClosed {
		t.Error("impatient clients opened the circuit for a perfectly healthy backend")
	}
}

func TestMetricsExposeGatewayCounters(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	cfg.Clock = newClock().now
	cfg.RateLimit = RateConfig{PerMinute: 60, Burst: 1}
	g := newGW(t, cfg)
	g.get("/api/v1/locations")
	g.get("/api/v1/locations")               // limited
	g.do("GET", "/api/v1/subscriptions", "") // limited too (same client)
	text := g.get("/metrics").Body.String()
	for _, want := range []string{
		`gateway_upstream_requests_total{upstream="weather",result="ok"} 1`,
		`gateway_circuit_open{upstream="weather"} 0`,
		`gateway_rate_limited_total 2`,
		`gateway_climate_total{status="ok"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
