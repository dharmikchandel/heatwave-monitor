package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/inbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
)

var epoch = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// harness drives the service with a controllable clock.
type harness struct {
	t   *testing.T
	s   *Service
	mu  sync.Mutex // guards now: background goroutines (the notifier) read the clock too
	now time.Time
	obs int64
	ev  int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, now: epoch}
	h.s = &Service{
		DB:           db,
		Clock:        h.clock,
		OpenLevel:    engine.Danger,
		ResolveAfter: time.Hour,
		Cooldown:     2 * time.Hour,
	}
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// event builds a risk.assessed event for a location at the harness's current time.
func (h *harness) event(loc int64, name string, level engine.RiskLevel) events.Event {
	h.t.Helper()
	h.obs++
	h.ev++
	expected := level.Severity() >= engine.Danger.Severity()
	a := contracts.Assessment{
		AlertLevel:       level,
		Now:              contracts.NowRisk{Level: level, ApparentTempC: 40},
		Peak:             contracts.DayRisk{Date: "2026-05-05", Level: level, Probability: 0.9},
		HeatwaveExpected: expected,
		Method:           "model",
		ModelVersion:     "lr-test",
		Rationale:        []string{fmt.Sprintf("Now: %s.", engine.RiskLevelLabel[level]), "Forecast looks hot."},
		AssessedAt:       h.clock(),
	}
	if expected {
		a.WarningDays = []string{"2026-05-03", "2026-05-04"}
	}
	payload, err := json.Marshal(contracts.RiskAssessed{
		Weather: contracts.ProcessedWeather{
			ObservationID: h.obs,
			Location:      contracts.Location{ID: loc, Name: name},
			FetchedAt:     h.clock(),
		},
		Assessment: a,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return events.Event{ID: fmt.Sprintf("ev-%d", h.ev), Type: contracts.EventRiskAssessed, Source: "risk", CreatedAt: h.clock(), Payload: payload}
}

func (h *harness) receive(ev events.Event) (bool, error) {
	h.t.Helper()
	return inbox.Receive(context.Background(), h.s.DB, ev, h.s.HandleEvent)
}

// feed advances one minute and delivers an assessment for the location.
func (h *harness) feed(loc int64, name string, level engine.RiskLevel) {
	h.t.Helper()
	h.advance(time.Minute)
	if processed, err := h.receive(h.event(loc, name, level)); err != nil || !processed {
		h.t.Fatalf("feed(%s, %s) = %v, %v", name, level, processed, err)
	}
}

func (h *harness) sub(loc *int64, min, channel, target string) Subscription {
	h.t.Helper()
	sub, err := h.s.CreateSubscription(context.Background(), NewSubscription{LocationID: loc, MinLevel: min, Channel: channel, Target: target})
	if err != nil {
		h.t.Fatal(err)
	}
	return sub
}

func (h *harness) logSub(min string) Subscription { return h.sub(nil, min, ChannelLog, "") }

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) alerts() []Alert {
	h.t.Helper()
	list, err := h.s.ListAlerts(context.Background(), AlertFilter{Limit: 100})
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func (h *harness) detail(id int64) AlertDetail {
	h.t.Helper()
	d, err := h.s.GetAlert(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

// told lists "kind:level" for each notification sent to a subscription, oldest first.
func (h *harness) told(subID int64) []string {
	h.t.Helper()
	rows, err := h.s.DB.Query(`SELECT kind, level FROM notifications WHERE subscription_id = ? ORDER BY id`, subID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k, l string
		rows.Scan(&k, &l)
		out = append(out, k+":"+l)
	}
	return out
}

func kinds(history []AlertEvent) []string {
	out := []string{}
	for _, e := range history {
		out = append(out, e.Kind)
	}
	return out
}

func i64(v int64) *int64 { return &v }

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }
