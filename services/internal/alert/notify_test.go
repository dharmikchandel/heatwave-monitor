package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// webhookServer records requests and answers with a controllable status.
type webhookServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int // 0 means 200
	got    []recorded
}

type recorded struct {
	header http.Header
	body   []byte
}

func newWebhook(t *testing.T) *webhookServer {
	t.Helper()
	w := &webhookServer{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		st := w.status
		if st == 0 || st/100 == 2 {
			w.got = append(w.got, recorded{r.Header.Clone(), body})
		}
		w.mu.Unlock()
		if st == 0 {
			st = 200
		}
		rw.WriteHeader(st)
		io.WriteString(rw, "response body")
	}))
	t.Cleanup(w.Close)
	return w
}

func (w *webhookServer) setStatus(s int) { w.mu.Lock(); w.status = s; w.mu.Unlock() }
func (w *webhookServer) received() []recorded {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]recorded(nil), w.got...)
}

func notifier(h *harness) *Notifier {
	n := NewNotifier(h.s)
	n.MaxAttempts = 4
	return n
}

func (h *harness) notification(id int64) NotificationInfo {
	h.t.Helper()
	list, err := h.s.ListNotifications(context.Background(), "", 100)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, n := range list {
		if n.ID == id {
			return n
		}
	}
	h.t.Fatalf("notification %d not found", id)
	return NotificationInfo{}
}

