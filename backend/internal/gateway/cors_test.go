package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsRequest(c CORS, method, origin string, extra map[string]string) *httptest.ResponseRecorder {
	h := c.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest(method, "/api/v1/x", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	if method == http.MethodOptions {
		c.preflight(rec, r)
	} else {
		h.ServeHTTP(rec, r)
	}
	return rec
}

func TestCORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	c := CORS{Origins: []string{"https://app.example.org"}}
	rec := corsRequest(c, "GET", "https://app.example.org", nil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.org" || rec.Header().Get("Vary") != "Origin" {
		t.Errorf("allowed origin headers = %v", rec.Header())
	}
	if rec := corsRequest(c, "GET", "https://evil.example.com", nil); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a foreign origin got CORS headers")
	}
	if rec := corsRequest(c, "GET", "", nil); rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "" {
		t.Error("a same-origin request (no Origin header) needs no CORS headers")
	}
	if rec := corsRequest(CORS{}, "GET", "https://app.example.org", nil); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("with no origins configured nothing may be allowed")
	}
}

func TestCORSWildcardAndCaseInsensitiveMatch(t *testing.T) {
	if rec := corsRequest(CORS{Origins: []string{"*"}}, "GET", "https://anything.example", nil); rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("wildcard = %v", rec.Header())
	}
	if rec := corsRequest(CORS{Origins: []string{"https://App.Example.org"}}, "GET", "https://app.example.org", nil); rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("origins should match case-insensitively")
	}
}

func TestCORSPreflight(t *testing.T) {
	c := CORS{Origins: []string{"https://app.example.org"}}
	rec := corsRequest(c, "OPTIONS", "https://app.example.org", map[string]string{"Access-Control-Request-Method": "POST"})
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") == "" ||
		rec.Header().Get("Access-Control-Allow-Headers") == "" || rec.Header().Get("Access-Control-Max-Age") != "600" {
		t.Errorf("preflight = %d %v", rec.Code, rec.Header())
	}
	rec = corsRequest(c, "OPTIONS", "https://evil.example.com", map[string]string{"Access-Control-Request-Method": "POST"})
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("a foreign origin's preflight must be answered without permission: %d %v", rec.Code, rec.Header())
	}
}
