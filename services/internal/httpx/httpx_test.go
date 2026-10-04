package httpx

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func serve(a *App, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRequestIDGeneratedAndPropagated(t *testing.T) {
	a := New("test")
	var seen string
	a.Mux.HandleFunc("GET /echo", func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		WriteJSON(w, 200, map[string]string{"ok": "1"})
	})

	rec := serve(a, "GET", "/echo", "", nil)
	if id := rec.Header().Get(RequestIDHeader); id == "" || id != seen {
		t.Errorf("generated id header=%q handler=%q", id, seen)
	}

	rec = serve(a, "GET", "/echo", "", map[string]string{RequestIDHeader: "abc-123"})
	if rec.Header().Get(RequestIDHeader) != "abc-123" {
		t.Error("valid inbound request ID was not reused")
	}

	rec = serve(a, "GET", "/echo", "", map[string]string{RequestIDHeader: "bad id\nwith newline"})
	if got := rec.Header().Get(RequestIDHeader); got == "" || strings.ContainsAny(got, " \n") {
		t.Errorf("unsafe inbound ID not replaced: %q", got)
	}
}

func TestErrorShapeAndNotFound(t *testing.T) {
	a := New("test")
	a.Mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusTeapot, "teapot", "short and stout")
	})
	rec := serve(a, "GET", "/boom", "", nil)
	var body ErrorBody
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusTeapot || body.Error.Code != "teapot" || body.RequestID == "" {
		t.Errorf("error response = %d %+v", rec.Code, body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	if rec := serve(a, "GET", "/missing", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route = %d", rec.Code)
	}
}

func TestPanicRecoveredAs500(t *testing.T) {
	a := New("test")
	a.Mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("oh no") })
	rec := serve(a, "GET", "/panic", "", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("panic response = %d %s", rec.Code, rec.Body)
	}
	if serve(a, "GET", "/healthz", "", nil).Code != 200 {
		t.Error("app unusable after a recovered panic")
	}
}

func TestHealthzAndReadyz(t *testing.T) {
	a := New("test")
	if rec := serve(a, "GET", "/healthz", "", nil); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := serve(a, "GET", "/readyz", "", nil); rec.Code != 200 {
		t.Fatalf("readyz with no checks = %d", rec.Code)
	}

	healthy := true
	a.AddCheck("upstream", func(context.Context) error {
		if healthy {
			return nil
		}
		return errors.New("connection refused")
	})
	if rec := serve(a, "GET", "/readyz", "", nil); rec.Code != 200 {
		t.Fatalf("readyz healthy = %d", rec.Code)
	}
	healthy = false
	rec := serve(a, "GET", "/readyz", "", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("readyz unhealthy = %d %s", rec.Code, rec.Body)
	}
}

func TestCheckDBFailsWhenClosed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	a := New("test")
	a.CheckDB(db)
	db.Close()
	if rec := serve(a, "GET", "/readyz", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz with closed db = %d, want 503", rec.Code)
	}
}

func TestMetricsCountByRoutePatternNotRawPath(t *testing.T) {
	a := New("test")
	a.Mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, nil) })
	a.Metrics.AddCollector(func(w io.Writer) { io.WriteString(w, "custom_metric 42\n") })
	serve(a, "GET", "/items/1", "", nil)
	serve(a, "GET", "/items/2", "", nil)
	serve(a, "GET", "/nope", "", nil)

	out := serve(a, "GET", "/metrics", "", nil).Body.String()
	for _, want := range []string{
		`service_up{service="test"} 1`,
		`http_requests_total{service="test",method="GET",route="GET /items/{id}",code="200"} 2`,
		`http_requests_total{service="test",method="GET",route="unmatched",code="404"} 1`,
		`custom_metric 42`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/items/1") {
		t.Error("raw path leaked into metric labels (unbounded cardinality)")
	}
}

func TestDecodeJSONStrict(t *testing.T) {
	type in struct{ Name string }
	cases := map[string]struct {
		body string
		ok   bool
	}{
		"valid":         {`{"Name":"x"}`, true},
		"unknown field": {`{"Name":"x","Extra":1}`, false},
		"trailing data": {`{"Name":"x"} {"Name":"y"}`, false},
		"malformed":     {`{"Name":`, false},
	}
	for name, c := range cases {
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		var v in
		err := DecodeJSON(rec, req, &v)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
		if !c.ok && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestClientForwardsRequestID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(RequestIDHeader)
	}))
	defer srv.Close()

	a := New("test")
	a.Mux.HandleFunc("GET /call", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", srv.URL, nil)
		resp, err := NewClient(time.Second).Do(req)
		if err == nil {
			resp.Body.Close()
		}
	})
	serve(a, "GET", "/call", "", map[string]string{RequestIDHeader: "trace-1"})
	if got != "trace-1" {
		t.Errorf("downstream saw request id %q, want trace-1", got)
	}
}

func TestListenAndServeStopsOnCancel(t *testing.T) {
	a := New("test")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ListenAndServe(ctx, "127.0.0.1:0") }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestPathID(t *testing.T) {
	a := New("test")
	a.Mux.HandleFunc("GET /x/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := PathID(w, r, "id"); ok {
			WriteJSON(w, 200, map[string]int64{"id": id})
		}
	})
	if rec := serve(a, "GET", "/x/42", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "42") {
		t.Errorf("valid id = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"abc", "0", "-1", "1.5", "99999999999999999999"} {
		if rec := serve(a, "GET", "/x/"+bad, "", nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_id") {
			t.Errorf("id %q = %d %s, want 400 invalid_id", bad, rec.Code, rec.Body)
		}
	}
}

func TestWrapDecoratesTheMuxInsideTheSharedMiddleware(t *testing.T) {
	a := New("test")
	a.Mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, map[string]string{"ok": "1"}) })
	var sawRequestID string
	a.Wrap = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawRequestID = RequestID(r.Context()) // set by the middleware outside the wrapper
			w.Header().Set("X-Wrapped", "yes")
			next.ServeHTTP(w, r)
		})
	}
	rec := serve(a, "GET", "/x", "", nil)
	if rec.Code != 200 || rec.Header().Get("X-Wrapped") != "yes" {
		t.Fatalf("wrapper not applied: %d %v", rec.Code, rec.Header())
	}
	if sawRequestID == "" || sawRequestID != rec.Header().Get(RequestIDHeader) {
		t.Errorf("the wrapper must run inside the middleware (request id %q vs header %q)", sawRequestID, rec.Header().Get(RequestIDHeader))
	}
	// The route label must still be the mux pattern, not "unmatched", despite the wrapper.
	if out := serve(a, "GET", "/metrics", "", nil).Body.String(); !strings.Contains(out, `route="GET /x"`) {
		t.Errorf("metrics lost the route pattern when a wrapper is present:\n%s", out)
	}
}
