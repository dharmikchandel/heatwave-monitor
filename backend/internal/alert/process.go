package alert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/events"
)

// Reasons an input is rejected. Retrying the same bad data would never help.
const (
	ReasonInvalid   = "invalid_payload"
	ReasonUndecoded = "undecodable_payload"
)

// Notification kinds.
const (
	KindOpened    = "opened"
	KindEscalated = "escalated"
	KindResolved  = "resolved"
)

type rejectErr struct{ reason, detail string }

// HandleEvent is the inbox handler for risk.assessed. It runs inside the inbox
// transaction, so alert changes, notifications and the dedupe record commit
// together. Only transient failures return an error (and so get retried); bad
// data is recorded and consumed.
func (s *Service) HandleEvent(ctx context.Context, tx *sql.Tx, ev events.Event) error {
	if ev.Type != contracts.EventRiskAssessed {
		s.log().Warn("ignoring unknown event type", "type", ev.Type, "event_id", ev.ID)
		return nil
	}

	var in contracts.RiskAssessed
	if err := ev.Decode(&in); err != nil {
		return s.reject(ctx, tx, ev, nil, rejectErr{ReasonUndecoded, err.Error()})
	}
	switch {
	case in.Weather.Location.ID <= 0:
		return s.reject(ctx, tx, ev, &in, rejectErr{ReasonInvalid, "event has no location id"})
	case in.Assessment.AlertLevel.Severity() < 0:
		return s.reject(ctx, tx, ev, &in, rejectErr{ReasonInvalid, fmt.Sprintf("unknown alert level %q", in.Assessment.AlertLevel)})
	}

	// Never let an older observation undo a newer one.
	loc := in.Weather.Location.ID
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT last_fetched_at FROM location_state WHERE location_id = ?`, loc).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check location state: %w", err)
	}
	fetched := in.Weather.FetchedAt.UnixMilli()
	if last.Valid && fetched <= last.Int64 {
		s.stale.Add(1)
		s.log().Info("dropping stale observation", "location_id", loc, "observation_id", in.Weather.ObservationID)
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO location_state (location_id, last_fetched_at) VALUES (?, ?)
		 ON CONFLICT(location_id) DO UPDATE SET last_fetched_at = excluded.last_fetched_at`, loc, fetched); err != nil {
		return fmt.Errorf("update location state: %w", err)
	}

	if err := s.apply(ctx, tx, in); err != nil {
		return err
	}
	s.handled.Add(1)
	return nil
}

