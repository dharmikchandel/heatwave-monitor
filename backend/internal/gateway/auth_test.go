package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func cookieNamed(r resp, name string) *http.Cookie {
	for _, c := range r.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// revokeUserToken is a user service that has ended the ordinary user's session.
func revokeUserToken(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/sessions/resolve" {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), userToken) {
			jsonHandler(401, `{"error":{"code":"invalid_session","message":"gone"}}`)(w, r)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	userService(w, r)
}

func TestLoginSetsAHardenedCookieAndKeepsTheTokenOutOfTheBody(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register"} {
		r := g.do("POST", path, `{"email":"ada@example.org","password":"x"}`, "Content-Type", "application/json")
		if r.Code != 200 && r.Code != 201 {
			t.Fatalf("POST %s = %d %s", path, r.Code, r.Body)
		}
		c := cookieNamed(r, sessionCookie)
		if c == nil || c.Value != "fresh-token" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
			t.Fatalf("%s cookie = %+v", path, c)
		}
		if c.Expires.Year() != 2030 {
			t.Errorf("%s cookie expiry = %v, want the session's own expiry", path, c.Expires)
		}
		if strings.Contains(r.Body.String(), "fresh-token") || r.json()["token"] != nil || r.json()["user"] == nil {
			t.Errorf("%s body = %s; the token must not be readable by scripts", path, r.Body)
		}
	}
	if got := b.user.last().header.Get("X-Client-IP"); got != "198.51.100.1" {
		t.Errorf("the user service was told the client is %q", got)
	}
	if r := g.do("POST", "/api/v1/auth/register", `{}`); r.Code != 201 {
		t.Errorf("register status = %d, want the service's 201", r.Code)
	}
}

func TestCommandLineClientsCanAskForTheToken(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	r := g.do("POST", "/api/v1/auth/login", `{}`, returnTokenHeader, "1")
	if r.json()["token"] != "fresh-token" || r.json()["expiresAt"] == nil {
		t.Errorf("body = %s", r.Body)
	}
	for _, v := range []string{"", "0", "true", "yes"} {
		if r := g.do("POST", "/api/v1/auth/login", `{}`, returnTokenHeader, v); r.json()["token"] != nil {
			t.Errorf("%s: %q returned the token", returnTokenHeader, v)
		}
	}
}

func TestFailedSignInsAreRelayedWithoutACookie(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	b.user.set(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "840")
		jsonHandler(429, `{"error":{"code":"too_many_attempts","message":"locked"}}`)(w, r)
	})
	r := g.do("POST", "/api/v1/auth/login", `{}`)
	if r.Code != 429 || r.errCode() != "too_many_attempts" || r.Header().Get("Retry-After") != "840" || len(r.Result().Cookies()) != 0 {
		t.Errorf("= %d %s retry-after=%q cookies=%v", r.Code, r.Body, r.Header().Get("Retry-After"), r.Result().Cookies())
	}
	b.user.set(jsonHandler(401, `{"error":{"code":"invalid_credentials","message":"no"}}`))
	if r := g.do("POST", "/api/v1/auth/login", `{}`); r.Code != 401 || r.errCode() != "invalid_credentials" || len(r.Result().Cookies()) != 0 {
		t.Errorf("= %d %s", r.Code, r.Body)
	}
	b.user.set(jsonHandler(200, `{"user":{},"token":""}`))
	if r := g.do("POST", "/api/v1/auth/login", `{}`); r.Code != 502 {
		t.Errorf("a success without a token = %d, want 502", r.Code)
	}
}

