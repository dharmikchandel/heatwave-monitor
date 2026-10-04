package gateway

import (
	"net/http"
	"strings"
)

// CORS decides which browser origins may call the API from another origin. With
// no origins configured nothing is allowed, which is right when the frontend
// reaches the gateway through its own server (same origin).
type CORS struct {
	Origins []string // exact origins such as "https://app.example.org", or "*"
}

func (c CORS) allowed(origin string) bool {
	for _, o := range c.Origins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// apply adds CORS headers when the request's Origin is allowed.
func (c CORS) apply(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	w.Header().Add("Vary", "Origin")
	if !c.allowed(origin) {
		return false
	}
	if len(c.Origins) == 1 && c.Origins[0] == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
	return true
}

// preflight answers an OPTIONS request.
func (c CORS) preflight(w http.ResponseWriter, r *http.Request) {
	if c.apply(w, r) && r.Header.Get("Access-Control-Request-Method") != "" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	w.WriteHeader(http.StatusNoContent)
}

// middleware adds CORS headers to every response, so that even errors (429, 401)
// are readable by the calling page.
func (c CORS) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.apply(w, r)
		next.ServeHTTP(w, r)
	})
}
