package weather

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/engine"
)

// Scenario selects the weather a Simulated source produces.
type Scenario string

const (
	// ScenarioNormal: comfortable weather, risk stays Normal.
	ScenarioNormal Scenario = "normal"
	// ScenarioBuilding: heat ramps up from Caution through a multi-day Danger
	// heatwave, peaking at Extreme Danger in the forecast. Today is still below Danger.
	ScenarioBuilding Scenario = "building"
	// ScenarioExtreme: record-level heat, Extreme Danger from today onward.
	ScenarioExtreme Scenario = "extreme"
)

// Scenarios lists the valid scenarios.
var Scenarios = []Scenario{ScenarioNormal, ScenarioBuilding, ScenarioExtreme}

// ParseScenario validates a scenario name.
func ParseScenario(s string) (Scenario, error) {
	for _, v := range Scenarios {
		if string(v) == s {
			return v, nil
		}
	}
	return "", ValidationError(fmt.Sprintf("unknown scenario %q (want normal, building or extreme)", s))
}

// ScenarioLookup tells the simulator which scenario applies to a location, so
// the choice can be changed at runtime and survive restarts.
type ScenarioLookup interface {
	ScenarioFor(ctx context.Context, locationID int64) (Scenario, error)
}

// Simulated generates deterministic synthetic weather. It exists so demos and
// CI never depend on the network or on there actually being a heatwave.
type Simulated struct {
	Scenarios ScenarioLookup
	Now       func() time.Time // defaults to time.Now
	PastDays  int              // history days before today; default 3, max 3
	// NullRate (0–1) blanks that fraction of hourly and daily samples, imitating
	// the gaps real APIs return, so the processing service's cleaning is exercised.
	NullRate float64
}

func (s *Simulated) Name() string { return "simulated" }

// Daily profiles span 10 days: three past days, today (index 3), six more.
type profile struct {
	tmax    [10]float64
	spread  float64 // tmax - tmin
	rhMean  float64
	uv      float64
	rainy   bool
	weather float64 // WMO code for "current"
}

var profiles = map[Scenario]profile{
	ScenarioNormal: {
		tmax: [10]float64{28, 29, 28.5, 29, 30, 29, 28, 27.5, 28, 29}, spread: 7, rhMean: 55, uv: 7, rainy: true, weather: 3,
	},
	ScenarioBuilding: {
		tmax: [10]float64{33, 35, 37, 39, 41, 43, 44, 45, 43, 41}, spread: 9, rhMean: 40, uv: 10, weather: 0,
	},
	ScenarioExtreme: {
		tmax: [10]float64{42, 45, 47, 49, 50, 51, 52, 51, 49, 47}, spread: 10, rhMean: 30, uv: 12, weather: 0,
	},
}

