package processing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/weather"
)

type fixedScenario weather.Scenario

func (f fixedScenario) ScenarioFor(context.Context, int64) (weather.Scenario, error) {
	return weather.Scenario(f), nil
}

var testLoc = contracts.Location{ID: 1, Name: "Mumbai", Latitude: 19.076, Longitude: 72.8777, Timezone: "Asia/Kolkata"}

// 09:00 UTC = 14:30 in Mumbai. Daily index 3 is today; hourly index 87 is 15:00 today.
func fixedNow() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) }

const (
	todayDay     = 3
	nowHourIndex = 3*24 + 15
)

func sim(t *testing.T, sc weather.Scenario, nullRate float64) contracts.RawSnapshot {
	t.Helper()
	s := &weather.Simulated{Scenarios: fixedScenario(sc), Now: fixedNow, NullRate: nullRate}
	snap, err := s.Fetch(context.Background(), testLoc)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func process(t *testing.T, raw contracts.RawSnapshot) contracts.ProcessedWeather {
	t.Helper()
	out, err := Process(raw)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return out
}

func rejectReason(t *testing.T, raw contracts.RawSnapshot) string {
	t.Helper()
	_, err := Process(raw)
	var rej *RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want RejectError", err)
	}
	return rej.Reason
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func nilOut(s []*float64, idx ...int) {
	for _, i := range idx {
		s[i] = nil
	}
}

func fp(v float64) *float64 { return &v }

func TestCleanSnapshotNeedsNoRepair(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	out := process(t, raw)

	q := out.Quality
	if q.Score != 1 || q.HourlyEstimated != 0 || q.DailyEstimated != 0 || len(q.CurrentEstimated) != 0 || q.OutOfRange != 0 {
		t.Errorf("quality = %+v, want a perfect score", q)
	}
	if q.HourlyTotal != 240 || q.DailyTotal != 10 {
		t.Errorf("totals = %d hours / %d days", q.HourlyTotal, q.DailyTotal)
	}
	if out.Hourly == nil || len(out.Hourly.Time) != 240 || len(out.Hourly.HeatIndexC) != 240 {
		t.Fatal("hourly series missing or wrong length")
	}
	if out.Hourly.CurrentIndex != nowHourIndex {
		t.Errorf("currentIndex = %d, want %d (first hour after 14:30)", out.Hourly.CurrentIndex, nowHourIndex)
	}

	forecast, history := 0, 0
	for _, d := range out.Days {
		if d.Forecast {
			forecast++
		} else {
			history++
		}
	}
	if forecast != 7 || history != 3 || out.Days[todayDay].Date != "2026-05-01" || !out.Days[todayDay].Forecast || out.Days[todayDay-1].Forecast {
		t.Errorf("forecast/history split wrong: %d forecast, %d history", forecast, history)
	}
	// Forecast max temps are 39,41,43,44,45,43,41: the last day is 1.5°C below the
	// mean of the earlier ones. (Including the 3 history days would change this.)
	if out.Trend.IsRising || !near(out.Trend.AnomalyC, -1.5, 0.011) {
		t.Errorf("trend = %+v, want anomaly -1.5 and not rising", out.Trend)
	}
	if out.Current.Time != raw.Current.Time || out.Current.TemperatureC != round2(*raw.Current.Temperature2m) {
		t.Errorf("current = %+v", out.Current)
	}
}

func TestHeatIndexComesFromTheEngine(t *testing.T) {
	out := process(t, sim(t, weather.ScenarioBuilding, 0))
	if want := round2(engine.HeatIndex(out.Current.TemperatureC, out.Current.Humidity)); !near(out.Current.HeatIndexC, want, 0.011) {
		t.Errorf("current heat index = %v, want %v", out.Current.HeatIndexC, want)
	}
	h := out.Hourly
	for _, i := range []int{0, 50, nowHourIndex, 239} {
		if want := round2(engine.HeatIndex(h.TemperatureC[i], h.Humidity[i])); !near(h.HeatIndexC[i], want, 0.011) {
			t.Errorf("hourly[%d] heat index = %v, want ≈%v", i, h.HeatIndexC[i], want)
		}
	}
	for _, d := range out.Days {
		if d.HeatIndexMaxC < d.TempMinC {
			t.Errorf("%s: heat index max %v below the day's minimum temperature", d.Date, d.HeatIndexMaxC)
		}
	}
}

func TestTrendMatchesEngineOverForecastDays(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	out := process(t, raw)
	var forecastMax []float64
	for _, d := range out.Days {
		if d.Forecast {
			forecastMax = append(forecastMax, d.TempMaxC)
		}
	}
	want := engine.ComputeTrendAnomaly(forecastMax)
	if out.Trend.IsRising != want.IsRising || !near(out.Trend.AnomalyC, want.AnomalyC, 0.011) {
		t.Errorf("trend = %+v, want %+v (history days must not count)", out.Trend, want)
	}
}

func TestHourlyGapsAreInterpolatedNotZeroed(t *testing.T) {
	raw := sim(t, weather.ScenarioExtreme, 0)
	before := *raw.Hourly.Temperature2m[99]
	after := *raw.Hourly.Temperature2m[102]
	nilOut(raw.Hourly.Temperature2m, 100, 101)

	out := process(t, raw)
	got := out.Hourly.TemperatureC
	if !near(got[100], before+(after-before)/3, 0.011) || !near(got[101], before+2*(after-before)/3, 0.011) {
		t.Errorf("gap filled with %v, %v; want a straight line from %v to %v", got[100], got[101], before, after)
	}
	if got[100] < 20 {
		t.Errorf("gap became %v — a missing reading must never look like a cold one", got[100])
	}
	if out.Quality.HourlyEstimated != 2 {
		t.Errorf("hourlyEstimated = %d, want 2", out.Quality.HourlyEstimated)
	}
}

func TestGapsAtTheEdgesCarryTheNearestValue(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	first, last := *raw.Hourly.RelativeHumidity2m[3], *raw.Hourly.RelativeHumidity2m[236]
	nilOut(raw.Hourly.RelativeHumidity2m, 0, 1, 2, 237, 238, 239)
	out := process(t, raw)
	h := out.Hourly.Humidity
	if h[0] != round2(first) || h[2] != round2(first) || h[239] != round2(last) || h[237] != round2(last) {
		t.Errorf("edges = %v %v … %v %v, want carry of %v / %v", h[0], h[2], h[237], h[239], first, last)
	}
}

func TestImplausibleValuesAreDiscardedAndCounted(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	good := *raw.Hourly.Temperature2m[50]
	raw.Hourly.Temperature2m[50] = fp(999)      // sensor glitch
	raw.Hourly.RelativeHumidity2m[60] = fp(250) // nonsense
	raw.Hourly.RelativeHumidity2m[61] = fp(103) // small overshoot: clamp, don't discard
	raw.Hourly.WindSpeed10m[70] = fp(-5)

	out := process(t, raw)
	if out.Hourly.TemperatureC[50] > 60 || math.Abs(out.Hourly.TemperatureC[50]-good) > 2 {
		t.Errorf("glitch leaked through: %v (neighbours ≈ %v)", out.Hourly.TemperatureC[50], good)
	}
	if out.Hourly.Humidity[61] != 100 {
		t.Errorf("humidity 103 → %v, want clamped to 100", out.Hourly.Humidity[61])
	}
	if out.Hourly.Humidity[60] == 250 || out.Hourly.WindSpeed[70] < 0 {
		t.Error("implausible value survived")
	}
	if out.Quality.OutOfRange != 3 { // 999, 250, -5 — the clamped 103 is not counted
		t.Errorf("outOfRange = %d, want 3", out.Quality.OutOfRange)
	}
}

func TestRejectsWhenTooMuchIsMissing(t *testing.T) {
	cases := map[string]struct {
		mutate func(*contracts.RawSnapshot)
		detail string // substring the rejection must mention, so each rule is tested for its own reason
	}{
		"all temperature missing": {func(r *contracts.RawSnapshot) {
			for i := range r.Hourly.Temperature2m {
				r.Hourly.Temperature2m[i] = nil
			}
		}, "no usable values"},
		"all humidity missing": {func(r *contracts.RawSnapshot) {
			for i := range r.Hourly.RelativeHumidity2m {
				r.Hourly.RelativeHumidity2m[i] = nil
			}
		}, "no usable values"},
		"60% of temperature missing": {func(r *contracts.RawSnapshot) {
			for i := range r.Hourly.Temperature2m {
				if i%5 < 3 {
					r.Hourly.Temperature2m[i] = nil
				}
			}
		}, "60% missing"},
		"daily max missing everywhere and hourly data cannot cover those dates": {func(r *contracts.RawSnapshot) {
			for i := range r.Daily.Time {
				r.Daily.Time[i] = "2026-06-0" + string(rune('1'+i%9)) // not in the hourly axis
				r.Daily.Temperature2mMax[i] = nil
			}
		}, "daily temperature2mMax"},
	}
	for name, c := range cases {
		raw := sim(t, weather.ScenarioNormal, 0)
		c.mutate(&raw)
		_, err := Process(raw)
		var rej *RejectError
		if !errors.As(err, &rej) || rej.Reason != ReasonTooMuchMissing || !strings.Contains(rej.Detail, c.detail) {
			t.Errorf("%s: err = %v, want %s mentioning %q", name, err, ReasonTooMuchMissing, c.detail)
		}
	}

	// Just under the limit is repaired, not rejected.
	raw := sim(t, weather.ScenarioNormal, 0)
	for i := range raw.Hourly.Temperature2m {
		if i%10 < 4 { // 40% missing
			raw.Hourly.Temperature2m[i] = nil
		}
	}
	if q := process(t, raw).Quality; q.Score >= 0.9 {
		t.Errorf("40%% missing should lower quality noticeably, got %v", q.Score)
	}
}

func TestRejectsStructurallyInvalidSnapshots(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	raw.Hourly.Temperature2m = raw.Hourly.Temperature2m[:10]
	if got := rejectReason(t, raw); got != ReasonInvalid {
		t.Errorf("ragged series: reason = %s", got)
	}

	raw = sim(t, weather.ScenarioNormal, 0)
	raw.Current.Time = "2026-06-30T12:00" // after every forecast day
	if got := rejectReason(t, raw); got != ReasonInvalid {
		t.Errorf("no forecast days: reason = %s", got)
	}
}

func TestMissingCurrentValuesAreFilledFromHourlyAndFlagged(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	// Current is 14:30, so its hour is the 14:00 sample (index 86).
	hourTemp, hourRH, hourApp := *raw.Hourly.Temperature2m[86], *raw.Hourly.RelativeHumidity2m[86], *raw.Hourly.ApparentTemperature[86]
	raw.Current.Temperature2m, raw.Current.RelativeHumidity2m, raw.Current.ApparentTemperature = nil, nil, nil
	raw.Current.DirectNormalIrradiance, raw.Current.WeatherCode = nil, nil

	out := process(t, raw)
	c := out.Current
	if c.TemperatureC != round2(hourTemp) || c.Humidity != round2(hourRH) || c.ApparentTemperatureC != round2(hourApp) {
		t.Errorf("current = %+v, want values from the 14:00 hour (%v, %v, %v)", c, hourTemp, hourRH, hourApp)
	}
	if c.DirectNormalIrradiance != 0 || c.WeatherCode != 0 {
		t.Errorf("non-core fallbacks wrong: %+v", c)
	}
	want := map[string]bool{"temperatureC": true, "humidity": true, "apparentTemperatureC": true, "directNormalIrradiance": true, "weatherCode": true}
	for _, f := range out.Quality.CurrentEstimated {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("fields not flagged as estimated: %v (got %v)", want, out.Quality.CurrentEstimated)
	}
	if out.Quality.Score != 0.7 { // all three core current fields estimated: 1 - 0.3*3/3
		t.Errorf("score = %v, want 0.7", out.Quality.Score)
	}
}

func TestOnlyCoreCurrentFieldsAffectScore(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	raw.Current.DirectNormalIrradiance, raw.Current.WindSpeed10m = nil, nil
	if q := process(t, raw).Quality; q.Score != 1 || len(q.CurrentEstimated) != 2 {
		t.Errorf("quality = %+v: DNI and wind are flagged but must not lower the score", q)
	}

	raw = sim(t, weather.ScenarioNormal, 0)
	raw.Current.Temperature2m = nil
	if q := process(t, raw).Quality; q.Score != 0.9 {
		t.Errorf("one core field estimated: score = %v, want 0.9", q.Score)
	}
}

func TestApparentFallsBackToHeatIndexWhenSourceHasNone(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	for i := range raw.Hourly.ApparentTemperature {
		raw.Hourly.ApparentTemperature[i] = nil
	}
	raw.Current.ApparentTemperature = nil

	out := process(t, raw)
	if want := round2(out.Current.HeatIndexC); out.Current.ApparentTemperatureC != want {
		t.Errorf("current apparent = %v, want the heat index %v", out.Current.ApparentTemperatureC, want)
	}
	if out.Hourly.ApparentTemperatureC[nowHourIndex] != out.Hourly.HeatIndexC[nowHourIndex] {
		t.Error("hourly apparent should equal heat index when the source has none")
	}
	if out.Quality.HourlyEstimated != 0 {
		t.Errorf("hourlyEstimated = %d: a source that never supplies apparent temperature must not be penalised hour by hour", out.Quality.HourlyEstimated)
	}
}

func TestDailyMaxDerivedFromHourlyWhenEnoughRealSamples(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	day := 5 // 2026-05-03
	hourlyMax := math.Inf(-1)
	for i := day * 24; i < day*24+24; i++ {
		hourlyMax = math.Max(hourlyMax, *raw.Hourly.Temperature2m[i])
	}
	raw.Daily.Temperature2mMax[day] = nil

	out := process(t, raw)
	if d := out.Days[day]; d.TempMaxC != round2(hourlyMax) || !d.Estimated {
		t.Errorf("day = %+v, want tempMax %v derived from hourly and flagged estimated", d, hourlyMax)
	}
	if out.Quality.DailyEstimated != 1 {
		t.Errorf("dailyEstimated = %d", out.Quality.DailyEstimated)
	}
}

func TestDailyMaxInterpolatedWhenHourlyCannotHelp(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0)
	day := 5
	left, right := *raw.Daily.Temperature2mMax[day-1], *raw.Daily.Temperature2mMax[day+1]
	raw.Daily.Temperature2mMax[day] = nil
	for i := day * 24; i < day*24+20; i++ { // only 4 real hours left on that date
		raw.Hourly.Temperature2m[i] = nil
	}

	out := process(t, raw)
	if got := out.Days[day].TempMaxC; !near(got, (left+right)/2, 0.011) {
		t.Errorf("tempMax = %v, want %v (halfway between neighbouring days)", got, (left+right)/2)
	}
}

