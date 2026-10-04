package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
)

type env struct {
	t *testing.T
	h *harness
	m http.Handler
}

func newEnv(t *testing.T, n *Notifier) *env {
	t.Helper()
	h := newHarness(t)
	app := httpx.New("alert")
	(&API{Svc: h.s, Notifier: n}).Register(app.Mux)
	return &env{t: t, h: h, m: app.Handler()}
}

func (e *env) do(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	if rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func list(m map[string]any, key string) []any { l, _ := m[key].([]any); return l }

func TestSubscriptionLifecycleThroughTheAPI(t *testing.T) {
	e := newEnv(t, nil)

	status, sub := e.do("POST", "/subscriptions", `{"locationId":3,"minLevel":"danger","channel":"webhook","target":"https://example.org/hook","secret":"shh","label":"ops"}`)
	if status != http.StatusCreated || sub["hasSecret"] != true || sub["id"] == nil {
		t.Fatalf("create = %d %v", status, sub)
	}
	if _, leaked := sub["secret"]; leaked {
		t.Error("the secret must never be returned")
	}
	if sub["locationId"].(float64) != 3 || sub["minLevel"] != "danger" || sub["label"] != "ops" || sub["active"] != true {
		t.Errorf("subscription = %v", sub)
	}

	e.do("POST", "/subscriptions", `{"minLevel":"extreme-danger","channel":"log"}`)
	_, all := e.do("GET", "/subscriptions", "")
	if len(list(all, "subscriptions")) != 2 {
		t.Errorf("list = %v", all)
	}
	_, forThree := e.do("GET", "/subscriptions?locationId=3", "")
	_, forNine := e.do("GET", "/subscriptions?locationId=9", "")
	if len(list(forThree, "subscriptions")) != 2 || len(list(forNine, "subscriptions")) != 1 {
		t.Errorf("per-location listing wrong: %d for 3 (own + global), %d for 9 (global only)", len(list(forThree, "subscriptions")), len(list(forNine, "subscriptions")))
	}

	if status, _ := e.do("DELETE", "/subscriptions/1", ""); status != http.StatusNoContent {
		t.Errorf("delete = %d", status)
	}
	if status, resp := e.do("DELETE", "/subscriptions/1", ""); status != 404 || errCode(resp) != "not_found" {
		t.Errorf("second delete = %d %v", status, resp)
	}
	_, after := e.do("GET", "/subscriptions", "")
	if len(list(after, "subscriptions")) != 1 {
		t.Errorf("removed subscription still listed: %v", after)
	}
}

func TestSubscriptionValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.h.s.AllowedHosts = []string{"hooks.example.org"}
	cases := map[string]string{
		"unknown level":             `{"minLevel":"scorching","channel":"log"}`,
		"below the alert level":     `{"minLevel":"caution","channel":"log"}`,
		"unknown channel":           `{"minLevel":"danger","channel":"sms"}`,
		"webhook without target":    `{"minLevel":"danger","channel":"webhook"}`,
		"target is not a URL":       `{"minLevel":"danger","channel":"webhook","target":"not a url"}`,
		"non-http scheme":           `{"minLevel":"danger","channel":"webhook","target":"file:///etc/passwd"}`,
		"credentials in the URL":    `{"minLevel":"danger","channel":"webhook","target":"https://user:pw@hooks.example.org/x"}`,
		"host not allowed":          `{"minLevel":"danger","channel":"webhook","target":"http://169.254.169.254/latest"}`,
		"log channel with a target": `{"minLevel":"danger","channel":"log","target":"https://hooks.example.org"}`,
		"log channel with a secret": `{"minLevel":"danger","channel":"log","secret":"x"}`,
		"bad location":              `{"minLevel":"danger","channel":"log","locationId":0}`,
		"label too long":            `{"minLevel":"danger","channel":"log","label":"` + strings.Repeat("x", 101) + `"}`,
		"unknown field":             `{"minLevel":"danger","channel":"log","priority":1}`,
		"wrong type":                `{"minLevel":5,"channel":"log"}`,
		"not json":                  `{`,
		"URL too long":              `{"minLevel":"danger","channel":"webhook","target":"https://hooks.example.org/` + strings.Repeat("a", 500) + `"}`,
	}
	for name, body := range cases {
		status, resp := e.do("POST", "/subscriptions", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d (%v), want 400", name, status, resp)
		}
	}
	if status, _ := e.do("POST", "/subscriptions", `{"minLevel":"danger","channel":"webhook","target":"https://hooks.example.org/ok"}`); status != http.StatusCreated {
		t.Errorf("an allowed host was rejected: %d", status)
	}
	if n := e.h.count(`SELECT COUNT(*) FROM subscriptions`); n != 1 {
		t.Errorf("%d subscriptions stored, want only the valid one", n)
	}

	_, resp := e.do("POST", "/subscriptions", `{"minLevel":"caution","channel":"log"}`)
	if msg := resp["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "danger") {
		t.Errorf("message should say which level is required: %q", msg)
	}
}

