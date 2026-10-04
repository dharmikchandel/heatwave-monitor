package weather

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
)

// Service owns the location registry and turns source fetches into stored
// observations plus outbox events.
type Service struct {
	DB         *sql.DB
	Source     Source
	Dispatcher *outbox.Dispatcher // optional; nudged after each stored observation
	Log        *slog.Logger

	Clock           func() time.Time // defaults to time.Now
	RequestGap      time.Duration    // pause between locations in a poll cycle (be kind to the API)
	Retention       time.Duration    // observations older than this are purged; default 24h
	MaxLocations    int              // 0 = unlimited
	DefaultScenario Scenario         // simulated source only; default "normal"

	locks sync.Map // location id -> *sync.Mutex, serialises fetches per location
	wg    sync.WaitGroup

	fetchOK   atomic.Int64
	fetchFail atomic.Int64
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *Service) lockFor(id int64) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Wait blocks until background refreshes started by the API have finished.
func (s *Service) Wait() { s.wg.Wait() }

// background runs fn detached from the request but bounded in time.
func (s *Service) background(ctx context.Context, fn func(context.Context)) {
	s.wg.Add(1)
	bg := context.WithoutCancel(ctx)
	go func() {
		defer s.wg.Done()
		bgCtx, cancel := context.WithTimeout(bg, 45*time.Second)
		defer cancel()
		fn(bgCtx)
	}()
}

// defaultSeed is the starter watch list.
var defaultSeed = []NewLocation{
	{Name: "Mumbai", Country: "India", Admin1: "Maharashtra", Latitude: 19.076, Longitude: 72.8777, Timezone: "Asia/Kolkata"},
	{Name: "Delhi", Country: "India", Admin1: "Delhi", Latitude: 28.6139, Longitude: 77.209, Timezone: "Asia/Kolkata"},
	{Name: "Nagpur", Country: "India", Admin1: "Maharashtra", Latitude: 21.1458, Longitude: 79.0882, Timezone: "Asia/Kolkata"},
	{Name: "Ahmedabad", Country: "India", Admin1: "Gujarat", Latitude: 23.0225, Longitude: 72.5714, Timezone: "Asia/Kolkata"},
	{Name: "Jaipur", Country: "India", Admin1: "Rajasthan", Latitude: 26.9124, Longitude: 75.7873, Timezone: "Asia/Kolkata"},
	{Name: "Chennai", Country: "India", Admin1: "Tamil Nadu", Latitude: 13.0827, Longitude: 80.2707, Timezone: "Asia/Kolkata"},
	{Name: "Kolkata", Country: "India", Admin1: "West Bengal", Latitude: 22.5726, Longitude: 88.3639, Timezone: "Asia/Kolkata"},
	{Name: "Hyderabad", Country: "India", Admin1: "Telangana", Latitude: 17.385, Longitude: 78.4867, Timezone: "Asia/Kolkata"},
}

// Seed registers the starter locations, but only on a brand-new database, so
// locations an operator removed never come back after a restart.
func (s *Service) Seed(ctx context.Context) (int, error) {
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM locations`).Scan(&total); err != nil {
		return 0, err
	}
	if total > 0 {
		return 0, nil
	}
	for _, l := range defaultSeed {
		if _, _, err := s.AddLocation(ctx, l); err != nil {
			return 0, fmt.Errorf("seed %s: %w", l.Name, err)
		}
	}
	return len(defaultSeed), nil
}

// Refresh fetches fresh data for one location, stores it, and queues a
// weather.updated event for the processing service. The observation and the
// event are written in one transaction, so a stored observation is never
// announced twice or lost.
func (s *Service) Refresh(ctx context.Context, locationID int64) (Observation, error) {
	loc, err := s.GetLocation(ctx, locationID)
	if err != nil {
		return Observation{}, err
	}
	if !loc.Active {
		return Observation{}, ErrNotFound
	}

	mu := s.lockFor(locationID)
	mu.Lock()
	defer mu.Unlock()

	snap, err := s.Source.Fetch(ctx, loc.Location)
	if err != nil {
		s.fetchFail.Add(1)
		s.recordFailure(ctx, locationID, err)
		return Observation{}, err
	}
	s.fetchOK.Add(1)

	if loc.Timezone == "" && snap.Timezone != "" {
		loc.Timezone = snap.Timezone // the source resolved it (timezone=auto)
	}
	fetchedAt := s.now().UTC()
	body, err := json.Marshal(snap)
	if err != nil {
		return Observation{}, fmt.Errorf("encode snapshot: %w", err)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO observations (location_id, fetched_at, source, payload) VALUES (?, ?, ?, ?)`,
		locationID, fetchedAt.UnixMilli(), s.Source.Name(), string(body))
	if err != nil {
		return Observation{}, fmt.Errorf("store observation: %w", err)
	}
	obsID, _ := res.LastInsertId()

	if _, err := tx.ExecContext(ctx,
		`UPDATE locations SET last_fetched_at = ?, last_error = NULL, timezone = ? WHERE id = ?`,
		fetchedAt.UnixMilli(), loc.Timezone, locationID); err != nil {
		return Observation{}, fmt.Errorf("update location: %w", err)
	}

	if _, err := outbox.Enqueue(ctx, tx, contracts.TargetProcessing, contracts.EventWeatherUpdated, contracts.WeatherUpdated{
		ObservationID: obsID,
		Location:      loc.Location,
		Source:        s.Source.Name(),
		FetchedAt:     fetchedAt,
		Snapshot:      snap,
	}); err != nil {
		return Observation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Observation{}, fmt.Errorf("commit: %w", err)
	}

	if s.Dispatcher != nil {
		s.Dispatcher.Notify()
	}
	return Observation{ID: obsID, LocationID: locationID, FetchedAt: fetchedAt, Source: s.Source.Name(), Snapshot: snap}, nil
}