func TestPrecipitationGapsAreZeroButUVIsInterpolated(t *testing.T) {
	raw := sim(t, weather.ScenarioExtreme, 0)
	raw.Daily.PrecipitationSum[4] = nil
	raw.Daily.UVIndexMax[5] = nil
	raw.Daily.UVIndexMax[4], raw.Daily.UVIndexMax[6] = fp(8), fp(12)
	out := process(t, raw)

	if out.Days[4].PrecipitationSumMm != 0 || !out.Days[4].Estimated {
		t.Errorf("precip gap = %+v, want 0 and estimated (never an average of neighbours)", out.Days[4])
	}
	if out.Days[5].UVIndexMax != 10 {
		t.Errorf("uv gap = %v, want 10 (between 8 and 12)", out.Days[5].UVIndexMax)
	}
}

func TestSwappedMinMaxIsRepaired(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	raw.Daily.Temperature2mMax[4], raw.Daily.Temperature2mMin[4] = raw.Daily.Temperature2mMin[4], raw.Daily.Temperature2mMax[4]
	d := process(t, raw).Days[4]
	if d.TempMinC > d.TempMaxC || !d.Estimated {
		t.Errorf("day = %+v", d)
	}
}

func TestNaNAndInfinityCountAsMissing(t *testing.T) {
	raw := sim(t, weather.ScenarioNormal, 0)
	raw.Hourly.Temperature2m[40] = fp(math.NaN())
	raw.Hourly.Temperature2m[41] = fp(math.Inf(1))
	out := process(t, raw)
	for _, v := range out.Hourly.TemperatureC {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatal("non-finite value reached the output")
		}
	}
	if out.Quality.HourlyEstimated != 2 {
		t.Errorf("hourlyEstimated = %d, want 2", out.Quality.HourlyEstimated)
	}
}

