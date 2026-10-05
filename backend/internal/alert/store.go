package alert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
)

// Channels a subscription can use.
const (
	ChannelWebhook = "webhook"
	ChannelLog     = "log"
	ChannelInApp   = "inapp" // shown in the user's inbox; there is nothing to deliver
)

// queryer is satisfied by both *sql.DB and *sql.Tx, so the same helpers serve
// request handlers and the event transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// ---- subscriptions ----

// Subscription says who wants to hear about which alerts, and how.
type Subscription struct {
	ID         int64            `json:"id"`
	LocationID *int64           `json:"locationId"` // nil = every location
	MinLevel   engine.RiskLevel `json:"minLevel"`
	Channel    string           `json:"channel"`
	Target     string           `json:"target,omitempty"`
	HasSecret  bool             `json:"hasSecret"` // the secret itself is never returned
	Label      string           `json:"label,omitempty"`
	Active     bool             `json:"active"`
	CreatedAt  time.Time        `json:"createdAt"`

	secret string
	userID *int64
}

// NewSubscription is the input for creating a subscription.
type NewSubscription struct {
	LocationID *int64
	MinLevel   string
	Channel    string
	Target     string
	Secret     string
	Label      string
	UserID     *int64 // set for in-app subscriptions, which belong to a user
}

func (s *Service) validateSubscription(in NewSubscription) (engine.RiskLevel, error) {
	level := engine.RiskLevel(in.MinLevel)
	if level.Severity() < 0 {
		return "", validationError("minLevel %q is not a risk level", in.MinLevel)
	}
	if level.Severity() < s.openLevel().Severity() {
		return "", validationError("minLevel must be %s or higher: alerts only open at %s", s.openLevel(), s.openLevel())
	}
	if in.LocationID != nil && *in.LocationID <= 0 {
		return "", validationError("locationId must be a positive integer")
	}
	if len([]rune(in.Label)) > 100 {
		return "", validationError("label must be at most 100 characters")
	}

	if (in.Channel == ChannelInApp) != (in.UserID != nil) {
		return "", validationError("only the %q channel is for users, and it needs a user", ChannelInApp)
	}
	switch in.Channel {
	case ChannelInApp:
		if in.Target != "" || in.Secret != "" {
			return "", validationError("the %s channel takes no target or secret", ChannelInApp)
		}
	case ChannelWebhook:
		if err := s.validateWebhookURL(in.Target); err != nil {
			return "", err
		}
		if len(in.Secret) > 200 {
			return "", validationError("secret must be at most 200 characters")
		}
	case ChannelLog:
		if in.Target != "" || in.Secret != "" {
			return "", validationError("the log channel takes no target or secret")
		}
	default:
		return "", validationError("channel must be %q, %q or %q", ChannelWebhook, ChannelLog, ChannelInApp)
	}
	return level, nil
}