func TestWebhookDeliveryHeadersBodyAndSignature(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	secretSub, err := h.s.CreateSubscription(context.Background(), NewSubscription{MinLevel: "danger", Channel: ChannelWebhook, Target: srv.URL, Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	h.feed(1, "Mumbai", danger)

	if sent, err := notifier(h).DeliverOnce(context.Background()); err != nil || sent != 1 {
		t.Fatalf("DeliverOnce = %d, %v", sent, err)
	}
	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("webhook received %d requests", len(got))
	}
	r := got[0]
	if r.header.Get("Content-Type") != "application/json" || r.header.Get("X-Heatwave-Event") != "alert.opened" || r.header.Get("X-Heatwave-Delivery") == "" {
		t.Errorf("headers = %v", r.header)
	}
	if want := "sha256=" + Sign("s3cret", r.body); r.header.Get("X-Heatwave-Signature") != want {
		t.Errorf("signature %q does not verify (want %q)", r.header.Get("X-Heatwave-Signature"), want)
	}
	if Sign("other", r.body) == Sign("s3cret", r.body) {
		t.Error("signatures must depend on the secret")
	}
	var n Notification
	if err := json.Unmarshal(r.body, &n); err != nil || n.Kind != "opened" || n.Level != danger || n.Alert.LocationName != "Mumbai" ||
		n.Alert.Headline != "Danger heat alert for Mumbai" {
		t.Errorf("body = %+v, %v", n, err)
	}
	info := h.notification(n.ID)
	if info.Status != "sent" || info.Attempts != 1 || info.SentAt == nil || info.ResponseCode == nil || *info.ResponseCode != 200 {
		t.Errorf("notification state = %+v", info)
	}
	if secretSub.HasSecret != true {
		t.Error("subscription should report that it has a secret")
	}
}

func TestNoSignatureHeaderWithoutASecret(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	h.sub(nil, "danger", ChannelWebhook, srv.URL)
	h.feed(1, "Mumbai", danger)
	notifier(h).DeliverOnce(context.Background())
	if got := srv.received(); len(got) != 1 || got[0].header.Get("X-Heatwave-Signature") != "" {
		t.Errorf("got %d requests, signature %q", len(got), got[0].header.Get("X-Heatwave-Signature"))
	}
}

func TestFailedDeliveryIsRetriedWithBackoffThenSucceeds(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	srv.setStatus(503)
	h.sub(nil, "danger", ChannelWebhook, srv.URL)
	h.feed(1, "Mumbai", danger)
	n, ctx := notifier(h), context.Background()

	if sent, _ := n.DeliverOnce(ctx); sent != 0 {
		t.Fatal("a 503 counted as delivered")
	}
	info := h.notification(1)
	if info.Status != "pending" || info.Attempts != 1 || !strings.Contains(info.LastError, "503") || info.ResponseCode == nil || *info.ResponseCode != 503 {
		t.Fatalf("after the first failure: %+v", info)
	}

	srv.setStatus(0)
	if sent, _ := n.DeliverOnce(ctx); sent != 0 {
		t.Fatal("retried before the backoff elapsed")
	}
	h.advance(1500 * time.Millisecond) // first backoff is 1s
	if sent, _ := n.DeliverOnce(ctx); sent != 1 {
		t.Fatal("did not retry after the backoff")
	}
	if info := h.notification(1); info.Status != "sent" || info.Attempts != 2 {
		t.Errorf("after recovery: %+v", info)
	}
	if len(srv.received()) != 1 {
		t.Errorf("webhook received %d, want 1", len(srv.received()))
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := backoff(i+1, 30*time.Second); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestGivesUpAfterMaxAttemptsButKeepsGoingForLaterOnes(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	srv.setStatus(500)
	sub := h.sub(nil, "danger", ChannelWebhook, srv.URL)
	h.s.ResolveAfter = 0
	h.feed(1, "Mumbai", danger) // notification 1: opened
	n, ctx := notifier(h), context.Background()
	for i := 0; i < 4; i++ {
		n.DeliverOnce(ctx)
		h.advance(time.Minute)
	}
	if info := h.notification(1); info.Status != "failed" || info.Attempts != 4 {
		t.Fatalf("after %d failures: %+v, want failed", 4, info)
	}

	srv.setStatus(0)
	h.feed(1, "Mumbai", normal) // notification 2: resolved
	if sent, _ := n.DeliverOnce(ctx); sent != 1 {
		t.Fatal("a permanently failed notification blocked later ones")
	}
	if info := h.notification(2); info.Status != "sent" {
		t.Errorf("later notification: %+v", info)
	}
	_ = sub
	failed, _ := h.s.ListNotifications(ctx, "failed", 10)
	if len(failed) != 1 {
		t.Errorf("failed list = %d, want 1 (operators must be able to see it)", len(failed))
	}
}

func TestDeliveriesToOneSubscriptionStayInOrder(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	h.sub(nil, "danger", ChannelWebhook, srv.URL)
	h.feed(1, "Mumbai", danger)   // 1: opened
	h.feed(1, "Mumbai", extremeD) // 2: escalated
	n, ctx := notifier(h), context.Background()

	srv.setStatus(500)
	n.DeliverOnce(ctx)
	if info := h.notification(2); info.Attempts != 0 || info.Status != "pending" {
		t.Fatalf("the escalation was attempted while the opening was still failing: %+v", info)
	}
	srv.setStatus(0)
	h.advance(2 * time.Second)
	if sent, _ := n.DeliverOnce(ctx); sent != 2 {
		t.Fatalf("sent %d, want both once the opening got through", sent)
	}
	var order []string
	for _, r := range srv.received() {
		order = append(order, r.header.Get("X-Heatwave-Event"))
	}
	if !eq(order, []string{"alert.opened", "alert.escalated"}) {
		t.Errorf("arrival order = %v", order)
	}
}

func TestOneFailingSubscriptionDoesNotHoldUpOthers(t *testing.T) {
	h := newHarness(t)
	bad, good := newWebhook(t), newWebhook(t)
	bad.setStatus(500)
	h.sub(nil, "danger", ChannelWebhook, bad.URL)
	h.sub(nil, "danger", ChannelWebhook, good.URL)
	h.feed(1, "Mumbai", danger)
	if sent, _ := notifier(h).DeliverOnce(context.Background()); sent != 1 || len(good.received()) != 1 {
		t.Errorf("sent %d, good webhook received %d", sent, len(good.received()))
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	h := newHarness(t)
	internal := newWebhook(t) // stands in for an internal service the redirect points at
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()
	h.sub(nil, "danger", ChannelWebhook, redirector.URL)
	h.feed(1, "Mumbai", danger)
	notifier(h).DeliverOnce(context.Background())

	if len(internal.received()) != 0 {
		t.Fatal("the redirect was followed")
	}
	if info := h.notification(1); info.Status != "pending" || !strings.Contains(info.LastError, "302") {
		t.Errorf("notification = %+v; a redirect is a delivery failure", info)
	}
}

func TestSlowReceiversTimeOut(t *testing.T) {
	h := newHarness(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(400 * time.Millisecond) }))
	defer slow.Close()
	h.sub(nil, "danger", ChannelWebhook, slow.URL)
	h.feed(1, "Mumbai", danger)
	n := notifier(h)
	n.Client.Timeout = 80 * time.Millisecond
	started := time.Now()
	n.DeliverOnce(context.Background())
	if time.Since(started) > 300*time.Millisecond {
		t.Error("the delivery pass waited for a slow receiver")
	}
	if info := h.notification(1); info.Status != "pending" || info.LastError == "" {
		t.Errorf("notification = %+v", info)
	}
}

func TestUnreachableReceiverIsRetried(t *testing.T) {
	h := newHarness(t)
	h.sub(nil, "danger", ChannelWebhook, "http://127.0.0.1:1/hook")
	h.feed(1, "Mumbai", danger)
	notifier(h).DeliverOnce(context.Background())
	if info := h.notification(1); info.Status != "pending" || info.Attempts != 1 || info.ResponseCode != nil {
		t.Errorf("notification = %+v", info)
	}
}

func TestRemovedSubscriptionsAreNeverNotified(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	sub := h.sub(nil, "danger", ChannelWebhook, srv.URL)
	h.feed(1, "Mumbai", danger)
	if err := h.s.DeleteSubscription(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	notifier(h).DeliverOnce(context.Background())
	if len(srv.received()) != 0 {
		t.Error("a removed subscription still received a notification")
	}
	if info := h.notification(1); info.Status != "cancelled" {
		t.Errorf("status = %s, want cancelled", info.Status)
	}
}

func TestLogChannelWritesTheAlertToTheLog(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	h.s.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	if sent, _ := notifier(h).DeliverOnce(context.Background()); sent != 1 {
		t.Fatal("log notification not sent")
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "heat alert notification" {
			found = rec["location"] == "Mumbai" && rec["kind"] == "opened" && rec["level"] == "danger"
		}
	}
	if !found {
		t.Errorf("no alert line in the log:\n%s", buf.String())
	}
	if info := h.notification(1); info.Status != "sent" || info.ResponseCode != nil {
		t.Errorf("notification = %+v", info)
	}
}

func TestRunDeliversWhenNotifiedAndStopsOnCancel(t *testing.T) {
	h := newHarness(t)
	srv := newWebhook(t)
	h.sub(nil, "danger", ChannelWebhook, srv.URL)
	n := notifier(h)
	n.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)

	h.feed(1, "Mumbai", danger)
	n.Notify()
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.received()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(srv.received()) != 1 {
		t.Fatal("Notify did not trigger a prompt delivery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// One alert's undeliverable payload must not hold back a different alert's
// notifications, even to the same subscription.
func TestAFailingAlertDoesNotBlockOtherAlertsForTheSameSubscription(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var delivered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"locationName":"Mumbai"`)) {
			w.WriteHeader(http.StatusBadRequest) // this receiver chokes on Mumbai's payload
			return
		}
		mu.Lock()
		delivered = append(delivered, r.Header.Get("X-Heatwave-Event"))
		mu.Unlock()
	}))
	defer srv.Close()
	h.sub(nil, "danger", ChannelWebhook, srv.URL)

	h.feed(1, "Mumbai", danger)   // notification 1 (will keep failing)
	h.feed(1, "Mumbai", extremeD) // notification 2: queued behind 1, same alert
	h.feed(2, "Delhi", danger)    // notification 3: a different alert

	n := notifier(h)
	n.DeliverOnce(context.Background())
	mu.Lock()
	got := append([]string(nil), delivered...)
	mu.Unlock()
	if !eq(got, []string{"alert.opened"}) {
		t.Fatalf("delivered %v; Delhi must go out although Mumbai is failing", got)
	}
	if info := h.notification(2); info.Attempts != 0 {
		t.Errorf("Mumbai's escalation was attempted ahead of its failing opening: %+v", info)
	}
	if info := h.notification(3); info.Status != "sent" {
		t.Errorf("Delhi's notification: %+v", info)
	}
}