func TestSecureCookieFlag(t *testing.T) {
	login := func(cfg func(*Config), headers ...string) bool {
		b := healthyBackends(t)
		c := b.config()
		cfg(&c)
		c2 := newGW(t, c).do("POST", "/api/v1/auth/login", `{}`, headers...)
		return cookieNamed(c2, sessionCookie).Secure
	}
	if login(func(*Config) {}) {
		t.Error("plain http must not get a Secure cookie (browsers would drop it)")
	}
	if !login(func(c *Config) { c.SecureCookies = true }) {
		t.Error("SecureCookies must set the flag")
	}
	if !login(func(c *Config) { c.TrustProxy = true }, "X-Forwarded-Proto", "https") {
		t.Error("behind a trusted https proxy the cookie must be Secure")
	}
	if login(func(c *Config) {}, "X-Forwarded-Proto", "https") {
		t.Error("X-Forwarded-Proto must be ignored when the proxy is not trusted")
	}
}

func TestSessionEndpointAnswersWhoAmI(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())

	r := g.get("/api/v1/auth/session")
	if r.Code != 200 || r.json()["user"] != nil || len(r.Result().Cookies()) != 0 {
		t.Errorf("anonymous = %d %s", r.Code, r.Body)
	}
	r = g.get("/api/v1/auth/session", "Cookie", sessionCookie+"="+userToken)
	user, _ := r.json()["user"].(map[string]any)
	if r.Code != 200 || user["email"] != "ada@example.org" || user["role"] != "user" {
		t.Errorf("signed in = %d %s", r.Code, r.Body)
	}
	if _, leaked := user["passwordHash"]; leaked {
		t.Error("session endpoint leaks the hash")
	}
	// A stale cookie is answered as anonymous and removed, so the browser stops sending it.
	r = g.get("/api/v1/auth/session", "Cookie", sessionCookie+"=stale")
	c := cookieNamed(r, sessionCookie)
	if r.Code != 200 || r.json()["user"] != nil || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("stale cookie = %d %s cookie=%+v", r.Code, r.Body, c)
	}
	// A stale Bearer token is not a browser cookie and is not "cleared".
	if r = g.get("/api/v1/auth/session", "Authorization", "Bearer stale"); r.json()["user"] != nil || cookieNamed(r, sessionCookie) != nil {
		t.Errorf("stale bearer = %s", r.Body)
	}
	b.user.Close()
	if r = g.get("/api/v1/auth/session", "Cookie", sessionCookie+"=fresh-user-token"); r.Code < 500 {
		t.Errorf("user service down = %d; do not claim the visitor is anonymous", r.Code)
	}
}

func TestCookieAndBearerBothAuthenticateAndBearerWins(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	if r := g.get("/api/v1/me/watchlist", "Cookie", sessionCookie+"="+userToken); r.Code != 200 {
		t.Errorf("cookie = %d", r.Code)
	}
	if r := g.get("/api/v1/subscriptions", "Cookie", sessionCookie+"="+userToken, "Authorization", "Bearer "+adminToken); r.Code != 200 {
		t.Errorf("bearer admin with a user cookie = %d, want the bearer to win", r.Code)
	}
	if r := g.get("/api/v1/subscriptions", "Cookie", sessionCookie+"="+adminToken, "Authorization", "Bearer "+userToken); r.Code != 403 {
		t.Errorf("bearer user with an admin cookie = %d, want 403", r.Code)
	}
}

func TestCrossSiteBrowserWritesWithACookieAreRefused(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	cookie := []string{"Cookie", sessionCookie + "=" + userToken}
	write := func(site string) resp {
		h := append([]string{}, cookie...)
		if site != "" {
			h = append(h, "Sec-Fetch-Site", site)
		}
		return g.do("POST", "/api/v1/me/subscriptions", `{"minLevel":"danger"}`, h...)
	}
	for _, site := range []string{"cross-site", "same-site"} {
		if r := write(site); r.Code != 403 || r.errCode() != "cross_site_request" {
			t.Errorf("%s = %d %s", site, r.Code, r.Body)
		}
	}
	if b.alert.count() != 0 {
		t.Error("a cross-site write reached the backend")
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if r := write(site); r.Code != 200 {
			t.Errorf("Sec-Fetch-Site %q = %d, want the write allowed", site, r.Code)
		}
	}
	// Reads are safe whatever the origin; and a Bearer token is not ambient, so it is not at risk.
	if r := g.get("/api/v1/me/watchlist", append(cookie, "Sec-Fetch-Site", "cross-site")...); r.Code != 200 {
		t.Errorf("cross-site read = %d", r.Code)
	}
	if r := g.do("POST", "/api/v1/me/subscriptions", `{}`, "Authorization", "Bearer "+userToken, "Sec-Fetch-Site", "cross-site"); r.Code != 200 {
		t.Errorf("cross-site write with a Bearer token = %d", r.Code)
	}
}

func TestLogoutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r := g.do("POST", "/api/v1/auth/logout", "", "Cookie", sessionCookie+"="+userToken)
	c := cookieNamed(r, sessionCookie)
	if r.Code != 204 || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("= %d cookie=%+v", r.Code, c)
	}
	last := b.user.last()
	if last.path != "/logout" || last.header.Get("Authorization") != "Bearer "+userToken {
		t.Errorf("the user service saw %s %q", last.path, last.header.Get("Authorization"))
	}
	// Signing out always works: with nothing to end, or with the user service down.
	if r := g.do("POST", "/api/v1/auth/logout", ""); r.Code != 204 {
		t.Errorf("logout while signed out = %d", r.Code)
	}
	b.user.Close()
	if r := g.do("POST", "/api/v1/auth/logout", "", "Cookie", sessionCookie+"=x"); r.Code != 204 || cookieNamed(r, sessionCookie) == nil {
		t.Errorf("logout with the user service down = %d", r.Code)
	}
}

func TestPasswordChangeReplacesTheCookie(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	r := g.user("POST", "/api/v1/auth/password", `{"current":"a","new":"b"}`)
	if c := cookieNamed(r, sessionCookie); r.Code != 200 || c == nil || c.Value != "fresh-token" || strings.Contains(r.Body.String(), "fresh-token") {
		t.Errorf("= %d %s cookie=%+v", r.Code, r.Body, c)
	}
	if got := b.user.last(); got.path != "/me/password" || got.header.Get("X-User-ID") != "2" {
		t.Errorf("the user service saw %+v", got)
	}
}

func TestDeletingAnAccount(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())

	r := g.do("DELETE", "/api/v1/auth/account", `{"password":"pw"}`, "Cookie", sessionCookie+"="+userToken)
	c := cookieNamed(r, sessionCookie)
	if r.Code != 204 || c == nil || c.MaxAge >= 0 {
		t.Fatalf("= %d cookie=%+v", r.Code, c)
	}
	if got := b.user.last(); got.method != "DELETE" || got.path != "/me" || got.body != `{"password":"pw"}` || got.header.Get("X-User-ID") != "2" {
		t.Errorf("the user service saw %+v", got)
	}
	if got := b.alert.last(); got.method != "DELETE" || got.path != "/internal/users/2" {
		t.Errorf("the alert service saw %+v; the user's subscriptions and inbox must go too", got)
	}

	// A wrong password changes nothing anywhere.
	b2 := healthyBackends(t)
	g2 := newGW(t, b2.config())
	b2.user.set(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" {
			jsonHandler(401, `{"error":{"code":"invalid_credentials","message":"no"}}`)(w, r)
			return
		}
		userService(w, r)
	})
	r = g2.do("DELETE", "/api/v1/auth/account", `{"password":"bad"}`, "Authorization", "Bearer "+userToken)
	if r.Code != 401 || r.errCode() != "invalid_credentials" || b2.alert.count() != 0 || cookieNamed(r, sessionCookie) != nil {
		t.Errorf("wrong password = %d %s alert calls=%d", r.Code, r.Body, b2.alert.count())
	}

	// If the alert service is down the account is still gone, and the visitor is told so.
	b3 := healthyBackends(t)
	g3 := newGW(t, b3.config())
	b3.alert.Close()
	if r := g3.user("DELETE", "/api/v1/auth/account", `{"password":"pw"}`); r.Code != 204 {
		t.Errorf("alert service down = %d", r.Code)
	}
}

