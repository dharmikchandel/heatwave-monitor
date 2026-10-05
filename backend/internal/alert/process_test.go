package alert

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/events"
)

const (
	normal   = engine.Normal
	caution  = engine.Caution
	extremeC = engine.ExtremeCaution
	danger   = engine.Danger
	extremeD = engine.ExtremeDanger
)

func TestNothingBelowTheOpenLevelOpensAnAlert(t *testing.T) {
	h := newHarness(t)
	h.logSub("danger")
	for _, l := range []engine.RiskLevel{normal, caution, extremeC} {
		h.feed(1, "Mumbai", l)
	}
	if len(h.alerts()) != 0 || h.count(`SELECT COUNT(*) FROM notifications`) != 0 {
		t.Error("an alert or notification appeared below the open level")
	}
}

func TestOpensOnceAndRepeatsDoNotNotifyAgain(t *testing.T) {
	h := newHarness(t)
	sub := h.logSub("danger")
	for i := 0; i < 6; i++ {
		h.feed(1, "Mumbai", danger)
	}
	alerts := h.alerts()
	if len(alerts) != 1 || alerts[0].Status != "open" || alerts[0].CurrentLevel != danger {
		t.Fatalf("alerts = %+v", alerts)
	}
	if !eq(h.told(sub.ID), []string{"opened:danger"}) {
		t.Errorf("told = %v; six identical updates must produce one notification", h.told(sub.ID))
	}
	d := h.detail(alerts[0].ID)
	if !eq(kinds(d.History), []string{"opened"}) {
		t.Errorf("history = %v", kinds(d.History))
	}
	if !alerts[0].UpdatedAt.After(alerts[0].OpenedAt) {
		t.Error("the alert's updatedAt should follow the latest assessment")
	}
	if alerts[0].Headline != "Danger heat alert for Mumbai" || !strings.Contains(alerts[0].Summary, "Forecast looks hot.") {
		t.Errorf("headline/summary = %q / %q", alerts[0].Headline, alerts[0].Summary)
	}
	if alerts[0].Details.Peak.Date != "2026-05-05" || len(alerts[0].Details.WarningDays) != 2 || alerts[0].Details.Method != "model" {
		t.Errorf("details = %+v", alerts[0].Details)
	}
}