func TestProcessDoesNotModifyItsInput(t *testing.T) {
	raw := sim(t, weather.ScenarioBuilding, 0.05)
	before, _ := json.Marshal(raw)
	process(t, raw)
	after, _ := json.Marshal(raw)
	if string(before) != string(after) {
		t.Error("Process mutated the raw snapshot")
	}
}

func TestRealisticGapRatesAreRepairedAccurately(t *testing.T) {
	truth := process(t, sim(t, weather.ScenarioBuilding, 0))
	prevScore := 1.0
	for _, rate := range []float64{0.02, 0.1, 0.25, 0.4} {
		out := process(t, sim(t, weather.ScenarioBuilding, rate))

		for i, v := range out.Hourly.TemperatureC {
			if math.IsNaN(v) || v < -90 || v > 60 {
				t.Fatalf("rate %.2f: hourly temp[%d] = %v", rate, i, v)
			}
		}
		worst := 0.0
		for i := range out.Hourly.TemperatureC {
			worst = math.Max(worst, math.Abs(out.Hourly.TemperatureC[i]-truth.Hourly.TemperatureC[i]))
		}
		if limit := 2 + rate*20; worst > limit {
			t.Errorf("rate %.2f: worst hourly temperature error %.2f°C exceeds %.1f", rate, worst, limit)
		}
		if out.Quality.Score >= prevScore || out.Quality.Score < 0 {
			t.Errorf("rate %.2f: score %v did not drop below %v", rate, out.Quality.Score, prevScore)
		}
		prevScore = out.Quality.Score
	}
}

func TestSummary(t *testing.T) {
	if got := Summary(contracts.Quality{Score: 1}); got != "quality=1.00" {
		t.Errorf("clean summary = %q", got)
	}
	got := Summary(contracts.Quality{Score: 0.8, HourlyTotal: 240, HourlyEstimated: 5, DailyTotal: 10, DailyEstimated: 1,
		CurrentEstimated: []string{"windSpeed", "humidity"}, OutOfRange: 2})
	want := "quality=0.80 hourly_estimated=5/240 daily_estimated=1/10 current_estimated=humidity,windSpeed out_of_range=2"
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}
