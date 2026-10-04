package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// newRequestID returns 16 random hex characters.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("httpx: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// validRequestID accepts caller-supplied IDs only if they are short and made of
// safe characters, so they can't inject into logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush etc.).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// quietRoutes are logged at debug level so probes don't flood the log.
var quietRoutes = map[string]bool{
	"GET /healthz": true,
	"GET /readyz":  true,
	"GET /metrics": true,
}

// Middleware assigns a request ID (reusing a valid inbound X-Request-ID),
// attaches a request-scoped logger, recovers from panics with a 500, records
// metrics and writes one access-log line per request.
func Middleware(log *slog.Logger, m *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)

		reqLog := log.With("request_id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, loggerKey, reqLog)
		// Pass this exact *Request down: ServeMux records the matched pattern on it,
		// which we read afterwards to label metrics with the route, not the raw path.
		r = r.WithContext(ctx)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				reqLog.Error("panic in handler", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if !rec.wrote {
					WriteError(rec, r, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}

			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			m.observe(r.Method, route, rec.status, elapsed)

			level := slog.LevelInfo
			if quietRoutes[route] {
				level = slog.LevelDebug
			}
			reqLog.Log(r.Context(), level, "request",
				"method", r.Method, "path", r.URL.Path, "status", rec.status,
				"duration_ms", float64(elapsed.Microseconds())/1000)
		}()

		next.ServeHTTP(rec, r)
	})
}