func (s *Service) reject(ctx context.Context, tx *sql.Tx, ev events.Event, in *contracts.RiskAssessed, rej rejectErr) error {
	var locID, obsID any
	if in != nil {
		locID, obsID = in.Weather.Location.ID, in.Weather.ObservationID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO rejections (event_id, location_id, observation_id, reason, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ev.ID, locID, obsID, rej.reason, rej.detail, ms(s.now())); err != nil {
		return fmt.Errorf("record rejection: %w", err)
	}
	s.rejected.Add(1)
	s.log().Warn("rejected observation", "event_id", ev.ID, "location_id", locID, "reason", rej.reason, "detail", rej.detail)
	return nil
}

func detailsOf(in contracts.RiskAssessed) AlertDetails {
	a := in.Assessment
	return AlertDetails{
		ObservationID:    in.Weather.ObservationID,
		Now:              a.Now,
		Peak:             a.Peak,
		HeatwaveExpected: a.HeatwaveExpected,
		WarningDays:      append([]string{}, a.WarningDays...),
		Rationale:        append([]string{}, a.Rationale...),
		Method:           a.Method,
		ModelVersion:     a.ModelVersion,
	}
}

func describe(in contracts.RiskAssessed, level engine.RiskLevel) (headline, summary string) {
	name := in.Weather.Location.Name
	if name == "" {
		name = fmt.Sprintf("location %d", in.Weather.Location.ID)
	}
	return fmt.Sprintf("%s heat alert for %s", engine.RiskLevelLabel[level], name), strings.Join(in.Assessment.Rationale, " ")
}

func maxLevel(a, b engine.RiskLevel) engine.RiskLevel {
	if b.Severity() > a.Severity() {
		return b
	}
	return a
}

// apply moves the location's alert through its lifecycle for one assessment.
func (s *Service) apply(ctx context.Context, tx *sql.Tx, in contracts.RiskAssessed) error {
	now := s.now()
	level := in.Assessment.AlertLevel
	loc := in.Weather.Location
	aboveOpen := level.Severity() >= s.openLevel().Severity()

	open, err := loadAlert(ctx, tx, `SELECT `+alertColumns+` FROM alerts WHERE location_id = ? AND status = 'open'`, loc.ID)
	if err != nil {
		return err
	}

	if open == nil {
		if !aboveOpen {
			return nil
		}
		return s.openOrReopen(ctx, tx, in, now)
	}

	headline, summary := describe(in, level)
	details, err := json.Marshal(detailsOf(in))
	if err != nil {
		return err
	}

	if aboveOpen {
		peak := maxLevel(open.PeakLevel, level)
		if _, err := tx.ExecContext(ctx,
			`UPDATE alerts SET current_level = ?, peak_level = ?, headline = ?, summary = ?, details = ?, updated_at = ?,
			        below_since = NULL, location_name = ? WHERE id = ?`,
			string(level), string(peak), headline, summary, string(details), ms(now), loc.Name, open.ID); err != nil {
			return fmt.Errorf("update alert: %w", err)
		}
		switch {
		case level.Severity() > open.CurrentLevel.Severity():
			note := ""
			if open.belowSince != nil {
				note = "risk returned to the alert level before the alert was resolved"
			}
			if err := addEvent(ctx, tx, open.ID, now, "escalated", level, note); err != nil {
				return err
			}
		case level.Severity() < open.CurrentLevel.Severity():
			if err := addEvent(ctx, tx, open.ID, now, "deescalated", level, ""); err != nil {
				return err
			}
		}
		return s.notifyAbove(ctx, tx, open.ID, level, now)
	}

	// Below the alert level: wait out the hysteresis window before resolving.
	since := now
	if open.belowSince != nil {
		since = *open.belowSince
	}
	if now.Sub(since) >= s.ResolveAfter {
		return s.resolve(ctx, tx, open, level, string(details), now)
	}
	if open.belowSince == nil || level != open.CurrentLevel {
		note := ""
		if open.belowSince == nil {
			note = fmt.Sprintf("risk fell below %s; will resolve if it stays there for %s", s.openLevel(), s.ResolveAfter)
		}
		if err := addEvent(ctx, tx, open.ID, now, "deescalated", level, note); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE alerts SET current_level = ?, details = ?, updated_at = ?, below_since = ? WHERE id = ?`,
		string(level), string(details), ms(now), ms(since), open.ID)
	return err
}

// openOrReopen starts an alert for a location with none open. If one was resolved
// within the cooldown, that one is reopened so a flickering forecast does not
// announce the same heatwave twice.
func (s *Service) openOrReopen(ctx context.Context, tx *sql.Tx, in contracts.RiskAssessed, now time.Time) error {
	level, loc := in.Assessment.AlertLevel, in.Weather.Location
	headline, summary := describe(in, level)
	detailsJSON, err := json.Marshal(detailsOf(in))
	if err != nil {
		return err
	}

	if s.Cooldown > 0 {
		prev, err := loadAlert(ctx, tx,
			`SELECT `+alertColumns+` FROM alerts WHERE location_id = ? AND status = 'resolved' AND resolved_at >= ?
			  ORDER BY resolved_at DESC, id DESC LIMIT 1`, loc.ID, ms(now.Add(-s.Cooldown)))
		if err != nil {
			return err
		}
		if prev != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE alerts SET status = 'open', current_level = ?, peak_level = ?, headline = ?, summary = ?, details = ?,
				        updated_at = ?, resolved_at = NULL, below_since = NULL, acknowledged_at = NULL,
				        reopen_count = reopen_count + 1, location_name = ? WHERE id = ?`,
				string(level), string(maxLevel(prev.PeakLevel, level)), headline, summary, string(detailsJSON), ms(now), loc.Name, prev.ID); err != nil {
				return fmt.Errorf("reopen alert: %w", err)
			}
			if err := addEvent(ctx, tx, prev.ID, now, "reopened", level, "risk returned within the cooldown after the alert was resolved"); err != nil {
				return err
			}
			s.log().Info("alert reopened", "alert_id", prev.ID, "location_id", loc.ID, "level", level)
			return s.notifyAbove(ctx, tx, prev.ID, level, now)
		}
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO alerts (location_id, location_name, status, current_level, peak_level, headline, summary, details, opened_at, updated_at)
		 VALUES (?, ?, 'open', ?, ?, ?, ?, ?, ?, ?)`,
		loc.ID, loc.Name, string(level), string(level), headline, summary, string(detailsJSON), ms(now), ms(now))
	if err != nil {
		return fmt.Errorf("open alert: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := addEvent(ctx, tx, id, now, "opened", level, ""); err != nil {
		return err
	}
	s.opened.Add(1)
	s.log().Info("alert opened", "alert_id", id, "location_id", loc.ID, "location", loc.Name, "level", level)
	return s.notifyAbove(ctx, tx, id, level, now)
}

// resolve closes an alert and tells everyone who had been told about it.
func (s *Service) resolve(ctx context.Context, tx *sql.Tx, a *Alert, level engine.RiskLevel, detailsJSON string, now time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE alerts SET status = 'resolved', current_level = ?, details = ?, updated_at = ?, resolved_at = ?, below_since = NULL WHERE id = ?`,
		string(level), detailsJSON, ms(now), ms(now), a.ID); err != nil {
		return fmt.Errorf("resolve alert: %w", err)
	}
	if err := addEvent(ctx, tx, a.ID, now, "resolved", level, ""); err != nil {
		return err
	}
	s.resolved.Add(1)
	s.log().Info("alert resolved", "alert_id", a.ID, "location_id", a.LocationID, "level", level)

	subs, err := listSubscriptions(ctx, tx, &a.LocationID, false)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		told, err := lastNotifiedLevel(ctx, tx, a.ID, sub.ID)
		if err != nil {
			return err
		}
		if told == "" {
			continue // this subscription never heard about the alert, so it needs no all-clear
		}
		if err := s.createNotification(ctx, tx, a.ID, sub, KindResolved, level, now); err != nil {
			return err
		}
	}
	return nil
}