// recordFailure notes the error on the location; failing to record it must not
// mask the original error, so it is only logged.
func (s *Service) recordFailure(ctx context.Context, id int64, cause error) {
	msg := cause.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if _, err := s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE locations SET last_error = ? WHERE id = ?`, msg, id); err != nil {
		s.log().Error("record fetch failure", "location_id", id, "err", err)
	}
}

// RefreshAll refreshes every active location one after another and reports how
// many succeeded and failed. One failing location never stops the others.
func (s *Service) RefreshAll(ctx context.Context) (ok, failed int) {
	locs, err := s.ListLocations(ctx)
	if err != nil {
		s.log().Error("list locations", "err", err)
		return 0, 0
	}
	for i, l := range locs {
		if ctx.Err() != nil {
			return ok, failed
		}
		if _, err := s.Refresh(ctx, l.ID); err != nil {
			failed++
			s.log().Warn("refresh failed", "location_id", l.ID, "location", l.Name, "err", err)
		} else {
			ok++
		}
		if s.RequestGap > 0 && i < len(locs)-1 {
			select {
			case <-ctx.Done():
				return ok, failed
			case <-time.After(s.RequestGap):
			}
		}
	}
	return ok, failed
}

// Poll refreshes all locations immediately and then every interval, purging old
// observations after each cycle, until ctx is cancelled.
func (s *Service) Poll(ctx context.Context, interval time.Duration) {
	cycle := func() {
		ok, failed := s.RefreshAll(ctx)
		s.log().Info("poll cycle finished", "ok", ok, "failed", failed)
		if n, err := s.PurgeObservations(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("purge observations", "err", err)
		} else if n > 0 {
			s.log().Info("purged old observations", "count", n)
		}
	}

	cycle()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cycle()
		}
	}
}

// ---- simulation control (only meaningful when Source is *Simulated) ----

const (
	scenarioKey       = "scenario"
	scenarioKeyPrefix = "scenario:"
)

// SimulationEnabled reports whether the active source is the simulator.
func (s *Service) SimulationEnabled() bool {
	_, ok := s.Source.(*Simulated)
	return ok
}

func (s *Service) defaultScenario() Scenario {
	if s.DefaultScenario != "" {
		return s.DefaultScenario
	}
	return ScenarioNormal
}

// ScenarioFor implements ScenarioLookup: a per-location override wins over the
// global scenario, which wins over the configured default.
func (s *Service) ScenarioFor(ctx context.Context, locationID int64) (Scenario, error) {
	for _, key := range []string{scenarioKeyPrefix + strconv.FormatInt(locationID, 10), scenarioKey} {
		v, found, err := s.getSetting(ctx, key)
		if err != nil {
			return "", err
		}
		if found {
			return ParseScenario(v)
		}
	}
	return s.defaultScenario(), nil
}

// SimulationState is the current scenario configuration.
type SimulationState struct {
	Scenario  Scenario           `json:"scenario"`
	Overrides map[int64]Scenario `json:"overrides"`
}

// Simulation returns the global scenario and any per-location overrides.
func (s *Service) Simulation(ctx context.Context) (SimulationState, error) {
	st := SimulationState{Scenario: s.defaultScenario(), Overrides: map[int64]Scenario{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings WHERE key = ? OR key LIKE ?`, scenarioKey, scenarioKeyPrefix+"%")
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		sc, err := ParseScenario(v)
		if err != nil {
			continue
		}
		if k == scenarioKey {
			st.Scenario = sc
		} else if id, err := strconv.ParseInt(strings.TrimPrefix(k, scenarioKeyPrefix), 10, 64); err == nil {
			st.Overrides[id] = sc
		}
	}
	return st, rows.Err()
}

// SetScenario changes the simulated weather. With a nil locationID it applies to
// every location (and clears per-location overrides, so "everything is extreme"
// really means everything); otherwise only to that location.
func (s *Service) SetScenario(ctx context.Context, locationID *int64, sc Scenario) error {
	if !s.SimulationEnabled() {
		return errors.New("weather source is not the simulator")
	}
	if locationID == nil {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key LIKE ?`, scenarioKeyPrefix+"%"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			scenarioKey, string(sc)); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := s.GetLocation(ctx, *locationID); err != nil {
		return err
	}
	return s.setSetting(ctx, scenarioKeyPrefix+strconv.FormatInt(*locationID, 10), string(sc))
}

// FetchCounts returns how many source fetches succeeded and failed since start.
func (s *Service) FetchCounts() (ok, failed int64) { return s.fetchOK.Load(), s.fetchFail.Load() }