func (s *Service) validateWebhookURL(raw string) error {
	if raw == "" {
		return validationError("target is required for the webhook channel")
	}
	if len(raw) > 500 {
		return validationError("target must be at most 500 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return validationError("target must be an http or https URL")
	}
	if u.User != nil {
		return validationError("target must not contain credentials")
	}
	if len(s.AllowedHosts) > 0 {
		for _, h := range s.AllowedHosts {
			if strings.EqualFold(h, u.Hostname()) {
				return nil
			}
		}
		return validationError("target host %q is not in the allowed list", u.Hostname())
	}
	return nil
}

// CreateSubscription validates and stores a subscription.
func (s *Service) CreateSubscription(ctx context.Context, in NewSubscription) (Subscription, error) {
	level, err := s.validateSubscription(in)
	if err != nil {
		return Subscription{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, err
	}
	defer tx.Rollback()

	var loc any
	if in.LocationID != nil {
		loc = *in.LocationID
	}
	var id int64
	if in.UserID != nil {
		id, err = s.upsertUserSubscription(ctx, tx, in, level, loc)
	} else {
		id, err = s.insertOperatorSubscription(ctx, tx, in, level, loc)
	}
	if err != nil {
		return Subscription{}, err
	}
	sub, err := getSubscription(ctx, tx, id)
	if err != nil {
		return Subscription{}, err
	}
	return sub, tx.Commit()
}

// insertOperatorSubscription adds a webhook or log subscription, subject to the operator limit.
func (s *Service) insertOperatorSubscription(ctx context.Context, tx *sql.Tx, in NewSubscription, level engine.RiskLevel, loc any) (int64, error) {
	if s.MaxSubscriptions > 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE active = 1 AND user_id IS NULL`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= s.MaxSubscriptions {
			return 0, ErrTooManySubs
		}
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO subscriptions (location_id, min_level, channel, target, secret, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		loc, string(level), in.Channel, in.Target, in.Secret, strings.TrimSpace(in.Label), ms(s.now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// upsertUserSubscription follows a location for a user. Following it again just changes
// the threshold, so the call is safe to repeat.
func (s *Service) upsertUserSubscription(ctx context.Context, tx *sql.Tx, in NewSubscription, level engine.RiskLevel, loc any) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM subscriptions WHERE user_id = ? AND active = 1 AND COALESCE(location_id, 0) = COALESCE(?, 0)`, *in.UserID, loc).Scan(&id)
	switch {
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE subscriptions SET min_level = ? WHERE id = ?`, string(level), id)
		return id, err
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND active = 1`, *in.UserID).Scan(&n); err != nil {
		return 0, err
	}
	if n >= s.maxUserSubs() {
		return 0, ErrTooManySubs
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO subscriptions (location_id, min_level, channel, label, created_at, user_id) VALUES (?, ?, ?, ?, ?, ?)`,
		loc, string(level), ChannelInApp, strings.TrimSpace(in.Label), ms(s.now()), *in.UserID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const subscriptionColumns = `id, location_id, min_level, channel, target, secret, label, active, created_at, user_id`

func scanSubscription(r interface{ Scan(...any) error }) (Subscription, error) {
	var (
		sub     Subscription
		loc     sql.NullInt64
		user    sql.NullInt64
		level   string
		active  int
		created int64
	)
	if err := r.Scan(&sub.ID, &loc, &level, &sub.Channel, &sub.Target, &sub.secret, &sub.Label, &active, &created, &user); err != nil {
		return Subscription{}, err
	}
	if loc.Valid {
		sub.LocationID = &loc.Int64
	}
	if user.Valid {
		sub.userID = &user.Int64
	}
	sub.MinLevel, sub.Active, sub.CreatedAt, sub.HasSecret = engine.RiskLevel(level), active == 1, fromMS(created), sub.secret != ""
	return sub, nil
}

func getSubscription(ctx context.Context, q queryer, id int64) (Subscription, error) {
	sub, err := scanSubscription(q.QueryRowContext(ctx, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return sub, err
}

// ListSubscriptions returns the operators' active subscriptions, optionally only those for
// one location (including the "every location" ones that apply to it). Users' own in-app
// subscriptions are private to them and never listed here.
func (s *Service) ListSubscriptions(ctx context.Context, locationID *int64) ([]Subscription, error) {
	return listSubscriptions(ctx, s.DB, locationID, true)
}

// listSubscriptions is also what alert matching uses, with operatorOnly false so that
// users' subscriptions are included.
func listSubscriptions(ctx context.Context, q queryer, locationID *int64, operatorOnly bool) ([]Subscription, error) {
	query, args := `SELECT `+subscriptionColumns+` FROM subscriptions WHERE active = 1`, []any{}
	if operatorOnly {
		query += ` AND user_id IS NULL`
	}
	if locationID != nil {
		query += ` AND (location_id IS NULL OR location_id = ?)`
		args = append(args, *locationID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteSubscription deactivates an operator subscription and cancels notifications still
// waiting for it. Users' subscriptions are theirs to remove (DeleteUserSubscription).
func (s *Service) DeleteSubscription(ctx context.Context, id int64) error {
	return s.deactivate(ctx, id, nil)
}

func (s *Service) deactivate(ctx context.Context, id int64, userID *int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query, args := `UPDATE subscriptions SET active = 0 WHERE id = ? AND active = 1 AND user_id IS NULL`, []any{id}
	if userID != nil {
		query, args = `UPDATE subscriptions SET active = 0 WHERE id = ? AND active = 1 AND user_id = ?`, []any{id, *userID}
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notifications SET status = 'cancelled' WHERE subscription_id = ? AND status = 'pending'`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- alerts ----

// AlertDetails is the latest assessment summary an alert carries.
type AlertDetails struct {
	ObservationID    int64             `json:"observationId"`
	Now              contracts.NowRisk `json:"now"`
	Peak             contracts.DayRisk `json:"peak"`
	HeatwaveExpected bool              `json:"heatwaveExpected"`
	WarningDays      []string          `json:"warningDays"`
	Rationale        []string          `json:"rationale"`
	Method           string            `json:"method"`
	ModelVersion     string            `json:"modelVersion"`
}

// Alert is one alert episode for a location.
type Alert struct {
	ID             int64            `json:"id"`
	LocationID     int64            `json:"locationId"`
	LocationName   string           `json:"locationName"`
	Status         string           `json:"status"` // "open" or "resolved"
	CurrentLevel   engine.RiskLevel `json:"currentLevel"`
	PeakLevel      engine.RiskLevel `json:"peakLevel"`
	Headline       string           `json:"headline"`
	Summary        string           `json:"summary"`
	Details        AlertDetails     `json:"details"`
	OpenedAt       time.Time        `json:"openedAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	ResolvedAt     *time.Time       `json:"resolvedAt,omitempty"`
	AcknowledgedAt *time.Time       `json:"acknowledgedAt,omitempty"`
	ReopenCount    int              `json:"reopenCount"`

	belowSince *time.Time
}

const alertColumns = `id, location_id, location_name, status, current_level, peak_level, headline, summary, details,
	opened_at, updated_at, resolved_at, acknowledged_at, below_since, reopen_count`

func scanAlert(r interface{ Scan(...any) error }) (Alert, error) {
	var (
		a                      Alert
		current, peak, details string
		opened, updated        int64
		resolved, acked, below sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.LocationID, &a.LocationName, &a.Status, &current, &peak, &a.Headline, &a.Summary, &details,
		&opened, &updated, &resolved, &acked, &below, &a.ReopenCount); err != nil {
		return Alert{}, err
	}
	a.CurrentLevel, a.PeakLevel = engine.RiskLevel(current), engine.RiskLevel(peak)
	a.OpenedAt, a.UpdatedAt = fromMS(opened), fromMS(updated)
	a.ResolvedAt, a.AcknowledgedAt, a.belowSince = nullTime(resolved), nullTime(acked), nullTime(below)
	if err := json.Unmarshal([]byte(details), &a.Details); err != nil {
		return Alert{}, fmt.Errorf("decode alert %d details: %w", a.ID, err)
	}
	return a, nil
}

// AlertFilter selects alerts for listing.
type AlertFilter struct {
	Status     string // "open", "resolved" or "" for both
	LocationID *int64
	Limit      int
}

// ListAlerts returns alerts newest first.
func (s *Service) ListAlerts(ctx context.Context, f AlertFilter) ([]Alert, error) {
	query, args := `SELECT `+alertColumns+` FROM alerts WHERE 1=1`, []any{}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.LocationID != nil {
		query += ` AND location_id = ?`
		args = append(args, *f.LocationID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, query+` ORDER BY opened_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlertEvent is one step in an alert's timeline.
type AlertEvent struct {
	At    time.Time        `json:"at"`
	Kind  string           `json:"kind"`
	Level engine.RiskLevel `json:"level"`
	Note  string           `json:"note,omitempty"`
}

// NotificationInfo reports what a subscription was told and whether it arrived.
type NotificationInfo struct {
	ID             int64            `json:"id"`
	SubscriptionID int64            `json:"subscriptionId"`
	Kind           string           `json:"kind"`
	Level          engine.RiskLevel `json:"level"`
	Channel        string           `json:"channel"`
	Status         string           `json:"status"`
	Attempts       int              `json:"attempts"`
	CreatedAt      time.Time        `json:"createdAt"`
	SentAt         *time.Time       `json:"sentAt,omitempty"`
	LastError      string           `json:"lastError,omitempty"`
	ResponseCode   *int             `json:"responseCode,omitempty"`
}

// AlertDetail is an alert with its timeline and notifications.
type AlertDetail struct {
	Alert
	History       []AlertEvent       `json:"history"`
	Notifications []NotificationInfo `json:"notifications"`
}

// GetAlert returns one alert with its history and notifications.
func (s *Service) GetAlert(ctx context.Context, id int64) (AlertDetail, error) {
	a, err := scanAlert(s.DB.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AlertDetail{}, ErrNotFound
	}
	if err != nil {
		return AlertDetail{}, err
	}
	d := AlertDetail{Alert: a, History: []AlertEvent{}, Notifications: []NotificationInfo{}}

	rows, err := s.DB.QueryContext(ctx, `SELECT at, kind, level, note FROM alert_events WHERE alert_id = ? ORDER BY id`, id)
	if err != nil {
		return AlertDetail{}, err
	}
	for rows.Next() {
		var (
			e     AlertEvent
			at    int64
			level string
		)
		if err := rows.Scan(&at, &e.Kind, &level, &e.Note); err != nil {
			rows.Close()
			return AlertDetail{}, err
		}
		e.At, e.Level = fromMS(at), engine.RiskLevel(level)
		d.History = append(d.History, e)
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx,
		`SELECT id, subscription_id, kind, level, channel, status, attempts, created_at, sent_at, last_error, response_code
		   FROM notifications WHERE alert_id = ? ORDER BY id`, id)
	if err != nil {
		return AlertDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			n       NotificationInfo
			level   string
			created int64
			sent    sql.NullInt64
			lastErr sql.NullString
			code    sql.NullInt64
		)
		if err := rows.Scan(&n.ID, &n.SubscriptionID, &n.Kind, &level, &n.Channel, &n.Status, &n.Attempts, &created, &sent, &lastErr, &code); err != nil {
			return AlertDetail{}, err
		}
		n.Level, n.CreatedAt, n.SentAt, n.LastError = engine.RiskLevel(level), fromMS(created), nullTime(sent), lastErr.String
		if code.Valid {
			c := int(code.Int64)
			n.ResponseCode = &c
		}
		d.Notifications = append(d.Notifications, n)
	}
	return d, rows.Err()
}

// Acknowledge marks an alert as seen. It is idempotent: acknowledging again keeps the first time.
func (s *Service) Acknowledge(ctx context.Context, id int64) (Alert, error) {
	if _, err := s.DB.ExecContext(ctx, `UPDATE alerts SET acknowledged_at = COALESCE(acknowledged_at, ?) WHERE id = ?`, ms(s.now()), id); err != nil {
		return Alert{}, err
	}
	a, err := scanAlert(s.DB.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

// ListNotifications returns notifications by delivery status (for operators), newest first.
func (s *Service) ListNotifications(ctx context.Context, status string, limit int) ([]NotificationInfo, error) {
	if limit <= 0 {
		limit = 50
	}
	query, args := `SELECT id, subscription_id, kind, level, channel, status, attempts, created_at, sent_at, last_error, response_code FROM notifications WHERE 1=1`, []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, query+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationInfo{}
	for rows.Next() {
		var (
			n       NotificationInfo
			level   string
			created int64
			sent    sql.NullInt64
			lastErr sql.NullString
			code    sql.NullInt64
		)
		if err := rows.Scan(&n.ID, &n.SubscriptionID, &n.Kind, &level, &n.Channel, &n.Status, &n.Attempts, &created, &sent, &lastErr, &code); err != nil {
			return nil, err
		}
		n.Level, n.CreatedAt, n.SentAt, n.LastError = engine.RiskLevel(level), fromMS(created), nullTime(sent), lastErr.String
		if code.Valid {
			c := int(code.Int64)
			n.ResponseCode = &c
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- users: subscriptions and inbox ----

// UserSubscriptions lists a user's active in-app subscriptions.
func (s *Service) UserSubscriptions(ctx context.Context, userID int64) ([]Subscription, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE user_id = ? AND active = 1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteUserSubscription stops a user following a location. Notifications they already
// received stay in their inbox.
func (s *Service) DeleteUserSubscription(ctx context.Context, userID, id int64) error {
	return s.deactivate(ctx, id, &userID)
}

// InboxItem is one in-app notification, with the alert text as it was when it was sent.
type InboxItem struct {
	ID           int64            `json:"id"`
	Kind         string           `json:"kind"`
	Level        engine.RiskLevel `json:"level"`
	CreatedAt    time.Time        `json:"createdAt"`
	ReadAt       *time.Time       `json:"readAt,omitempty"`
	AlertID      int64            `json:"alertId"`
	LocationID   int64            `json:"locationId"`
	LocationName string           `json:"locationName"`
	Headline     string           `json:"headline"`
	Summary      string           `json:"summary"`
}

// Inbox is a user's newest notifications and how many are unread overall.
type Inbox struct {
	Unread        int         `json:"unread"`
	Notifications []InboxItem `json:"notifications"`
}

// Inbox returns the user's newest in-app notifications, including those from subscriptions
// they have since removed.
func (s *Service) Inbox(ctx context.Context, userID int64, limit int) (Inbox, error) {
	box := Inbox{Notifications: []InboxItem{}}
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications n JOIN subscriptions s ON s.id = n.subscription_id
		  WHERE s.user_id = ? AND n.read_at IS NULL`, userID).Scan(&box.Unread); err != nil {
		return Inbox{}, err
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT n.id, n.kind, n.level, n.created_at, n.read_at, n.payload
		   FROM notifications n JOIN subscriptions s ON s.id = n.subscription_id
		  WHERE s.user_id = ? ORDER BY n.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return Inbox{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			it      InboxItem
			level   string
			created int64
			read    sql.NullInt64
			payload string
			note    Notification
		)
		if err := rows.Scan(&it.ID, &it.Kind, &level, &created, &read, &payload); err != nil {
			return Inbox{}, err
		}
		if err := json.Unmarshal([]byte(payload), &note); err != nil {
			return Inbox{}, fmt.Errorf("decode notification %d: %w", it.ID, err)
		}
		it.Level, it.CreatedAt, it.ReadAt = engine.RiskLevel(level), fromMS(created), nullTime(read)
		it.AlertID, it.LocationID, it.LocationName = note.Alert.ID, note.Alert.LocationID, note.Alert.LocationName
		it.Headline, it.Summary = note.Alert.Headline, note.Alert.Summary
		box.Notifications = append(box.Notifications, it)
	}
	return box, rows.Err()
}

// MarkRead marks one of the user's notifications read. Reading it again keeps the first time.
func (s *Service) MarkRead(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE notifications SET read_at = COALESCE(read_at, ?)
		  WHERE id = ? AND subscription_id IN (SELECT id FROM subscriptions WHERE user_id = ?)`, ms(s.now()), id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead marks everything in the user's inbox read.
func (s *Service) MarkAllRead(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE read_at IS NULL
		   AND subscription_id IN (SELECT id FROM subscriptions WHERE user_id = ?)`, ms(s.now()), userID)
	return err
}

// ForgetUser deletes a user's subscriptions and inbox (used when the account is deleted).
func (s *Service) ForgetUser(ctx context.Context, userID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE subscription_id IN (SELECT id FROM subscriptions WHERE user_id = ?)`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}
