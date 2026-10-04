package weather

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/engine"
)

type fixedScenario Scenario

func (f fixedScenario) ScenarioFor(context.Context, int64) (Scenario, error) { return Scenario(f), nil }

type failingScenario struct{}

func (failingScenario) ScenarioFor(context.Context, int64) (Scenario, error) {
	return "", errors.New("db down")
}

var mumbai = contracts.Location{ID: 1, Name: "Mumbai", Latitude: 19.076, Longitude: 72.8777, Timezone: "Asia/Kolkata"}

// 09:00 UTC = 14:30 in Mumbai.
func noon() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) }

func simulate(t *testing.T, s *Simulated, loc contracts.Location) contracts.RawSnapshot {
	t.Helper()
	snap, err := s.Fetch(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func levels(snap contracts.RawSnapshot, fromDay int) []engine.RiskLevel {
	var app, zeros []float64
	for i := range snap.Daily.Time {
		app = append(app, *snap.Daily.ApparentTemperatureMax[i])
		zeros = append(zeros, 0)
	}
	fc := engine.BuildDailyRiskForecast(engine.DailyWeather{
		Time: snap.Daily.Time, Temperature2mMax: zeros, Temperature2mMin: zeros,
		ApparentTemperatureMax: app, UVIndexMax: zeros, PrecipitationSum: zeros,
	})
	var out []engine.RiskLevel
	for _, d := range fc[fromDay:] {
		out = append(out, d.RiskLevel)
	}
	return out
}

func highest(ls []engine.RiskLevel) engine.RiskLevel {
	top := engine.Normal
	for _, l := range ls {
		if l.Severity() > top.Severity() {
			top = l
		}
	}
	return top
}

func TestSimulatedShapeAndDeterminism(t *testing.T) {
	s := &Simulated{Scenarios: fixedScenario(ScenarioBuilding), Now: noon}
	a, b := simulate(t, s, mumbai), simulate(t, s, mumbai)

	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Error("same inputs produced different snapshots")
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(a.Daily.Time) != 10 || len(a.Hourly.Time) != 240 {
		t.Fatalf("got %d days / %d hours, want 3 past + 7 forecast = 10 / 240", len(a.Daily.Time), len(a.Hourly.Time))
	}
	if a.Daily.Time[0] != "2026-04-28" || a.Daily.Time[3] != "2026-05-01" || a.Hourly.Time[0] != "2026-04-28T00:00" {
		t.Errorf("time axes wrong: daily %v, first hour %s", a.Daily.Time, a.Hourly.Time[0])
	}
	if a.Current.Time != "2026-05-01T14:30" {
		t.Errorf("current.time = %s, want 2026-05-01T14:30 (Mumbai local)", a.Current.Time)
	}
	if a.Timezone != "Asia/Kolkata" {
		t.Errorf("timezone = %s", a.Timezone)
	}
	for _, v := range []*float64{a.Current.Temperature2m, a.Current.RelativeHumidity2m, a.Current.ApparentTemperature, a.Current.WindSpeed10m} {
		if v == nil {
			t.Error("current value is nil; current must never be blanked")
		}
	}
}

func TestSimulatedScenariosProduceTheAdvertisedRisk(t *testing.T) {
	cases := []struct {
		scenario         Scenario
		wantPeak         engine.RiskLevel
		wantTodayAtLeast engine.RiskLevel
		wantTodayBelow   engine.RiskLevel // exclusive; "" = no upper bound
	}{
		{ScenarioNormal, engine.Normal, engine.Normal, ""},
		{ScenarioBuilding, engine.ExtremeDanger, engine.Caution, engine.Danger},
		{ScenarioExtreme, engine.ExtremeDanger, engine.ExtremeDanger, ""},
	}
	for _, c := range cases {
		snap := simulate(t, &Simulated{Scenarios: fixedScenario(c.scenario), Now: noon}, mumbai)
		forecast := levels(snap, 3) // from today onward, like the dashboard
		if got := highest(forecast); got != c.wantPeak {
			t.Errorf("%s: peak = %s, want %s (%v)", c.scenario, got, c.wantPeak, forecast)
		}
		today := forecast[0]
		if today.Severity() < c.wantTodayAtLeast.Severity() {
			t.Errorf("%s: today = %s, want at least %s", c.scenario, today, c.wantTodayAtLeast)
		}
		if c.wantTodayBelow != "" && today.Severity() >= c.wantTodayBelow.Severity() {
			t.Errorf("%s: today = %s, want below %s", c.scenario, today, c.wantTodayBelow)
		}
	}

	// A heatwave means "danger" (2+ consecutive hot days) appears, not just one hot day.
	building := levels(simulate(t, &Simulated{Scenarios: fixedScenario(ScenarioBuilding), Now: noon}, mumbai), 3)
	dangerDays := 0
	for _, l := range building {
		if l == engine.Danger || l == engine.ExtremeDanger {
			dangerDays++
		}
	}
	if dangerDays < 2 {
		t.Errorf("building scenario has %d danger days, want a multi-day heatwave", dangerDays)
	}
}

func TestSimulatedNullRate(t *testing.T) {
	count := func(rate float64) (nulls, total int) {
		snap := simulate(t, &Simulated{Scenarios: fixedScenario(ScenarioBuilding), Now: noon, NullRate: rate}, mumbai)
		for _, series := range [][]*float64{
			snap.Hourly.Temperature2m, snap.Hourly.RelativeHumidity2m, snap.Hourly.ApparentTemperature, snap.Hourly.WindSpeed10m,
			snap.Daily.Temperature2mMax, snap.Daily.ApparentTemperatureMax,
		} {
			for _, v := range series {
				total++
				if v == nil {
					nulls++
				}
			}
		}
		if snap.Current.Temperature2m == nil {
			t.Error("current temperature was blanked")
		}
		return
	}
	if n, _ := count(0); n != 0 {
		t.Errorf("rate 0 produced %d nulls", n)
	}
	n, total := count(0.05)
	if frac := float64(n) / float64(total); frac < 0.02 || frac > 0.09 {
		t.Errorf("rate 0.05 blanked %.3f of samples", frac)
	}
}

func TestSimulatedTimezones(t *testing.T) {
	// 20:00 UTC on May 1 is already May 2 in Kolkata, still May 1 in UTC.
	now := func() time.Time { return time.Date(2026, 5, 1, 20, 0, 0, 0, time.UTC) }
	s := &Simulated{Scenarios: fixedScenario(ScenarioNormal), Now: now}

	if got := simulate(t, s, mumbai).Current.Time; got != "2026-05-02T01:30" {
		t.Errorf("Kolkata current.time = %s", got)
	}
	utc := mumbai
	utc.Timezone = ""
	snap := simulate(t, s, utc)
	if snap.Current.Time != "2026-05-01T20:00" || snap.Timezone != "UTC" {
		t.Errorf("empty timezone should fall back to UTC, got %s / %s", snap.Current.Time, snap.Timezone)
	}
	bad := mumbai
	bad.Timezone = "Mars/Olympus"
	if got := simulate(t, s, bad).Current.Time; got != "2026-05-01T20:00" {
		t.Errorf("invalid timezone should fall back to UTC, got %s", got)
	}
}

func TestSimulatedLocationsDiffer(t *testing.T) {
	s := &Simulated{Scenarios: fixedScenario(ScenarioBuilding), Now: noon}
	delhi := contracts.Location{ID: 2, Name: "Delhi", Latitude: 28.6139, Longitude: 77.209, Timezone: "Asia/Kolkata"}
	a, b := simulate(t, s, mumbai), simulate(t, s, delhi)
	if *a.Current.Temperature2m == *b.Current.Temperature2m {
		t.Error("two cities produced identical temperatures")
	}
}

func TestSimulatedPropagatesScenarioLookupFailure(t *testing.T) {
	s := &Simulated{Scenarios: failingScenario{}, Now: noon}
	_, err := s.Fetch(context.Background(), mumbai)
	var serr *SourceError
	if !errors.As(err, &serr) {
		t.Fatalf("err = %v, want SourceError", err)
	}
}

func TestParseScenario(t *testing.T) {
	for _, sc := range Scenarios {
		if got, err := ParseScenario(string(sc)); err != nil || got != sc {
			t.Errorf("ParseScenario(%q) = %v, %v", sc, got, err)
		}
	}
	var verr ValidationError
	if _, err := ParseScenario("apocalypse"); !errors.As(err, &verr) {
		t.Errorf("want ValidationError, got %v", err)
	}
}
