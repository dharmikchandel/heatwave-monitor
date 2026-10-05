package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
)

// Notification is the JSON body POSTed to a webhook.
type Notification struct {
	ID        int64            `json:"id"`
	Kind      string           `json:"kind"` // opened, escalated or resolved
	Level     engine.RiskLevel `json:"level"`
	CreatedAt time.Time        `json:"createdAt"`
	Alert     Alert            `json:"alert"`
}

// createNotification records what a subscription is to be told, with its payload
// frozen now so that retries send exactly the same thing.
func (s *Service) createNotification(ctx context.Context, tx *sql.Tx, alertID int64, sub Subscription, kind string, level engine.RiskLevel, now time.Time) error {
	a, err := loadAlert(ctx, tx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, alertID)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("alert %d vanished while notifying", alertID)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO notifications (alert_id, subscription_id, kind, level, created_at, channel, next_attempt_at, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		alertID, sub.ID, kind, string(level), ms(now), sub.Channel, ms(now))
	if err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	id, _ := res.LastInsertId()
	payload, err := json.Marshal(Notification{ID: id, Kind: kind, Level: level, CreatedAt: now.UTC(), Alert: *a})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notifications SET payload = ? WHERE id = ?`, string(payload), id); err != nil {
		return fmt.Errorf("store notification payload: %w", err)
	}
	return nil
}

// Notifier delivers pending notifications: webhooks with retries and backoff, and
// the log channel. Deliveries stay in order per subscription and alert, so an
// "opened" is never overtaken by its "resolved"; different alerts never wait on
// each other, so one bad payload cannot hold up another city's alert.
type Notifier struct {
	Svc    *Service
	Client *http.Client // must not follow redirects; see NewNotifier

	Interval    time.Duration // how often to poll for due deliveries; default 2s
	MaxAttempts int           // attempts before a webhook delivery is marked failed; default 10
	MaxBackoff  time.Duration // retry delay cap; default 1m
	BatchSize   int           // default 50

	wake chan struct{}
}

// NewNotifier returns a Notifier with a client that refuses to follow redirects
// (a redirect could otherwise bounce a request to an internal address).
func NewNotifier(svc *Service) *Notifier {
	return &Notifier{
		Svc: svc,
		Client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (n *Notifier) init() {
	if n.Client == nil {
		n.Client = NewNotifier(n.Svc).Client
	}
	if n.Interval <= 0 {
		n.Interval = 2 * time.Second
	}
	if n.MaxAttempts <= 0 {
		n.MaxAttempts = 10
	}
	if n.MaxBackoff <= 0 {
		n.MaxBackoff = time.Minute
	}
	if n.BatchSize <= 0 {
		n.BatchSize = 50
	}
	if n.wake == nil {
		n.wake = make(chan struct{}, 1)
	}
}

// Notify asks for an immediate delivery pass (call after the transaction that
// created notifications commits). It never blocks.
func (n *Notifier) Notify() {
	n.init()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run delivers until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	n.init()
	t := time.NewTicker(n.Interval)
	defer t.Stop()
	for {
		if _, err := n.DeliverOnce(ctx); err != nil && ctx.Err() == nil {
			n.Svc.log().Error("notification delivery pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.wake:
		}
	}
}

type pending struct {
	id, subID, alertID, attempts int64
	channel, payload             string
	kind                         string
	target, secret               string
	active                       bool
}

// DeliverOnce runs a single pass and returns how many notifications were sent.
func (n *Notifier) DeliverOnce(ctx context.Context) (int, error) {
	n.init()
	now := ms(n.Svc.now())
	rows, err := n.Svc.DB.QueryContext(ctx,
		`SELECT n.id, n.subscription_id, n.alert_id, n.attempts, n.channel, n.payload, n.kind, n.next_attempt_at,
		        s.target, s.secret, s.active
		   FROM notifications n JOIN subscriptions s ON s.id = n.subscription_id
		  WHERE n.status = 'pending' ORDER BY n.id LIMIT ?`, n.BatchSize)
	if err != nil {
		return 0, err
	}
	var due []pending
	type stream struct{ sub, alert int64 } // the unit whose deliveries must stay in order
	holdBack := map[stream]bool{}          // streams whose earlier delivery is still waiting to retry
	for rows.Next() {
		var (
			p      pending
			next   int64
			active int
		)
		if err := rows.Scan(&p.id, &p.subID, &p.alertID, &p.attempts, &p.channel, &p.payload, &p.kind, &next, &p.target, &p.secret, &active); err != nil {
			rows.Close()
			return 0, err
		}
		p.active = active == 1
		key := stream{p.subID, p.alertID}
		if holdBack[key] {
			continue
		}
		if next > now {
			holdBack[key] = true
			continue
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	sent := 0
	blocked := map[stream]bool{}
	for _, p := range due {
		if blocked[stream{p.subID, p.alertID}] {
			continue
		}
		if !p.active {
			n.finish(ctx, p.id, "cancelled", 0, "subscription was removed")
			continue
		}
		code, err := n.deliver(ctx, p)
		if err == nil {
			n.finish(ctx, p.id, "sent", code, "")
			sent++
			continue
		}
		attempts := p.attempts + 1
		if int(attempts) >= n.MaxAttempts {
			n.Svc.log().Error("notification failed permanently", "notification_id", p.id, "subscription_id", p.subID, "attempts", attempts, "err", err)
			n.recordFailure(ctx, p.id, attempts, code, err, "failed", 0)
			continue // give up on this one; later deliveries for the subscription may go out
		}
		blocked[stream{p.subID, p.alertID}] = true
		retryAt := n.Svc.now().Add(backoff(int(attempts), n.MaxBackoff))
		n.recordFailure(ctx, p.id, attempts, code, err, "pending", ms(retryAt))
		n.Svc.log().Warn("notification delivery failed; will retry", "notification_id", p.id, "attempts", attempts, "err", err)
	}
	return sent, nil
}

func (n *Notifier) deliver(ctx context.Context, p pending) (int, error) {
	switch p.channel {
	case ChannelLog:
		var note Notification
		if err := json.Unmarshal([]byte(p.payload), &note); err != nil {
			return 0, err
		}
		n.Svc.log().Warn("heat alert notification",
			"kind", note.Kind, "level", note.Level, "alert_id", note.Alert.ID, "location", note.Alert.LocationName,
			"headline", note.Alert.Headline, "summary", note.Alert.Summary)
		return 0, nil
	case ChannelWebhook:
		return n.postWebhook(ctx, p)
	}
	return 0, fmt.Errorf("unknown channel %q", p.channel)
}

func (n *Notifier) postWebhook(ctx context.Context, p pending) (int, error) {
	body := []byte(p.payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "heatwave-monitor-alerts/1")
	req.Header.Set("X-Heatwave-Event", "alert."+p.kind)
	req.Header.Set("X-Heatwave-Delivery", fmt.Sprint(p.id))
	if p.secret != "" {
		req.Header.Set("X-Heatwave-Signature", "sha256="+Sign(p.secret, body))
	}

	resp, err := n.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("webhook answered %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return resp.StatusCode, nil
}

// Sign returns the hex HMAC-SHA256 of body under secret, as sent in X-Heatwave-Signature.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func (n *Notifier) finish(ctx context.Context, id int64, status string, code int, note string) {
	var codeArg any
	if code != 0 {
		codeArg = code
	}
	var sentAt any
	if status == "sent" {
		sentAt = ms(n.Svc.now())
	}
	if _, err := n.Svc.DB.ExecContext(ctx,
		`UPDATE notifications SET status = ?, attempts = attempts + 1, sent_at = ?, response_code = ?, last_error = ? WHERE id = ?`,
		status, sentAt, codeArg, nullIfEmpty(note), id); err != nil {
		n.Svc.log().Error("update notification", "notification_id", id, "err", err)
	}
}

func (n *Notifier) recordFailure(ctx context.Context, id, attempts int64, code int, cause error, status string, nextAt int64) {
	var codeArg any
	if code != 0 {
		codeArg = code
	}
	msg := cause.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if status == "pending" {
		_, err := n.Svc.DB.ExecContext(ctx,
			`UPDATE notifications SET attempts = ?, next_attempt_at = ?, last_error = ?, response_code = ? WHERE id = ?`,
			attempts, nextAt, msg, codeArg, id)
		if err != nil {
			n.Svc.log().Error("record notification failure", "notification_id", id, "err", err)
		}
		return
	}
	if _, err := n.Svc.DB.ExecContext(ctx,
		`UPDATE notifications SET status = ?, attempts = ?, last_error = ?, response_code = ? WHERE id = ?`,
		status, attempts, msg, codeArg, id); err != nil {
		n.Svc.log().Error("record notification failure", "notification_id", id, "err", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// backoff returns 1s, 2s, 4s, ... capped at max.
func backoff(attempts int, max time.Duration) time.Duration {
	if attempts > 10 {
		return max
	}
	d := time.Second << (attempts - 1)
	if d > max {
		return max
	}
	return d
}