func (s *Simulated) Fetch(ctx context.Context, loc contracts.Location) (contracts.RawSnapshot, error) {
	scenario := ScenarioNormal
	if s.Scenarios != nil {
		sc, err := s.Scenarios.ScenarioFor(ctx, loc.ID)
		if err != nil {
			return contracts.RawSnapshot{}, &SourceError{Msg: "scenario lookup: " + err.Error()}
		}
		scenario = sc
	}
	prof, ok := profiles[scenario]
	if !ok {
		return contracts.RawSnapshot{}, &SourceError{Msg: "unknown scenario " + string(scenario)}
	}

	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	tz := time.UTC
	if l, err := time.LoadLocation(loc.Timezone); err == nil && loc.Timezone != "" {
		tz = l
	}
	now = now.In(tz)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)

	past := s.PastDays
	if past == 0 {
		past = 3
	}
	past = max(0, min(past, 3))
	const forecastDays = 7
	offset := locationOffset(loc)

	snap := contracts.RawSnapshot{Latitude: loc.Latitude, Longitude: loc.Longitude, Timezone: loc.Timezone}
	if snap.Timezone == "" {
		snap.Timezone = "UTC"
	}

	tmaxOn := func(d int) float64 { return prof.tmax[max(0, min(9, 3+d))] + offset }
	// state returns temperature (°C) and relative humidity (%) at a fractional hour of day offset d.
	state := func(d int, hour float64) (temp, rh float64) {
		tmax := tmaxOn(d)
		tmin := tmax - prof.spread
		mean, amp := (tmax+tmin)/2, (tmax-tmin)/2
		temp = mean + amp*math.Cos(2*math.Pi*(hour-15)/24) // warmest at 15:00
		rh = math.Max(8, math.Min(100, prof.rhMean-2*(temp-mean)))
		return
	}
	apparent := func(temp, rh float64) float64 { return engine.HeatIndex(temp, rh) }

	for d := -past; d < forecastDays; d++ {
		date := today.AddDate(0, 0, d)
		dayMaxApparent := math.Inf(-1)
		for h := 0; h < 24; h++ {
			temp, rh := state(d, float64(h))
			app := apparent(temp, rh)
			dayMaxApparent = math.Max(dayMaxApparent, app)
			idx := len(snap.Hourly.Time)
			snap.Hourly.Time = append(snap.Hourly.Time, date.Add(time.Duration(h)*time.Hour).Format("2006-01-02T15:04"))
			snap.Hourly.Temperature2m = append(snap.Hourly.Temperature2m, sample(s.NullRate, loc.ID, 1, idx, round1(temp)))
			snap.Hourly.RelativeHumidity2m = append(snap.Hourly.RelativeHumidity2m, sample(s.NullRate, loc.ID, 2, idx, math.Round(rh)))
			snap.Hourly.ApparentTemperature = append(snap.Hourly.ApparentTemperature, sample(s.NullRate, loc.ID, 3, idx, round1(app)))
			snap.Hourly.WindSpeed10m = append(snap.Hourly.WindSpeed10m, sample(s.NullRate, loc.ID, 4, idx, round1(wind(d, float64(h)))))
		}

		idx := len(snap.Daily.Time)
		tmax := tmaxOn(d)
		precip := 0.0
		if prof.rainy && (d+10)%4 == 2 {
			precip = 2.5
		}
		snap.Daily.Time = append(snap.Daily.Time, date.Format("2006-01-02"))
		snap.Daily.Temperature2mMax = append(snap.Daily.Temperature2mMax, sample(s.NullRate, loc.ID, 5, idx, round1(tmax)))
		snap.Daily.Temperature2mMin = append(snap.Daily.Temperature2mMin, sample(s.NullRate, loc.ID, 6, idx, round1(tmax-prof.spread)))
		snap.Daily.ApparentTemperatureMax = append(snap.Daily.ApparentTemperatureMax, sample(s.NullRate, loc.ID, 7, idx, round1(dayMaxApparent)))
		snap.Daily.UVIndexMax = append(snap.Daily.UVIndexMax, sample(s.NullRate, loc.ID, 8, idx, prof.uv))
		snap.Daily.PrecipitationSum = append(snap.Daily.PrecipitationSum, sample(s.NullRate, loc.ID, 9, idx, precip))
	}

	// Current conditions at the last quarter hour; never blanked (see NullRate).
	minute := now.Minute() / 15 * 15
	hour := float64(now.Hour()) + float64(minute)/60
	temp, rh := state(0, hour)
	snap.Current = contracts.RawCurrent{
		Time:                   time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), minute, 0, 0, tz).Format("2006-01-02T15:04"),
		Temperature2m:          ptr(round1(temp)),
		RelativeHumidity2m:     ptr(math.Round(rh)),
		ApparentTemperature:    ptr(round1(apparent(temp, rh))),
		WeatherCode:            ptr(prof.weather),
		WindSpeed10m:           ptr(round1(wind(0, hour))),
		DirectNormalIrradiance: ptr(round1(irradiance(hour, prof.uv))),
	}

	if err := snap.Validate(); err != nil {
		return contracts.RawSnapshot{}, &SourceError{Msg: "simulator produced an invalid snapshot: " + err.Error()}
	}
	return snap, nil
}

func ptr(v float64) *float64 { return &v }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func sample(rate float64, locID int64, series, i int, v float64) *float64 {
	if nullHit(rate, locID, series, i) {
		return nil
	}
	return &v
}

// nullHit deterministically decides whether sample i of a series is blanked.
func nullHit(rate float64, locID int64, series, i int) bool {
	if rate <= 0 {
		return false
	}
	h := uint64(locID+1)*2654435761 + uint64(series)*40503 + uint64(i)*2246822519
	h ^= h >> 15
	h *= 0x2c1b3c6d
	h ^= h >> 12
	h *= 0x297a2d39
	h ^= h >> 15
	return float64(h%10000)/10000 < rate
}

// locationOffset gives each place a stable ±1°C difference so cities don't all
// show identical numbers.
func locationOffset(loc contracts.Location) float64 {
	n := int(math.Abs(loc.Latitude*100+loc.Longitude*10)) % 21
	return float64(n-10) / 10
}

func wind(day int, hour float64) float64 {
	return math.Max(0, 7+3*math.Sin(2*math.Pi*hour/24+float64(day)))
}

// irradiance is a daylight bell curve between 06:00 and 18:00 scaled by UV.
func irradiance(hour, uv float64) float64 {
	if hour < 6 || hour > 18 {
		return 0
	}
	return uv * 80 * math.Sin(math.Pi*(hour-6)/12)
}