// notifyAbove tells each matching subscription about an open alert at level, but
// only what is new to it: its first notification, or a level above the highest
// it has been told. Repeats and de-escalations notify no one.
func (s *Service) notifyAbove(ctx context.Context, tx *sql.Tx, alertID int64, level engine.RiskLevel, now time.Time) error {
	a, err := loadAlert(ctx, tx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, alertID)
	if err != nil || a == nil {
		return err
	}
	subs, err := listSubscriptions(ctx, tx, &a.LocationID, false)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if level.Severity() < sub.MinLevel.Severity() {
			continue
		}
		told, err := lastNotifiedLevel(ctx, tx, alertID, sub.ID)
		if err != nil {
			return err
		}
		kind := KindEscalated
		switch {
		case told == "":
			kind = KindOpened
		case level.Severity() <= told.Severity():
			continue // nothing new for this subscription
		}
		if err := s.createNotification(ctx, tx, alertID, sub, kind, level, now); err != nil {
			return err
		}
	}
	return nil
}

// lastNotifiedLevel is the highest level this subscription has been told about for the alert ("" if none).
func lastNotifiedLevel(ctx context.Context, q queryer, alertID, subID int64) (engine.RiskLevel, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT level FROM notifications WHERE alert_id = ? AND subscription_id = ? AND kind IN ('opened', 'escalated')`, alertID, subID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var best engine.RiskLevel
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return "", err
		}
		best = maxLevel(best, engine.RiskLevel(l))
	}
	return best, rows.Err()
}

func addEvent(ctx context.Context, tx *sql.Tx, alertID int64, at time.Time, kind string, level engine.RiskLevel, note string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_events (alert_id, at, kind, level, note) VALUES (?, ?, ?, ?, ?)`,
		alertID, ms(at), kind, string(level), note); err != nil {
		return fmt.Errorf("record alert event: %w", err)
	}
	return nil
}

func loadAlert(ctx context.Context, q queryer, query string, args ...any) (*Alert, error) {
	a, err := scanAlert(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load alert: %w", err)
	}
	return &a, nil
}
