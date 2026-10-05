package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

var bg = context.Background()

func follow(t *testing.T, h *harness, user int64, loc *int64, level string) Subscription {
	t.Helper()
	sub, err := h.s.CreateSubscription(bg, NewSubscription{LocationID: loc, MinLevel: level, Channel: ChannelInApp, UserID: &user})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestInAppNotificationsAreDeliveredToTheFollowersInbox(t *testing.T) {
	h := newHarness(t)
	follow(t, h, 1, i64(7), "danger")
	follow(t, h, 2, i64(7), "extreme-danger") // wants only the worst
	follow(t, h, 3, i64(8), "danger")         // follows another city

	h.feed(7, "Delhi", engine.Danger)

	box, err := h.s.Inbox(bg, 1, 10)
	if err != nil || box.Unread != 1 || len(box.Notifications) != 1 {
		t.Fatalf("inbox 1 = %+v, %v", box, err)
	}
	it := box.Notifications[0]
	if it.Kind != "opened" || it.Level != engine.Danger || it.LocationID != 7 || it.LocationName != "Delhi" || it.Headline == "" || it.ReadAt != nil || it.AlertID == 0 {
		t.Errorf("item = %+v", it)
	}
	for _, other := range []int64{2, 3, 99} {
		if b, _ := h.s.Inbox(bg, other, 10); len(b.Notifications) != 0 || b.Unread != 0 {
			t.Errorf("user %d was told: %+v", other, b)
		}
	}
	// Nothing is left for the notifier to deliver: an in-app notification is sent on creation.
	if n := h.count(`SELECT COUNT(*) FROM notifications WHERE status = 'pending'`); n != 0 {
		t.Errorf("%d in-app notifications are pending", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM notifications WHERE status = 'sent' AND sent_at IS NOT NULL AND channel = 'inapp'`); n != 1 {
		t.Errorf("sent in-app notifications = %d, want 1", n)
	}

	h.feed(7, "Delhi", engine.ExtremeDanger)
	if b, _ := h.s.Inbox(bg, 2, 10); len(b.Notifications) != 1 || b.Notifications[0].Level != engine.ExtremeDanger {
		t.Errorf("escalation inbox 2 = %+v", b)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); len(b.Notifications) != 2 || b.Notifications[0].Kind != "escalated" {
		t.Errorf("newest first? inbox 1 = %+v", b)
	}
}

func TestEveryLocationSubscriptionAndResolution(t *testing.T) {
	h := newHarness(t)
	follow(t, h, 1, nil, "danger")
	h.feed(7, "Delhi", engine.Danger)
	h.feed(8, "Pune", engine.Danger)
	h.feed(7, "Delhi", engine.Normal)
	h.advance(2 * time.Hour)
	h.feed(7, "Delhi", engine.Normal) // below long enough: resolves

	box, _ := h.s.Inbox(bg, 1, 10)
	got := []string{}
	for _, it := range box.Notifications {
		got = append(got, it.LocationName+":"+it.Kind)
	}
	want := []string{"Delhi:resolved", "Pune:opened", "Delhi:opened"}
	if !eq(got, want) {
		t.Errorf("inbox = %v, want %v", got, want)
	}
}

func TestReadStateIsPerNotificationAndPerUser(t *testing.T) {
	h := newHarness(t)
	follow(t, h, 1, i64(7), "danger")
	follow(t, h, 2, i64(7), "danger")
	h.feed(7, "Delhi", engine.Danger)
	h.feed(7, "Delhi", engine.ExtremeDanger)

	one, _ := h.s.Inbox(bg, 1, 10)
	first := one.Notifications[1].ID
	if err := h.s.MarkRead(bg, 1, first); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); b.Unread != 1 || b.Notifications[1].ReadAt == nil || b.Notifications[0].ReadAt != nil {
		t.Errorf("after reading one: %+v", b)
	}
	readAt := func() *time.Time { b, _ := h.s.Inbox(bg, 1, 10); return b.Notifications[1].ReadAt }
	before := *readAt()
	h.advance(time.Hour)
	if err := h.s.MarkRead(bg, 1, first); err != nil || !readAt().Equal(before) {
		t.Errorf("reading twice must keep the first time: %v", err)
	}
	if b, _ := h.s.Inbox(bg, 2, 10); b.Unread != 2 {
		t.Errorf("user 2's unread = %d; reading user 1's copy must not affect it", b.Unread)
	}
}

func TestMarkReadRejectsSomeoneElsesNotification(t *testing.T) {
	h := newHarness(t)
	follow(t, h, 1, i64(7), "danger")
	h.feed(7, "Delhi", engine.Danger)
	box, _ := h.s.Inbox(bg, 1, 10)
	id := box.Notifications[0].ID

	if err := h.s.MarkRead(bg, 2, id); err != ErrNotFound {
		t.Errorf("another user marking it read: %v, want ErrNotFound", err)
	}
	if err := h.s.MarkRead(bg, 1, 99999); err != ErrNotFound {
		t.Errorf("unknown id: %v", err)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); b.Unread != 1 {
		t.Error("a rejected call changed read state")
	}
	if err := h.s.MarkAllRead(bg, 2); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); b.Unread != 1 {
		t.Error("read-all by another user marked this inbox read")
	}
	if err := h.s.MarkAllRead(bg, 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); b.Unread != 0 {
		t.Errorf("unread after read-all = %d", b.Unread)
	}
}

func TestFollowingTwiceUpdatesTheThreshold(t *testing.T) {
	h := newHarness(t)
	a := follow(t, h, 1, i64(7), "danger")
	b := follow(t, h, 1, i64(7), "extreme-danger")
	if a.ID != b.ID || b.MinLevel != engine.ExtremeDanger {
		t.Errorf("a = %+v, b = %+v; want the same subscription with the new level", a, b)
	}
	allA := follow(t, h, 1, nil, "danger")
	allB := follow(t, h, 1, nil, "danger")
	if allA.ID != allB.ID || allA.ID == a.ID {
		t.Errorf("'every location' subscription = %d and %d (location one is %d)", allA.ID, allB.ID, a.ID)
	}
	if subs, _ := h.s.UserSubscriptions(bg, 1); len(subs) != 2 {
		t.Errorf("%d subscriptions, want 2", len(subs))
	}
	if n := h.count(`SELECT COUNT(*) FROM subscriptions WHERE user_id = 1`); n != 2 {
		t.Errorf("%d rows", n)
	}
}

func TestUserSubscriptionLimitAndValidation(t *testing.T) {
	h := newHarness(t)
	h.s.MaxUserSubs = 2
	follow(t, h, 1, i64(1), "danger")
	follow(t, h, 1, i64(2), "danger")
	if _, err := h.s.CreateSubscription(bg, NewSubscription{LocationID: i64(3), MinLevel: "danger", Channel: ChannelInApp, UserID: i64(1)}); err != ErrTooManySubs {
		t.Errorf("third subscription: %v", err)
	}
	follow(t, h, 1, i64(2), "extreme-danger") // changing an existing one is not "another"
	follow(t, h, 2, i64(3), "danger")         // the limit is per user

	bad := map[string]NewSubscription{
		"below the open level":    {MinLevel: "caution", Channel: ChannelInApp, UserID: i64(5)},
		"in-app with a target":    {MinLevel: "danger", Channel: ChannelInApp, UserID: i64(5), Target: "https://example.org"},
		"in-app without a user":   {MinLevel: "danger", Channel: ChannelInApp},
		"webhook owned by a user": {MinLevel: "danger", Channel: ChannelWebhook, Target: "https://example.org/h", UserID: i64(5)},
		"log owned by a user":     {MinLevel: "danger", Channel: ChannelLog, UserID: i64(5)},
		"bad location":            {MinLevel: "danger", Channel: ChannelInApp, UserID: i64(5), LocationID: i64(-1)},
	}
	for name, in := range bad {
		if _, err := h.s.CreateSubscription(bg, in); err == nil || !strings.Contains(err.Error(), "validation") {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestOperatorLimitIgnoresUserSubscriptions(t *testing.T) {
	h := newHarness(t)
	h.s.MaxSubscriptions = 1
	for u := int64(1); u <= 3; u++ {
		follow(t, h, u, i64(1), "danger")
	}
	h.logSub("danger")
	if _, err := h.s.CreateSubscription(bg, NewSubscription{MinLevel: "danger", Channel: ChannelLog}); err != ErrTooManySubs {
		t.Errorf("second operator subscription: %v", err)
	}
}

func TestOperatorViewsNeverExposeUserSubscriptions(t *testing.T) {
	h := newHarness(t)
	mine := follow(t, h, 1, i64(7), "danger")
	op := h.logSub("danger")

	list, _ := h.s.ListSubscriptions(bg, nil)
	if len(list) != 1 || list[0].ID != op.ID {
		t.Errorf("operator list = %+v", list)
	}
	if err := h.s.DeleteSubscription(bg, mine.ID); err != ErrNotFound {
		t.Errorf("an operator removing a user's subscription: %v, want ErrNotFound", err)
	}
	if subs, _ := h.s.UserSubscriptions(bg, 1); len(subs) != 1 {
		t.Error("the user's subscription was removed by the operator endpoint")
	}
	// ...while matching still sees both.
	h.feed(7, "Delhi", engine.Danger)
	if got := h.told(mine.ID); !eq(got, []string{"opened:danger"}) {
		t.Errorf("user told %v", got)
	}
	if got := h.told(op.ID); !eq(got, []string{"opened:danger"}) {
		t.Errorf("operator told %v", got)
	}
}

func TestUnfollowKeepsHistoryAndStopsNewNotifications(t *testing.T) {
	h := newHarness(t)
	sub := follow(t, h, 1, i64(7), "danger")
	h.feed(7, "Delhi", engine.Danger)

	if err := h.s.DeleteUserSubscription(bg, 2, sub.ID); err != ErrNotFound {
		t.Errorf("another user unfollowing: %v", err)
	}
	if err := h.s.DeleteUserSubscription(bg, 1, sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DeleteUserSubscription(bg, 1, sub.ID); err != ErrNotFound {
		t.Errorf("unfollowing twice: %v", err)
	}
	h.feed(7, "Delhi", engine.ExtremeDanger)
	box, _ := h.s.Inbox(bg, 1, 10)
	if len(box.Notifications) != 1 {
		t.Errorf("inbox = %d items; want the old one kept and no new one", len(box.Notifications))
	}
	// Following again after unfollowing works (the uniqueness only covers active ones).
	if again := follow(t, h, 1, i64(7), "danger"); again.ID == sub.ID {
		t.Error("a deactivated subscription was resurrected")
	}
}

func TestForgetUserRemovesTheirDataOnly(t *testing.T) {
	h := newHarness(t)
	follow(t, h, 1, i64(7), "danger")
	other := follow(t, h, 2, i64(7), "danger")
	op := h.logSub("danger")
	h.feed(7, "Delhi", engine.Danger)

	if err := h.s.ForgetUser(bg, 1); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT COUNT(*) FROM subscriptions WHERE user_id = 1`); n != 0 {
		t.Errorf("%d subscriptions remain", n)
	}
	if b, _ := h.s.Inbox(bg, 1, 10); len(b.Notifications) != 0 || b.Unread != 0 {
		t.Errorf("inbox survived: %+v", b)
	}
	if b, _ := h.s.Inbox(bg, 2, 10); len(b.Notifications) != 1 {
		t.Errorf("another user's inbox was damaged: %+v", b)
	}
	if got := h.told(other.ID); len(got) != 1 {
		t.Errorf("other user's notifications = %v", got)
	}
	if got := h.told(op.ID); len(got) != 1 {
		t.Errorf("operator notifications = %v", got)
	}
	if err := h.s.ForgetUser(bg, 1); err != nil {
		t.Errorf("forgetting twice: %v", err)
	}
}

// ---- HTTP ----

type userEnv struct{ *env }

func (e userEnv) as(user int64, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != 0 {
		req.Header.Set(httpx.HeaderUserID, strconv.FormatInt(user, 10))
	}
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		decode(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestUserAPIRequiresTheTrustedHeader(t *testing.T) {
	e := userEnv{newEnv(t, nil)}
	for _, r := range [][2]string{
		{"GET", "/my/subscriptions"}, {"POST", "/my/subscriptions"}, {"DELETE", "/my/subscriptions/1"},
		{"GET", "/my/notifications"}, {"POST", "/my/notifications/read"}, {"POST", "/my/notifications/1/read"},
	} {
		if code, out := e.as(0, r[0], r[1], `{"minLevel":"danger"}`); code != http.StatusUnauthorized || errCode(out) != "unauthenticated" {
			t.Errorf("%s %s = %d %v", r[0], r[1], code, out)
		}
	}
}

func TestUserAPIFlow(t *testing.T) {
	e := userEnv{newEnv(t, nil)}
	code, sub := e.as(1, "POST", "/my/subscriptions", `{"locationId":7,"minLevel":"danger"}`)
	if code != http.StatusOK || sub["channel"] != "inapp" || sub["locationId"] != float64(7) || sub["id"] == nil {
		t.Fatalf("follow = %d %v", code, sub)
	}
	// A user cannot pick another channel or smuggle in a target: unknown fields are refused.
	if code, _ = e.as(1, "POST", "/my/subscriptions", `{"locationId":8,"minLevel":"danger","channel":"webhook","target":"https://example.org"}`); code != http.StatusBadRequest {
		t.Errorf("choosing a channel = %d, want 400", code)
	}
	if code, out := e.as(1, "POST", "/my/subscriptions", `{"locationId":8,"minLevel":"normal"}`); code != http.StatusBadRequest || errCode(out) != "validation_failed" {
		t.Errorf("bad level = %d %v", code, out)
	}
	if _, out := e.as(1, "GET", "/my/subscriptions", ""); len(list(out, "subscriptions")) != 1 {
		t.Errorf("list = %v", out)
	}
	if _, out := e.as(2, "GET", "/my/subscriptions", ""); len(list(out, "subscriptions")) != 0 {
		t.Errorf("another user's list = %v", out)
	}

	e.h.feed(7, "Delhi", engine.Danger)
	code, box := e.as(1, "GET", "/my/notifications?limit=5", "")
	items := list(box, "notifications")
	if code != http.StatusOK || box["unread"] != float64(1) || len(items) != 1 {
		t.Fatalf("inbox = %d %v", code, box)
	}
	id := strconv.Itoa(int(items[0].(map[string]any)["id"].(float64)))
	if code, _ = e.as(2, "POST", "/my/notifications/"+id+"/read", ""); code != http.StatusNotFound {
		t.Errorf("someone else reading it = %d", code)
	}
	if code, _ = e.as(1, "POST", "/my/notifications/"+id+"/read", ""); code != http.StatusNoContent {
		t.Errorf("read = %d", code)
	}
	if _, box = e.as(1, "GET", "/my/notifications", ""); box["unread"] != float64(0) {
		t.Errorf("unread = %v", box["unread"])
	}
	if code, _ = e.as(1, "GET", "/my/notifications?limit=0", ""); code != http.StatusBadRequest {
		t.Errorf("limit 0 = %d", code)
	}
	if code, _ = e.as(1, "POST", "/my/notifications/read", ""); code != http.StatusNoContent {
		t.Errorf("read all = %d", code)
	}

	subID := strconv.Itoa(int(sub["id"].(float64)))
	if code, _ = e.as(2, "DELETE", "/my/subscriptions/"+subID, ""); code != http.StatusNotFound {
		t.Errorf("another user unfollowing = %d", code)
	}
	if code, _ = e.as(1, "DELETE", "/my/subscriptions/"+subID, ""); code != http.StatusNoContent {
		t.Errorf("unfollow = %d", code)
	}
}

func TestOperatorAPICannotCreateInAppSubscriptions(t *testing.T) {
	e := newEnv(t, nil)
	if code, out := e.do("POST", "/subscriptions", `{"minLevel":"danger","channel":"inapp"}`); code != http.StatusBadRequest || errCode(out) != "validation_failed" {
		t.Errorf("= %d %v", code, out)
	}
}

func TestForgetUserEndpoint(t *testing.T) {
	e := userEnv{newEnv(t, nil)}
	e.as(1, "POST", "/my/subscriptions", `{"locationId":7,"minLevel":"danger"}`)
	e.h.feed(7, "Delhi", engine.Danger)
	if code, _ := e.do("DELETE", "/internal/users/1", ""); code != http.StatusNoContent {
		t.Fatalf("= %d", code)
	}
	if _, out := e.as(1, "GET", "/my/notifications", ""); len(list(out, "notifications")) != 0 || out["unread"] != float64(0) {
		t.Errorf("inbox = %v", out)
	}
	if code, _ := e.do("DELETE", "/internal/users/abc", ""); code != http.StatusBadRequest {
		t.Errorf("bad id = %d", code)
	}
}
