package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// fake is a stand-in backend service whose behaviour a test can change at will.
type fake struct {
	*httptest.Server
	mu      sync.Mutex
	handler http.HandlerFunc
	calls   []call
}

type call struct {
	method, path, query string
	header              http.Header
	body                string
}

func newFake(t *testing.T, h http.HandlerFunc) *fake {
	t.Helper()
	f := &fake{handler: h}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body))) // so the handler can read it too
		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
		h := f.handler
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) set(h http.HandlerFunc) { f.mu.Lock(); f.handler = h; f.mu.Unlock() }
func (f *fake) count() int             { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *fake) last() call             { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[len(f.calls)-1] }

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func failing(w http.ResponseWriter, r *http.Request) { jsonHandler(500, `{"error":"boom"}`)(w, r) }

func hang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

const (
	locJSON  = `{"id":7,"name":"Mumbai","country":"India","latitude":19.076,"longitude":72.8777,"timezone":"Asia/Kolkata","active":true}`
	wxJSON   = `{"observationId":42,"location":{"id":7,"name":"Mumbai"},"current":{"temperatureC":39.1},"days":[{"date":"2026-05-01"}],"hourly":{"time":["t"]}}`
	predJSON = `{"locationId":7,"observationId":42,"fetchedAt":"2026-05-01T09:00:00Z","prediction":{"method":"model","peak":{"probability":0.98}}}`
	riskJSON = `{"location":{"id":7,"name":"Mumbai"},"observationId":42,"fetchedAt":"2026-05-01T09:00:00Z","assessment":{"alertLevel":"extreme-danger"}}`
	altJSON  = `{"alerts":[{"id":3,"locationId":7,"status":"open"}]}`
)

const (
	userJSON    = `{"id":2,"email":"ada@example.org","displayName":"Ada","role":"user","disabled":false,"createdAt":"2026-05-01T09:00:00Z"}`
	sessionJSON = `{"user":` + userJSON + `,"token":"fresh-token","expiresAt":"2030-01-02T03:04:05Z"}`
)

// Tokens the fake user service accepts.
const (
	adminToken = "admin-token"
	userToken  = "user-token"
)

// userService is a fake user service: it resolves the two known tokens and answers
// everything else with a plausible success.
func userService(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/readyz":
		jsonHandler(200, `{"status":"ready","service":"user","checks":{"database":"ok"}}`)(w, r)
	case "/sessions/resolve":
		var in struct{ Token string }
		json.NewDecoder(r.Body).Decode(&in)
		switch in.Token {
		case adminToken:
			jsonHandler(200, `{"userId":1,"role":"admin","email":"root@example.org"}`)(w, r)
		case userToken:
			jsonHandler(200, `{"userId":2,"role":"user","email":"ada@example.org"}`)(w, r)
		default:
			jsonHandler(401, `{"error":{"code":"invalid_session","message":"invalid or expired session"}}`)(w, r)
		}
	case "/register":
		jsonHandler(201, sessionJSON)(w, r)
	case "/login", "/me/password":
		jsonHandler(200, sessionJSON)(w, r)
	case "/me":
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		jsonHandler(200, userJSON)(w, r)
	case "/logout":
		w.WriteHeader(204)
	default:
		jsonHandler(200, `{}`)(w, r)
	}
}

// resolves counts the session lookups the user service has served.
func (f *fake) resolves() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.path == "/sessions/resolve" {
			n++
		}
	}
	return n
}

// backends is the full set of fake services.
type backends struct{ weather, processing, prediction, risk, alert, user *fake }

func healthyBackends(t *testing.T) *backends {
	t.Helper()
	ready := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(200, `{"status":"ready","service":"`+name+`","checks":{"database":"ok"}}`)(w, r)
		}
	}
	route := func(name string, data http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/readyz" {
				ready(name)(w, r)
				return
			}
			data(w, r)
		}
	}
	return &backends{
		weather:    newFake(t, route("weather", jsonHandler(200, locJSON))),
		processing: newFake(t, route("processing", jsonHandler(200, wxJSON))),
		prediction: newFake(t, route("prediction", jsonHandler(200, predJSON))),
		risk:       newFake(t, route("risk", jsonHandler(200, riskJSON))),
		alert:      newFake(t, route("alert", jsonHandler(200, altJSON))),
		user:       newFake(t, userService),
	}
}

func (b *backends) config() Config {
	return Config{
		WeatherURL: b.weather.URL, ProcessingURL: b.processing.URL, PredictionURL: b.prediction.URL,
		RiskURL: b.risk.URL, AlertURL: b.alert.URL, UserURL: b.user.URL,
		UpstreamTimeout: time.Second, ClimateTimeout: 2 * time.Second,
		BreakerFailures: 3, BreakerCooldown: time.Minute,
	}
}

// gw is a gateway under test, wired exactly as cmd/gateway wires it.
type gw struct {
	t *testing.T
	g *Gateway
	h http.Handler
}

func newGW(t *testing.T, cfg Config) *gw {
	t.Helper()
	t.Setenv("LOG_LEVEL", "error") // keep per-request access logs out of test output
	g := New(cfg, nil)
	app := httpx.New("gateway")
	app.Wrap = g.Wrap
	g.Register(app.Mux)
	app.Metrics.AddCollector(func(w io.Writer) { g.Collector(w) })
	return &gw{t: t, g: g, h: app.Handler()}
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) json() map[string]any {
	var out map[string]any
	json.Unmarshal(r.Body.Bytes(), &out)
	return out
}

func (r resp) errCode() string {
	e, _ := r.json()["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (g *gw) do(method, path, body string, headers ...string) resp {
	g.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.1:4000"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	return resp{rec}
}

func (g *gw) get(path string, headers ...string) resp { return g.do("GET", path, "", headers...) }

// admin and user call as a signed-in administrator or ordinary user (with a Bearer token).
func (g *gw) admin(method, path, body string) resp {
	return g.do(method, path, body, "Authorization", "Bearer "+adminToken)
}

func (g *gw) user(method, path, body string) resp {
	return g.do(method, path, body, "Authorization", "Bearer "+userToken)
}
