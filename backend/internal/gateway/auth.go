package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// sessionCookie is the browser's session cookie. It is HttpOnly (scripts cannot read
// it) and SameSite=Lax (other sites cannot make the browser send it with a POST).
const sessionCookie = "hm_session"

// returnTokenHeader asks for the session token in the response body, for command-line
// clients that cannot keep a cookie. Browsers never send it: it is not CORS-allowed.
const returnTokenHeader = "X-Return-Token"

// authLevel says who may call a route.
type authLevel int

const (
	authNone  authLevel = iota // anyone
	authUser                   // any signed-in user
	authAdmin                  // administrators only
)

// Principal is the signed-in user behind a request.
type Principal struct {
	UserID int64  `json:"userId"`
	Role   string `json:"role"`
	Email  string `json:"email"`
}

type principalKey struct{}

func principalOf(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(Principal)
	return p, ok
}

var errNoSession = errors.New("no valid session")

// credential returns the session token a request carries: a Bearer token wins over the
// cookie. The second result says it came from the cookie, the only kind a browser sends
// on its own and so the only kind that needs cross-site protection.
func credential(r *http.Request) (token string, viaCookie bool) {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && t != "" {
		return t, false
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// crossSite reports a browser request made from another site. Browsers label every
// request with Sec-Fetch-Site; clients that are not browsers send nothing, and are not
// at risk, because they do not attach cookies by themselves.
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	return false
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sessionCache remembers who a token belongs to for a few seconds, so a page that makes
// several calls does not ask the user service every time. Logout, password changes and
// account changes by an administrator clear it, so the delay only ever applies to a
// session ended by some other route.
type sessionCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[string]cached
}

type cached struct {
	p       Principal
	expires time.Time
}

const maxCachedSessions = 5000

func newSessionCache(ttl time.Duration, now func() time.Time) *sessionCache {
	if now == nil {
		now = time.Now
	}
	return &sessionCache{ttl: ttl, now: now, m: map[string]cached{}}
}

func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (c *sessionCache) get(token string) (Principal, bool) {
	if c.ttl <= 0 {
		return Principal{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[cacheKey(token)]
	if !ok || !c.now().Before(e.expires) {
		delete(c.m, cacheKey(token))
		return Principal{}, false
	}
	return e.p, true
}

func (c *sessionCache) put(token string, p Principal) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= maxCachedSessions {
		for k, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxCachedSessions {
			c.m = map[string]cached{} // under a flood of valid tokens: start over rather than grow
		}
	}
	c.m[cacheKey(token)] = cached{p: p, expires: now.Add(c.ttl)}
}

func (c *sessionCache) forget(token string) {
	c.mu.Lock()
	delete(c.m, cacheKey(token))
	c.mu.Unlock()
}

func (c *sessionCache) clear() {
	c.mu.Lock()
	c.m = map[string]cached{}
	c.mu.Unlock()
}

// resolve asks the user service who a token belongs to. errNoSession means the token is
// not valid (unknown, expired, or the account is disabled); any other error is a failed call.
func (g *Gateway) resolve(ctx context.Context, token string) (Principal, error) {
	if p, ok := g.sessions.get(token); ok {
		return p, nil
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	res, err := g.user.Do(ctx, http.MethodPost, "/sessions/resolve", "", body, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return Principal{}, err
	}
	switch {
	case res.Status == http.StatusUnauthorized:
		return Principal{}, errNoSession
	case res.Status != http.StatusOK:
		return Principal{}, &CallError{Upstream: g.user.Name, Kind: KindUnavailable}
	}
	var p Principal
	if err := json.Unmarshal(res.Body, &p); err != nil || p.UserID <= 0 {
		return Principal{}, &CallError{Upstream: g.user.Name, Kind: KindUnavailable, Err: errors.New("unreadable session answer")}
	}
	g.sessions.put(token, p)
	return p, nil
}

// authenticate enforces a route's auth level and puts the principal on the request.
func (g *Gateway) authenticate(level authLevel, next http.Handler) http.Handler {
	if level == authNone {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, viaCookie := credential(r)
		if token == "" {
			g.deny(w, r, http.StatusUnauthorized, "unauthorized", "sign in to do that")
			return
		}
		if viaCookie && !safeMethod(r.Method) && crossSite(r) {
			g.deny(w, r, http.StatusForbidden, "cross_site_request", "this request was made from another site")
			return
		}
		p, err := g.resolve(r.Context(), token)
		switch {
		case errors.Is(err, errNoSession):
			if viaCookie {
				g.clearCookie(w, r)
			}
			g.deny(w, r, http.StatusUnauthorized, "unauthorized", "your session has ended; sign in again")
			return
		case err != nil:
			g.writeCallError(w, r, err)
			return
		}
		if level == authAdmin && p.Role != "admin" {
			g.deny(w, r, http.StatusForbidden, "forbidden", "administrator access is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (g *Gateway) deny(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	g.authDenied.Add(1)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="heatwave-monitor"`)
	}
	httpx.WriteError(w, r, status, code, message)
}

// outbound builds the headers sent to a backend from scratch. Nothing the client sent is
// copied except what the service needs to understand the request; in particular a
// client's own X-User-* headers, cookies and Authorization never reach a backend.
func (g *Gateway) outbound(r *http.Request) http.Header {
	h := http.Header{}
	for _, name := range []string{"Content-Type", "Accept"} {
		if v := r.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	h.Set(httpx.HeaderClientIP, ClientKey(r, g.cfg.TrustProxy))
	if p, ok := principalOf(r); ok {
		h.Set(httpx.HeaderUserID, formatID(p.UserID))
		h.Set(httpx.HeaderUserRole, p.Role)
	}
	return h
}

func (g *Gateway) secureCookie(r *http.Request) bool {
	return g.cfg.SecureCookies || (g.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (g *Gateway) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: g.secureCookie(r),
	})
}

func (g *Gateway) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: g.secureCookie(r),
	})
}