func TestAnonymousVisitorsCannotDeleteOrChange(t *testing.T) {
	g := newGW(t, healthyBackends(t).config())
	for _, rt := range []struct{ m, p string }{{"DELETE", "/api/v1/auth/account"}, {"POST", "/api/v1/auth/password"}} {
		if r := g.do(rt.m, rt.p, `{}`); r.Code != 401 {
			t.Errorf("%s %s = %d", rt.m, rt.p, r.Code)
		}
	}
}

func TestSessionsAreCachedBriefly(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	clock := newClock()
	cfg.Clock = clock.now
	cfg.SessionCache = 5 * time.Second
	g := newGW(t, cfg)

	for i := 0; i < 5; i++ {
		if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 200 {
			t.Fatal(r.Code)
		}
	}
	if n := b.user.resolves(); n != 1 {
		t.Errorf("%d session lookups for five requests, want 1", n)
	}
	clock.advance(6 * time.Second)
	g.user("GET", "/api/v1/me/watchlist", "")
	if n := b.user.resolves(); n != 2 {
		t.Errorf("%d lookups after the cache expired, want 2", n)
	}

	// Logging out takes effect at once, not after the cache expires.
	b.user.set(revokeUserToken)
	g.do("POST", "/api/v1/auth/logout", "", "Authorization", "Bearer "+userToken)
	if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 401 {
		t.Errorf("after logout = %d, want 401 immediately", r.Code)
	}
}

func TestSessionCacheCanBeDisabled(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	cfg.SessionCache = -1
	g := newGW(t, cfg)
	g.user("GET", "/api/v1/me/watchlist", "")
	g.user("GET", "/api/v1/me/watchlist", "")
	if n := b.user.resolves(); n != 2 {
		t.Errorf("%d lookups with caching off, want 2", n)
	}
}

func TestDisablingAnAccountLocksItOutAtOnce(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 200 { // now cached
		t.Fatal(r.Code)
	}
	b.user.set(revokeUserToken) // the user service now refuses the ordinary user's session
	if r := g.admin("POST", "/api/v1/admin/users/2/disable", ""); r.Code != 200 {
		t.Fatalf("disable = %d %s", r.Code, r.Body)
	}
	if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 401 {
		t.Errorf("disabled account still has access: %d", r.Code)
	}
}

func TestSignInStyleRoutesHaveTheirOwnStricterLimit(t *testing.T) {
	b := healthyBackends(t)
	cfg := b.config()
	clock := newClock()
	cfg.Clock = clock.now
	cfg.RateLimit = RateConfig{PerMinute: 6000, Burst: 1000}
	cfg.WriteRateLimit = RateConfig{PerMinute: 6000, Burst: 1000}
	cfg.AuthRateLimit = RateConfig{PerMinute: 6, Burst: 2}
	g := newGW(t, cfg)

	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, g.do("POST", "/api/v1/auth/login", `{}`).Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 || codes[3] != 429 {
		t.Errorf("login statuses = %v", codes)
	}
	if r := g.do("POST", "/api/v1/auth/register", `{}`); r.Code != 429 {
		t.Errorf("register shares the sign-in limit: %d", r.Code)
	}
	if r := g.user("POST", "/api/v1/auth/password", `{}`); r.Code != 429 {
		t.Errorf("password changes share the sign-in limit: %d", r.Code)
	}
	for _, rt := range []struct{ m, p string }{{"GET", "/api/v1/auth/session"}, {"POST", "/api/v1/auth/logout"}, {"POST", "/api/v1/me/subscriptions"}} {
		if r := g.user(rt.m, rt.p, `{}`); r.Code == 429 {
			t.Errorf("%s %s was caught by the sign-in limit", rt.m, rt.p)
		}
	}
}