func TestSubscriptionLimit(t *testing.T) {
	e := newEnv(t, nil)
	e.h.s.MaxSubscriptions = 2
	for i := 0; i < 2; i++ {
		if status, _ := e.do("POST", "/subscriptions", `{"minLevel":"danger","channel":"log"}`); status != 201 {
			t.Fatalf("create #%d = %d", i, status)
		}
	}
	if status, resp := e.do("POST", "/subscriptions", `{"minLevel":"danger","channel":"log"}`); status != 409 || errCode(resp) != "subscription_limit" {
		t.Errorf("over the limit = %d %v", status, resp)
	}
	e.do("DELETE", "/subscriptions/1", "")
	if status, _ := e.do("POST", "/subscriptions", `{"minLevel":"danger","channel":"log"}`); status != 201 {
		t.Errorf("removal did not free a slot: %d", status)
	}
}

func TestAlertsListDetailAndAcknowledge(t *testing.T) {
	e := newEnv(t, nil)
	e.h.s.ResolveAfter = 0
	e.h.logSub("danger")
	e.h.feed(1, "Mumbai", danger)
	e.h.feed(1, "Mumbai", extremeD)
	e.h.feed(2, "Delhi", danger)
	e.h.feed(2, "Delhi", normal) // Delhi resolved

	_, all := e.do("GET", "/alerts", "")
	if len(list(all, "alerts")) != 2 {
		t.Fatalf("alerts = %v", all)
	}
	_, open := e.do("GET", "/alerts?status=open", "")
	_, resolved := e.do("GET", "/alerts?status=resolved", "")
	_, mumbai := e.do("GET", "/alerts?locationId=1", "")
	_, limited := e.do("GET", "/alerts?limit=1", "")
	if len(list(open, "alerts")) != 1 || len(list(resolved, "alerts")) != 1 || len(list(mumbai, "alerts")) != 1 || len(list(limited, "alerts")) != 1 {
		t.Errorf("filters: open %d, resolved %d, mumbai %d, limit %d", len(list(open, "alerts")), len(list(resolved, "alerts")), len(list(mumbai, "alerts")), len(list(limited, "alerts")))
	}
	first := list(open, "alerts")[0].(map[string]any)
	if first["locationName"] != "Mumbai" || first["currentLevel"] != "extreme-danger" || first["peakLevel"] != "extreme-danger" || first["status"] != "open" {
		t.Errorf("open alert = %v", first)
	}

	status, d := e.do("GET", "/alerts/1", "")
	if status != 200 || len(list(d, "history")) != 2 || len(list(d, "notifications")) != 2 {
		t.Errorf("detail = %d history %d notifications %d", status, len(list(d, "history")), len(list(d, "notifications")))
	}
	if n := list(d, "notifications")[0].(map[string]any); n["kind"] != "opened" || n["status"] != "pending" || n["channel"] != "log" {
		t.Errorf("notification = %v", n)
	}

	status, acked := e.do("POST", "/alerts/1/ack", "")
	if status != 200 || acked["acknowledgedAt"] == nil {
		t.Fatalf("ack = %d %v", status, acked)
	}
	first1 := acked["acknowledgedAt"]
	e.h.advance(time.Hour)
	if _, again := e.do("POST", "/alerts/1/ack", ""); again["acknowledgedAt"] != first1 {
		t.Error("acknowledging twice must keep the first time")
	}
	if status, _ := e.do("POST", "/alerts/2/ack", ""); status != 200 {
		t.Error("a resolved alert can be acknowledged too")
	}
}

