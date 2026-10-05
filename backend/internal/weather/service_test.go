package weather

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/events"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/sqlitex"
)

// fakeSource returns a valid simulated snapshot unless fn says otherwise.
type fakeSource struct {
	mu    sync.Mutex
	calls int
	fn    func(contracts.Location) (contracts.RawSnapshot, error)
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) Fetch(ctx context.Context, loc contracts.Location) (contracts.RawSnapshot, error) {
	f.mu.Lock()
	f.calls++
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(loc)
	}
	return (&Simulated{Scenarios: fixedScenario(ScenarioNormal), Now: noon}).Fetch(ctx, loc)
}

func (f *fakeSource) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func newService(t *testing.T, src Source) *Service {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	return &Service{DB: db, Source: src, Clock: func() time.Time { return clock }, Retention: 24 * time.Hour}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func addLoc(t *testing.T, s *Service, name string, lat, lon float64) Location {
	t.Helper()
	l, _, err := s.AddLocation(context.Background(), NewLocation{Name: name, Latitude: lat, Longitude: lon, Timezone: "Asia/Kolkata"})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSeedOnlyOnEmptyDatabase(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	n, err := s.Seed(ctx)
	if err != nil || n != len(defaultSeed) {
		t.Fatalf("Seed = %d, %v", n, err)
	}
	if n, _ := s.Seed(ctx); n != 0 {
		t.Errorf("second Seed added %d", n)
	}

	// An operator removing cities must not see them return on restart — even
	// when every one has been removed (no active rows left, but history exists).
	locs, _ := s.ListLocations(ctx)
	if err := s.RemoveLocation(ctx, locs[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Seed(ctx); n != 0 {
		t.Error("Seed resurrected a removed location")
	}
	for _, l := range locs[1:] {
		if err := s.RemoveLocation(ctx, l.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.Seed(ctx); n != 0 {
		t.Error("Seed repopulated after every location was removed")
	}
	if got, _ := s.ListLocations(ctx); len(got) != 0 {
		t.Errorf("have %d locations, want 0", len(got))
	}
}

func TestAddLocationValidation(t *testing.T) {
	s := newService(t, &fakeSource{})
	long := make([]rune, 101)
	for i := range long {
		long[i] = 'x'
	}
	bad := map[string]NewLocation{
		"empty name":   {Name: "  ", Latitude: 1, Longitude: 1},
		"long name":    {Name: string(long), Latitude: 1, Longitude: 1},
		"lat too high": {Name: "x", Latitude: 90.1, Longitude: 1},
		"lat too low":  {Name: "x", Latitude: -91, Longitude: 1},
		"lon too high": {Name: "x", Latitude: 1, Longitude: 180.5},
		"lon too low":  {Name: "x", Latitude: 1, Longitude: -181},
	}
	for name, in := range bad {
		_, _, err := s.AddLocation(context.Background(), in)
		var verr ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
	if _, _, err := s.AddLocation(context.Background(), NewLocation{Name: "edge", Latitude: 90, Longitude: -180}); err != nil {
		t.Errorf("boundary coordinates rejected: %v", err)
	}
}

func TestAddLocationDedupesByCellAndReactivates(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	a, created, _ := s.AddLocation(ctx, NewLocation{Name: "Mumbai", Latitude: 19.076, Longitude: 72.8777})
	if !created {
		t.Fatal("first add should create")
	}
	b, created, _ := s.AddLocation(ctx, NewLocation{Name: "Bombay", Latitude: 19.0761, Longitude: 72.8779})
	if created || b.ID != a.ID || b.Name != "Mumbai" {
		t.Errorf("nearby add = %+v created=%v, want the existing Mumbai record", b, created)
	}

	if err := s.RemoveLocation(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	c, created, _ := s.AddLocation(ctx, NewLocation{Name: "Mumbai", Latitude: 19.076, Longitude: 72.8777})
	if created || c.ID != a.ID || !c.Active {
		t.Errorf("re-add = %+v created=%v, want reactivation of id %d", c, created, a.ID)
	}
}

func TestGeoKeyNormalisesNegativeZero(t *testing.T) {
	if GeoKey(-0.001, 0.001) != GeoKey(0.001, -0.001) || GeoKey(-0.001, 0.001) != "0.00,0.00" {
		t.Errorf("GeoKey(-0.001, 0.001) = %q", GeoKey(-0.001, 0.001))
	}
	if GeoKey(19.076, 72.8777) != "19.08,72.88" {
		t.Errorf("GeoKey = %q", GeoKey(19.076, 72.8777))
	}
}

func TestMaxLocationsCountsOnlyActive(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	s.MaxLocations = 2
	a := addLoc(t, s, "a", 1, 1)
	addLoc(t, s, "b", 2, 2)
	if _, _, err := s.AddLocation(ctx, NewLocation{Name: "c", Latitude: 3, Longitude: 3}); !errors.Is(err, ErrTooManyLocations) {
		t.Fatalf("err = %v, want ErrTooManyLocations", err)
	}
	// Re-adding an existing active place is not "another location".
	if _, _, err := s.AddLocation(ctx, NewLocation{Name: "a", Latitude: 1, Longitude: 1}); err != nil {
		t.Errorf("re-adding an existing location hit the limit: %v", err)
	}
	s.RemoveLocation(ctx, a.ID)
	if _, _, err := s.AddLocation(ctx, NewLocation{Name: "c", Latitude: 3, Longitude: 3}); err != nil {
		t.Errorf("removal did not free a slot: %v", err)
	}
}

func TestRefreshStoresObservationAndQueuesEvent(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	loc, _, _ := s.AddLocation(ctx, NewLocation{Name: "Mumbai", Latitude: 19.076, Longitude: 72.8777}) // no timezone yet

	obs, err := s.Refresh(ctx, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ID == 0 || obs.Source != "fake" {
		t.Errorf("observation = %+v", obs)
	}

	got, err := s.LatestObservation(ctx, loc.ID)
	if err != nil || got.ID != obs.ID || got.Snapshot.Current.Time != obs.Snapshot.Current.Time {
		t.Fatalf("LatestObservation = %+v, %v", got, err)
	}

	after, _ := s.GetLocation(ctx, loc.ID)
	if after.LastFetchedAt == nil || after.LastError != "" {
		t.Errorf("location status = %+v", after)
	}
	if after.Timezone == "" {
		t.Error("timezone resolved by the source was not saved")
	}

	var target, typ, payload string
	if err := s.DB.QueryRow(`SELECT target, type, payload FROM outbox`).Scan(&target, &typ, &payload); err != nil {
		t.Fatalf("no outbox event: %v", err)
	}
	if target != contracts.TargetProcessing || typ != contracts.EventWeatherUpdated {
		t.Errorf("event routed as %s/%s", target, typ)
	}
	var ev contracts.WeatherUpdated
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ObservationID != obs.ID || ev.Location.ID != loc.ID || ev.Location.Name != "Mumbai" ||
		ev.Location.Timezone != after.Timezone || ev.Source != "fake" || ev.Snapshot.Validate() != nil {
		t.Errorf("payload = %+v", ev)
	}
}

func TestRefreshFailureRecordsErrorAndSendsNothing(t *testing.T) {
	src := &fakeSource{fn: func(contracts.Location) (contracts.RawSnapshot, error) {
		return contracts.RawSnapshot{}, &SourceError{Status: 429, Msg: "Too many requests"}
	}}
	s, ctx := newService(t, src), context.Background()
	loc := addLoc(t, s, "Mumbai", 19.076, 72.8777)

	_, err := s.Refresh(ctx, loc.ID)
	var serr *SourceError
	if !errors.As(err, &serr) || serr.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if count(t, s.DB, `SELECT COUNT(*) FROM observations`) != 0 || count(t, s.DB, `SELECT COUNT(*) FROM outbox`) != 0 {
		t.Error("a failed fetch left an observation or event behind")
	}
	if l, _ := s.GetLocation(ctx, loc.ID); l.LastError == "" || l.LastFetchedAt != nil {
		t.Errorf("failure not recorded: %+v", l)
	}
	if ok, failed := s.FetchCounts(); ok != 0 || failed != 1 {
		t.Errorf("counts = %d ok / %d failed", ok, failed)
	}

	// Recovery clears the error.
	src.mu.Lock()
	src.fn = nil
	src.mu.Unlock()
	if _, err := s.Refresh(ctx, loc.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.GetLocation(ctx, loc.ID); l.LastError != "" || l.LastFetchedAt == nil {
		t.Errorf("error not cleared after recovery: %+v", l)
	}
}

func TestRefreshRollsBackWhenEventCannotBeQueued(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	loc := addLoc(t, s, "Mumbai", 19.076, 72.8777)
	if _, err := s.DB.Exec(`DROP TABLE outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, loc.ID); err == nil {
		t.Fatal("expected an error")
	}
	if n := count(t, s.DB, `SELECT COUNT(*) FROM observations`); n != 0 {
		t.Errorf("observation committed without its event (%d rows): data would never reach processing", n)
	}
	if l, _ := s.GetLocation(ctx, loc.ID); l.LastFetchedAt != nil {
		t.Error("location marked as fetched although the transaction failed")
	}
}

func TestRefreshUnknownOrRemovedLocation(t *testing.T) {
	src := &fakeSource{}
	s, ctx := newService(t, src), context.Background()
	if _, err := s.Refresh(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	loc := addLoc(t, s, "x", 1, 1)
	s.RemoveLocation(ctx, loc.ID)
	if _, err := s.Refresh(ctx, loc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed: %v", err)
	}
	if src.callCount() != 0 {
		t.Error("source was called for a location that is not being watched")
	}
}

func TestRefreshAllIsolatesFailures(t *testing.T) {
	src := &fakeSource{fn: func(loc contracts.Location) (contracts.RawSnapshot, error) {
		if loc.Name == "bad" {
			return contracts.RawSnapshot{}, &SourceError{Status: 500, Msg: "boom"}
		}
		return (&Simulated{Scenarios: fixedScenario(ScenarioNormal), Now: noon}).Fetch(context.Background(), loc)
	}}
	s := newService(t, src)
	addLoc(t, s, "alpha", 1, 1)
	addLoc(t, s, "bad", 2, 2)
	addLoc(t, s, "zulu", 3, 3)

	ok, failed := s.RefreshAll(context.Background())
	if ok != 2 || failed != 1 {
		t.Errorf("RefreshAll = %d ok / %d failed, want 2 / 1", ok, failed)
	}
	if n := count(t, s.DB, `SELECT COUNT(*) FROM outbox`); n != 2 {
		t.Errorf("%d events queued, want 2", n)
	}
}

func TestRefreshAllStopsWhenContextCancelled(t *testing.T) {
	src := &fakeSource{}
	s := newService(t, src)
	s.RequestGap = time.Hour // would block forever without cancellation support
	addLoc(t, s, "a", 1, 1)
	addLoc(t, s, "b", 2, 2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RefreshAll(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshAll ignored cancellation during the request gap")
	}
	if src.callCount() != 1 {
		t.Errorf("fetched %d locations, want 1", src.callCount())
	}
}

func TestPollRunsImmediatelyAndStops(t *testing.T) {
	src := &fakeSource{}
	s := newService(t, src)
	addLoc(t, s, "a", 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Poll(ctx, time.Hour); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for src.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if src.callCount() != 1 {
		t.Fatal("Poll did not fetch immediately on start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not stop")
	}
}

func TestLatestObservationReturnsNewestAndPurgeKeepsRecent(t *testing.T) {
	s, ctx := newService(t, &fakeSource{}), context.Background()
	loc := addLoc(t, s, "a", 1, 1)
	first, _ := s.Refresh(ctx, loc.ID)
	second, _ := s.Refresh(ctx, loc.ID)

	got, _ := s.LatestObservation(ctx, loc.ID)
	if got.ID != second.ID {
		t.Errorf("latest = %d, want %d", got.ID, second.ID)
	}

	old := s.Clock().Add(-48 * time.Hour).UnixMilli()
	s.DB.Exec(`UPDATE observations SET fetched_at = ? WHERE id = ?`, old, first.ID)
	n, err := s.PurgeObservations(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if count(t, s.DB, `SELECT COUNT(*) FROM observations`) != 1 {
		t.Error("purge removed a recent observation")
	}
	if _, err := s.LatestObservation(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown location: %v", err)
	}
}

func TestScenarioSettingsPrecedence(t *testing.T) {
	s, ctx := newService(t, nil), context.Background()
	s.Source = &Simulated{Scenarios: s, Now: noon}
	s.DefaultScenario = ScenarioBuilding
	a, b := addLoc(t, s, "a", 1, 1), addLoc(t, s, "b", 2, 2)

	at := func(id int64) Scenario {
		t.Helper()
		sc, err := s.ScenarioFor(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}
	if at(a.ID) != ScenarioBuilding {
		t.Error("default scenario not used")
	}

	if err := s.SetScenario(ctx, nil, ScenarioExtreme); err != nil {
		t.Fatal(err)
	}
	if at(a.ID) != ScenarioExtreme || at(b.ID) != ScenarioExtreme {
		t.Error("global scenario not applied")
	}

	if err := s.SetScenario(ctx, &a.ID, ScenarioNormal); err != nil {
		t.Fatal(err)
	}
	if at(a.ID) != ScenarioNormal || at(b.ID) != ScenarioExtreme {
		t.Error("per-location override should win for a only")
	}
	st, _ := s.Simulation(ctx)
	if st.Scenario != ScenarioExtreme || st.Overrides[a.ID] != ScenarioNormal {
		t.Errorf("state = %+v", st)
	}

	// Setting the global scenario again clears overrides.
	s.SetScenario(ctx, nil, ScenarioBuilding)
	if at(a.ID) != ScenarioBuilding {
		t.Error("global set did not clear the per-location override")
	}

	missing := int64(999)
	if err := s.SetScenario(ctx, &missing, ScenarioNormal); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown location: %v", err)
	}
}

func TestSetScenarioRequiresSimulatedSource(t *testing.T) {
	s := newService(t, &fakeSource{})
	if s.SimulationEnabled() {
		t.Fatal("fake source reported as simulator")
	}
	if err := s.SetScenario(context.Background(), nil, ScenarioExtreme); err == nil {
		t.Error("SetScenario succeeded on a non-simulated source")
	}
}

func TestEventReachesDownstreamThroughDispatcher(t *testing.T) {
	var (
		mu  sync.Mutex
		got []events.Event
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev events.Event
		json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s, ctx := newService(t, &fakeSource{}), context.Background()
	s.Dispatcher = &outbox.Dispatcher{DB: s.DB, Source: "weather", Targets: map[string]string{contracts.TargetProcessing: srv.URL}}
	loc := addLoc(t, s, "Mumbai", 19.076, 72.8777)
	if _, err := s.Refresh(ctx, loc.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Dispatcher.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("DispatchOnce = %d, %v", n, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Type != contracts.EventWeatherUpdated || got[0].Source != "weather" {
		t.Fatalf("downstream received %+v", got)
	}
	var payload contracts.WeatherUpdated
	if err := got[0].Decode(&payload); err != nil || payload.Location.ID != loc.ID {
		t.Errorf("payload = %+v, %v", payload, err)
	}
}