func TestUserRoutesMapToTheRightBackend(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	cases := []struct {
		method, path string
		up           *fake
		wantPath     string
	}{
		{"GET", "/api/v1/me/watchlist", b.user, "/watchlist"},
		{"PUT", "/api/v1/me/watchlist/7", b.user, "/watchlist/7"},
		{"DELETE", "/api/v1/me/watchlist/7", b.user, "/watchlist/7"},
		{"GET", "/api/v1/me/subscriptions", b.alert, "/my/subscriptions"},
		{"POST", "/api/v1/me/subscriptions", b.alert, "/my/subscriptions"},
		{"DELETE", "/api/v1/me/subscriptions/3", b.alert, "/my/subscriptions/3"},
		{"GET", "/api/v1/me/notifications?limit=5", b.alert, "/my/notifications"},
		{"POST", "/api/v1/me/notifications/read", b.alert, "/my/notifications/read"},
		{"POST", "/api/v1/me/notifications/9/read", b.alert, "/my/notifications/9/read"},
		{"POST", "/api/v1/locations", b.weather, "/locations"},
	}
	for _, c := range cases {
		g.user(c.method, c.path, `{}`)
		got := c.up.last()
		if got.path != c.wantPath || got.method != c.method || got.header.Get("X-User-ID") != "2" && c.up != b.weather {
			t.Errorf("%s %s -> %s %s (user %q), want %s %s", c.method, c.path, got.method, got.path, got.header.Get("X-User-ID"), c.method, c.wantPath)
		}
	}
	if got := b.alert.last(); got.path == "/my/notifications/9/read" && got.header.Get("X-User-ID") != "2" {
		t.Error("identity missing")
	}
	for _, id := range []string{"abc", "0", "-1"} {
		if r := g.user("PUT", "/api/v1/me/watchlist/"+id, `{}`); r.Code != 400 {
			t.Errorf("watchlist id %q = %d", id, r.Code)
		}
	}
}

func TestPasswordChangeEndsCachedSessionsAtOnce(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	if r := g.do("GET", "/api/v1/me/watchlist", "", "Authorization", "Bearer "+adminToken); r.Code != 200 { // cached
		t.Fatal(r.Code)
	}
	if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 200 { // cached
		t.Fatal(r.Code)
	}
	b.user.set(revokeUserToken) // changing the password has ended the user's other sessions
	if r := g.do("POST", "/api/v1/auth/password", `{}`, "Authorization", "Bearer "+adminToken); r.Code != 200 {
		t.Fatalf("change = %d", r.Code)
	}
	if r := g.user("GET", "/api/v1/me/watchlist", ""); r.Code != 401 {
		t.Errorf("a session ended by a password change still works from the cache: %d", r.Code)
	}
}

func TestSigningInFromAnotherSiteIsRefused(t *testing.T) {
	b := healthyBackends(t)
	g := newGW(t, b.config())
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register"} {
		for _, site := range []string{"cross-site", "same-site"} {
			r := g.do("POST", path, `{}`, "Sec-Fetch-Site", site)
			if r.Code != 403 || r.errCode() != "cross_site_request" || len(r.Result().Cookies()) != 0 {
				t.Errorf("%s from %s = %d %s", path, site, r.Code, r.Body)
			}
		}
	}
	if b.user.count() != 0 {
		t.Error("a cross-site sign-in reached the user service")
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if r := g.do("POST", "/api/v1/auth/login", `{}`, "Sec-Fetch-Site", site); r.Code != 200 {
			t.Errorf("Sec-Fetch-Site %q = %d, want the sign-in to work", site, r.Code)
		}
	}
}

func TestBrowsersFromOtherOriginsCannotAskForTheToken(t *testing.T) {
	cfg := healthyBackends(t).config()
	cfg.CORSOrigins = []string{"https://app.example.org"}
	g := newGW(t, cfg)
	r := g.do("OPTIONS", "/api/v1/auth/login", "", "Origin", "https://app.example.org", "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", returnTokenHeader)
	allowed := strings.ToLower(r.Header().Get("Access-Control-Allow-Headers"))
	if strings.Contains(allowed, strings.ToLower(returnTokenHeader)) {
		t.Errorf("preflight allows %s (%q); a page on another origin could then read session tokens", returnTokenHeader, allowed)
	}
	if r.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("cross-origin requests must not be allowed to carry credentials")
	}
}