func TestAlertEndpointValidation(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{"/alerts/abc", "/alerts/0", "/alerts/-1"} {
		if status, resp := e.do("GET", path, ""); status != 400 || errCode(resp) != "invalid_id" {
			t.Errorf("GET %s = %d %v", path, status, resp)
		}
	}
	for _, c := range []struct{ method, path string }{{"GET", "/alerts/99"}, {"POST", "/alerts/99/ack"}} {
		if status, resp := e.do(c.method, c.path, ""); status != 404 || errCode(resp) != "not_found" {
			t.Errorf("%s %s = %d %v", c.method, c.path, status, resp)
		}
	}
	for _, q := range []string{"status=bogus", "limit=0", "limit=201", "limit=x", "locationId=0", "locationId=abc"} {
		if status, _ := e.do("GET", "/alerts?"+q, ""); status != 400 {
			t.Errorf("?%s = %d, want 400", q, status)
		}
	}
	for _, q := range []string{"status=bogus", "limit=0", "limit=501"} {
		if status, _ := e.do("GET", "/notifications?"+q, ""); status != 400 {
			t.Errorf("/notifications?%s = %d, want 400", q, status)
		}
	}
	if status, _ := e.do("GET", "/subscriptions?locationId=-1", ""); status != 400 {
		t.Errorf("subscriptions locationId=-1 = %d", status)
	}
}

func TestEventEndpointAndRejections(t *testing.T) {
	e := newEnv(t, nil)
	e.h.logSub("danger")
	ev := e.h.event(1, "Mumbai", danger)
	body, _ := json.Marshal(ev)
	if status, resp := e.do("POST", "/internal/events", string(body)); status != http.StatusAccepted || resp["duplicate"] != false {
		t.Fatalf("event = %d %v", status, resp)
	}
	if status, resp := e.do("POST", "/internal/events", string(body)); status != http.StatusAccepted || resp["duplicate"] != true {
		t.Errorf("redelivery = %d %v", status, resp)
	}
	if n := len(e.h.alerts()); n != 1 {
		t.Errorf("alerts = %d", n)
	}

	for name, b := range map[string]string{"not json": `{`, "no id": `{"type":"risk.assessed","payload":{}}`, "no payload": `{"id":"x","type":"risk.assessed"}`} {
		if status, _ := e.do("POST", "/internal/events", b); status != 400 {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}
	bad, _ := json.Marshal(events.Event{ID: "bad", Type: "risk.assessed", Payload: json.RawMessage(`"nope"`)})
	e.do("POST", "/internal/events", string(bad))
	status, resp := e.do("GET", "/rejections?limit=1", "")
	if rej := list(resp, "rejections"); status != 200 || len(rej) != 1 || rej[0].(map[string]any)["reason"] != ReasonUndecoded {
		t.Errorf("rejections = %d %v", status, resp)
	}
	if status, _ := e.do("GET", "/rejections?limit=0", ""); status != 400 {
		t.Errorf("limit=0 = %d", status)
	}
}

func TestNotificationsEndpointShowsDeliveryState(t *testing.T) {
	e := newEnv(t, nil)
	srv := newWebhook(t)
	srv.setStatus(500)
	e.h.sub(nil, "danger", ChannelWebhook, srv.URL)
	e.h.feed(1, "Mumbai", danger)
	n := notifier(e.h)
	n.DeliverOnce(context.Background())

	_, pending := e.do("GET", "/notifications?status=pending", "")
	_, sent := e.do("GET", "/notifications?status=sent", "")
	if len(list(pending, "notifications")) != 1 || len(list(sent, "notifications")) != 0 {
		t.Errorf("pending %v sent %v", pending, sent)
	}
	if p := list(pending, "notifications")[0].(map[string]any); p["attempts"].(float64) != 1 || !strings.Contains(p["lastError"].(string), "500") {
		t.Errorf("pending notification = %v", p)
	}
}

func TestAnAssessmentReachesTheWebhookPromptly(t *testing.T) {
	srv := newWebhook(t)
	n := NewNotifier(nil)
	e := newEnv(t, n)
	n.Svc = e.h.s
	n.Interval = time.Hour // only a wake-up after commit can make this fast
	e.h.sub(nil, "danger", ChannelWebhook, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	body, _ := json.Marshal(e.h.event(1, "Mumbai", danger))
	if status, _ := e.do("POST", "/internal/events", string(body)); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.received()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := srv.received()
	if len(got) != 1 || got[0].header.Get("X-Heatwave-Event") != "alert.opened" {
		t.Fatalf("webhook received %d requests; the notifier (interval 1h) must be woken after commit", len(got))
	}
	var note Notification
	if err := json.Unmarshal(got[0].body, &note); err != nil || note.Alert.LocationName != "Mumbai" {
		t.Errorf("payload = %+v, %v", note, err)
	}
}