func TestEscalationNotifiesOnlyWhoNeedsToKnow(t *testing.T) {
	h := newHarness(t)
	all := h.logSub("danger")
	severe := h.logSub("extreme-danger")

	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", extremeD)
	h.feed(1, "Mumbai", danger)   // cooling: nobody is told
	h.feed(1, "Mumbai", extremeD) // back to a level everyone was already told about: nobody is told

	if !eq(h.told(all.ID), []string{"opened:danger", "escalated:extreme-danger"}) {
		t.Errorf("min-danger subscriber told %v", h.told(all.ID))
	}
	if !eq(h.told(severe.ID), []string{"opened:extreme-danger"}) {
		t.Errorf("min-extreme-danger subscriber told %v; its first message is an 'opened', and only at its own level", h.told(severe.ID))
	}
	a := h.alerts()[0]
	if a.PeakLevel != extremeD || a.CurrentLevel != extremeD {
		t.Errorf("alert = %+v", a)
	}
	want := []string{"opened", "escalated", "deescalated", "escalated"}
	if got := kinds(h.detail(a.ID).History); !eq(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
}

func TestResolutionWaitsOutTheHysteresisWindow(t *testing.T) {
	h := newHarness(t)
	sub := h.logSub("danger")
	h.feed(1, "Mumbai", danger)

	h.feed(1, "Mumbai", normal) // drops below: noted, but the alert stays open
	a := h.alerts()[0]
	if a.Status != "open" || a.CurrentLevel != normal || a.belowSince == nil {
		t.Fatalf("alert after the first low reading = %+v", a)
	}
	h.advance(30 * time.Minute)
	h.feed(1, "Mumbai", normal)
	if h.alerts()[0].Status != "open" {
		t.Fatal("resolved before the window elapsed")
	}

	h.advance(31 * time.Minute)
	h.feed(1, "Mumbai", normal)
	a = h.alerts()[0]
	if a.Status != "resolved" || a.ResolvedAt == nil {
		t.Fatalf("alert = %+v, want resolved", a)
	}
	if !eq(h.told(sub.ID), []string{"opened:danger", "resolved:normal"}) {
		t.Errorf("told = %v", h.told(sub.ID))
	}
	if got := kinds(h.detail(a.ID).History); !eq(got, []string{"opened", "deescalated", "resolved"}) {
		t.Errorf("history = %v", got)
	}
	if h.count(`SELECT COUNT(*) FROM alerts WHERE status = 'open'`) != 0 {
		t.Error("an open alert remains")
	}
}

func TestAFlickeringForecastDoesNotResolveTheAlert(t *testing.T) {
	h := newHarness(t)
	sub := h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	for i := 0; i < 5; i++ { // 20 minutes low, then back up, repeatedly: never a full hour below
		h.feed(1, "Mumbai", normal)
		h.advance(19 * time.Minute)
		h.feed(1, "Mumbai", normal)
		h.feed(1, "Mumbai", danger)
	}
	a := h.alerts()[0]
	if a.Status != "open" || len(h.alerts()) != 1 || a.belowSince != nil {
		t.Errorf("alert = %+v (count %d)", a, len(h.alerts()))
	}
	if !eq(h.told(sub.ID), []string{"opened:danger"}) {
		t.Errorf("flicker produced notifications: %v", h.told(sub.ID))
	}
}

func TestResolveAfterZeroResolvesAtOnce(t *testing.T) {
	h := newHarness(t)
	h.s.ResolveAfter = 0
	h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", caution)
	if a := h.alerts()[0]; a.Status != "resolved" {
		t.Errorf("status = %s", a.Status)
	}
}

func TestOnlySubscriptionsAlreadyToldGetAnAllClear(t *testing.T) {
	h := newHarness(t)
	told := h.logSub("danger")
	never := h.logSub("extreme-danger")
	h.s.ResolveAfter = 0
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", normal)

	if !eq(h.told(told.ID), []string{"opened:danger", "resolved:normal"}) {
		t.Errorf("told subscriber got %v", h.told(told.ID))
	}
	if len(h.told(never.ID)) != 0 {
		t.Errorf("a subscriber who never heard about the alert got %v", h.told(never.ID))
	}
}

func TestReopensTheSameAlertWithinTheCooldown(t *testing.T) {
	h := newHarness(t)
	h.s.ResolveAfter = 0
	sub := h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	first := h.alerts()[0]
	if _, err := h.s.Acknowledge(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	h.feed(1, "Mumbai", normal)
	h.advance(30 * time.Minute)
	h.feed(1, "Mumbai", danger)

	alerts := h.alerts()
	if len(alerts) != 1 || alerts[0].ID != first.ID || alerts[0].Status != "open" || alerts[0].ReopenCount != 1 {
		t.Fatalf("alerts = %+v, want the same alert reopened", alerts)
	}
	if alerts[0].AcknowledgedAt != nil || alerts[0].ResolvedAt != nil {
		t.Error("a reopened alert needs attention again: acknowledgement and resolution time must be cleared")
	}
	if !eq(kinds(h.detail(first.ID).History), []string{"opened", "resolved", "reopened"}) {
		t.Errorf("history = %v", kinds(h.detail(first.ID).History))
	}
	// Already told about danger for this alert: a reopen at the same level is not news.
	if !eq(h.told(sub.ID), []string{"opened:danger", "resolved:normal"}) {
		t.Errorf("told = %v", h.told(sub.ID))
	}

	// But a reopen at a worse level is.
	h.feed(1, "Mumbai", extremeD)
	if !eq(h.told(sub.ID), []string{"opened:danger", "resolved:normal", "escalated:extreme-danger"}) {
		t.Errorf("told = %v", h.told(sub.ID))
	}
}

func TestANewAlertStartsAfterTheCooldown(t *testing.T) {
	h := newHarness(t)
	h.s.ResolveAfter = 0
	sub := h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", normal)
	h.advance(3 * time.Hour)
	h.feed(1, "Mumbai", danger)

	alerts := h.alerts()
	if len(alerts) != 2 || alerts[0].ID == alerts[1].ID || alerts[0].Status != "open" || alerts[1].Status != "resolved" {
		t.Fatalf("alerts = %+v, want a fresh alert after the old one", alerts)
	}
	if !eq(h.told(sub.ID), []string{"opened:danger", "resolved:normal", "opened:danger"}) {
		t.Errorf("told = %v", h.told(sub.ID))
	}
}

func TestCooldownZeroNeverReopens(t *testing.T) {
	h := newHarness(t)
	h.s.ResolveAfter, h.s.Cooldown = 0, 0
	h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", normal)
	h.feed(1, "Mumbai", danger)
	if len(h.alerts()) != 2 {
		t.Errorf("alerts = %d, want 2", len(h.alerts()))
	}
}

func TestSubscriptionScoping(t *testing.T) {
	h := newHarness(t)
	mumbai := h.sub(i64(1), "danger", ChannelLog, "")
	delhi := h.sub(i64(2), "danger", ChannelLog, "")
	global := h.logSub("danger")
	removed := h.logSub("danger")
	if err := h.s.DeleteSubscription(context.Background(), removed.ID); err != nil {
		t.Fatal(err)
	}

	h.feed(1, "Mumbai", danger)
	if !eq(h.told(mumbai.ID), []string{"opened:danger"}) || len(h.told(delhi.ID)) != 0 ||
		!eq(h.told(global.ID), []string{"opened:danger"}) || len(h.told(removed.ID)) != 0 {
		t.Errorf("told: mumbai %v, delhi %v, global %v, removed %v", h.told(mumbai.ID), h.told(delhi.ID), h.told(global.ID), h.told(removed.ID))
	}
	h.feed(2, "Delhi", danger)
	if !eq(h.told(delhi.ID), []string{"opened:danger"}) || len(h.told(global.ID)) != 2 {
		t.Errorf("after Delhi: delhi %v, global %v", h.told(delhi.ID), h.told(global.ID))
	}
	if len(h.alerts()) != 2 {
		t.Errorf("each location has its own alert; got %d", len(h.alerts()))
	}
}

func TestASubscriptionCreatedMidAlertHearsAboutItOnTheNextUpdate(t *testing.T) {
	h := newHarness(t)
	h.feed(1, "Mumbai", danger)
	late := h.logSub("danger")
	if len(h.told(late.ID)) != 0 {
		t.Fatal("told before any update arrived")
	}
	h.feed(1, "Mumbai", danger)
	if !eq(h.told(late.ID), []string{"opened:danger"}) {
		t.Errorf("told = %v", h.told(late.ID))
	}
	h.feed(1, "Mumbai", danger)
	if len(h.told(late.ID)) != 1 {
		t.Error("told again at the same level")
	}
}

func TestOnlyOneOpenAlertPerLocationIsPossible(t *testing.T) {
	h := newHarness(t)
	h.feed(1, "Mumbai", danger)
	_, err := h.s.DB.Exec(`INSERT INTO alerts (location_id, location_name, status, current_level, peak_level, headline, summary, details, opened_at, updated_at)
	                       VALUES (1, 'Mumbai', 'open', 'danger', 'danger', 'h', 's', '{}', 0, 0)`)
	if err == nil {
		t.Fatal("the database allowed a second open alert for one location")
	}
	if _, err := h.s.DB.Exec(`INSERT INTO alerts (location_id, location_name, status, current_level, peak_level, headline, summary, details, opened_at, updated_at)
	                          VALUES (2, 'Delhi', 'open', 'danger', 'danger', 'h', 's', '{}', 0, 0)`); err != nil {
		t.Errorf("another location must be allowed its own open alert: %v", err)
	}
}

func TestOpenLevelIsConfigurable(t *testing.T) {
	h := newHarness(t)
	h.s.OpenLevel = extremeC
	h.logSub("extreme-caution")
	h.feed(1, "Mumbai", caution)
	if len(h.alerts()) != 0 {
		t.Fatal("opened below the configured level")
	}
	h.feed(1, "Mumbai", extremeC)
	if a := h.alerts(); len(a) != 1 || a[0].Headline != "Extreme Caution heat alert for Mumbai" {
		t.Errorf("alerts = %+v", a)
	}
}

func TestNotificationPayloadIsFrozenAtCreation(t *testing.T) {
	h := newHarness(t)
	sub := h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", extremeD)

	rows, err := h.s.DB.Query(`SELECT id, kind, payload FROM notifications WHERE subscription_id = ? ORDER BY id`, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var notes []Notification
	for rows.Next() {
		var id int64
		var kind, payload string
		rows.Scan(&id, &kind, &payload)
		var n Notification
		if err := json.Unmarshal([]byte(payload), &n); err != nil {
			t.Fatal(err)
		}
		if n.ID != id || n.Kind != kind {
			t.Errorf("payload id/kind = %d/%s, row says %d/%s", n.ID, n.Kind, id, kind)
		}
		notes = append(notes, n)
	}
	if len(notes) != 2 || notes[0].Alert.CurrentLevel != danger || notes[1].Alert.CurrentLevel != extremeD ||
		notes[0].Alert.Headline != "Danger heat alert for Mumbai" || notes[1].Level != extremeD {
		t.Errorf("notes = %+v; each must describe the alert as it was when sent", notes)
	}
}

func TestOutOfOrderAndDuplicateAssessmentsAreIgnored(t *testing.T) {
	h := newHarness(t)
	h.logSub("danger")
	ev := h.event(1, "Mumbai", danger)
	for i := 0; i < 3; i++ {
		h.receive(ev)
	}
	if h.count(`SELECT COUNT(*) FROM notifications`) != 1 {
		t.Error("a redelivered event notified more than once")
	}

	stale := h.event(1, "Mumbai", normal) // built at the same instant as the first: not newer
	h.receive(stale)
	h.advance(time.Minute)
	newer := h.event(1, "Mumbai", extremeD)
	h.receive(newer)
	old := h.event(1, "Mumbai", normal)
	var env map[string]any
	json.Unmarshal(old.Payload, &env)
	env["weather"].(map[string]any)["fetchedAt"] = epoch.Add(-time.Hour).Format(time.RFC3339Nano)
	old.ID = "late"
	old.Payload, _ = json.Marshal(env)
	h.receive(old)

	if a := h.alerts()[0]; a.CurrentLevel != extremeD || a.Status != "open" {
		t.Errorf("a late reading overwrote newer state: %+v", a)
	}
	if _, _, stale, _, _ := h.s.Counts(); stale != 2 {
		t.Errorf("stale counter = %d, want 2", stale)
	}
}

func TestBadInputIsRecordedAndConsumedNotRetried(t *testing.T) {
	h := newHarness(t)
	noLoc := h.event(0, "x", danger)
	badLevel := h.event(1, "x", "scorching")
	cases := map[string]events.Event{
		"undecodable": {ID: "u", Type: contracts.EventRiskAssessed, Payload: json.RawMessage(`"nope"`)},
		"no location": noLoc,
		"bad level":   badLevel,
	}
	for name, ev := range cases {
		processed, err := h.receive(ev)
		if err != nil || !processed {
			t.Errorf("%s: receive = %v, %v; bad data must be consumed without error", name, processed, err)
		}
	}
	list, err := h.s.RecentRejections(context.Background(), 10)
	if err != nil || len(list) != 3 {
		t.Fatalf("rejections = %d, %v", len(list), err)
	}
	reasons := map[string]string{}
	for _, r := range list {
		reasons[r.EventID] = r.Reason
	}
	if reasons["u"] != ReasonUndecoded || reasons[noLoc.ID] != ReasonInvalid || reasons[badLevel.ID] != ReasonInvalid {
		t.Errorf("reasons = %v", reasons)
	}
	if len(h.alerts()) != 0 {
		t.Error("bad input created an alert")
	}
}

func TestUnknownEventTypeIsIgnored(t *testing.T) {
	h := newHarness(t)
	if processed, err := h.receive(events.Event{ID: "x", Type: "something.else", Payload: json.RawMessage(`{}`)}); err != nil || !processed {
		t.Errorf("= %v, %v", processed, err)
	}
	if h.count(`SELECT COUNT(*) FROM rejections`) != 0 {
		t.Error("an unknown event type was treated as bad data")
	}
}

func TestTransientFailureRollsBackSoTheRetryWorks(t *testing.T) {
	h := newHarness(t)
	h.logSub("danger")
	ev := h.event(1, "Mumbai", danger)
	if _, err := h.s.DB.Exec(`ALTER TABLE notifications RENAME TO notifications_away`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.receive(ev); err == nil {
		t.Fatal("expected an error while notifications are unavailable")
	}
	if len(h.alerts()) != 0 || h.count(`SELECT COUNT(*) FROM inbox`) != 0 || h.count(`SELECT COUNT(*) FROM location_state`) != 0 {
		t.Fatal("a failed attempt left an alert, a dedupe record or location state behind; the retry would be skipped or judged stale")
	}
	h.s.DB.Exec(`ALTER TABLE notifications_away RENAME TO notifications`)
	if processed, err := h.receive(ev); err != nil || !processed {
		t.Fatalf("retry = %v, %v", processed, err)
	}
	if len(h.alerts()) != 1 || h.count(`SELECT COUNT(*) FROM notifications`) != 1 {
		t.Error("retry did not complete the work")
	}
}

func TestPurgeRemovesOldResolvedAlertsAndTheirHistory(t *testing.T) {
	h := newHarness(t)
	h.s.ResolveAfter, h.s.Retention = 0, 24*time.Hour
	h.logSub("danger")
	h.feed(1, "Mumbai", danger)
	h.feed(1, "Mumbai", normal) // resolved
	h.feed(2, "Delhi", danger)  // still open
	h.advance(48 * time.Hour)
	if err := h.s.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	alerts := h.alerts()
	if len(alerts) != 1 || alerts[0].LocationName != "Delhi" {
		t.Errorf("alerts after purge = %+v; open alerts must survive", alerts)
	}
	if h.count(`SELECT COUNT(*) FROM alert_events`) != 1 || h.count(`SELECT COUNT(*) FROM notifications`) != 1 {
		t.Error("history and notifications of the purged alert were left behind")
	}
}
