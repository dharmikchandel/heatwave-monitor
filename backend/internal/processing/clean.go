// Package processing implements the data processing service: it cleans raw
// weather snapshots (repairing gaps and discarding implausible values instead
// of treating them as zero), computes heat index, daily aggregates and the trend
// anomaly, and hands the result to the prediction service.
package processing

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
)

// Reasons a snapshot is rejected. A rejected snapshot is recorded and dropped;
// retrying the same bad data would never help.
const (
	ReasonInvalid        = "invalid_snapshot"
	ReasonTooMuchMissing = "too_much_missing_data"
	ReasonUndecodable    = "undecodable_payload"
)

// RejectError means the snapshot cannot be turned into trustworthy metrics.
type RejectError struct {
	Reason string
	Detail string
}

func (e *RejectError) Error() string { return e.Reason + ": " + e.Detail }

func reject(reason, format string, args ...any) *RejectError {
	return &RejectError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// maxMissingFraction is how much of the hourly temperature or humidity series
// may be missing before the snapshot is considered too unreliable to repair.
const maxMissingFraction = 0.5

// minRealHoursPerDay is how many genuine hourly samples a day needs before its
// daily max/min may be derived from them.
const minRealHoursPerDay = 12

// Physically plausible ranges. Values outside are treated as missing.
var (
	tempRange     = [2]float64{-90, 60}
	apparentRange = [2]float64{-100, 90}
	windRange     = [2]float64{0, 400} // km/h
	uvRange       = [2]float64{0, 20}
	precipRange   = [2]float64{0, 1000} // mm per day
	dniRange      = [2]float64{0, 1500} // W/m²
)

// plausible returns a validator accepting finite values inside r.
func plausible(r [2]float64) func(float64) (float64, bool) {
	return func(v float64) (float64, bool) { return v, v >= r[0] && v <= r[1] }
}

// humidity tolerates small sensor overshoot (clamped to 0–100) but not nonsense.
func humidity(v float64) (float64, bool) {
	if v < -10 || v > 110 {
		return 0, false
	}
	return math.Min(100, math.Max(0, v)), true
}

// series is a cleaned numeric series: vals holds repaired values, est marks the
// positions that had to be repaired.
type series struct {
	vals       []float64
	est        []bool
	real       int // genuine samples
	outOfRange int // implausible samples discarded
}

func (s series) missing() int { return len(s.vals) - s.real }

// cleanSeries validates each sample, then fills gaps: linear interpolation
// between the nearest valid neighbours, or the nearest valid value at the edges.
// ok is false when no sample is usable.
func cleanSeries(raw []*float64, valid func(float64) (float64, bool)) (s series, ok bool) {
	n := len(raw)
	s = series{vals: make([]float64, n), est: make([]bool, n)}
	good := make([]bool, n)
	for i, p := range raw {
		if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
			continue
		}
		v, accepted := valid(*p)
		if !accepted {
			s.outOfRange++
			continue
		}
		s.vals[i], good[i] = v, true
		s.real++
	}
	for i := range good {
		s.est[i] = !good[i]
	}
	return s, fill(s.vals, good)
}

// fill repairs vals in place where good[i] is false. It reports false if there
// is nothing to repair from.
func fill(vals []float64, good []bool) bool {
	first := -1
	for i, g := range good {
		if g {
			first = i
			break
		}
	}
	if first < 0 {
		return false
	}
	for i := 0; i < first; i++ {
		vals[i] = vals[first]
	}
	prev := first
	for i := first + 1; i < len(vals); i++ {
		if !good[i] {
			continue
		}
		for j := prev + 1; j < i; j++ {
			frac := float64(j-prev) / float64(i-prev)
			vals[j] = vals[prev] + frac*(vals[i]-vals[prev])
		}
		prev = i
	}
	for i := prev + 1; i < len(vals); i++ {
		vals[i] = vals[prev]
	}
	return true
}

// currentHourIndex mirrors the dashboard's lookup: the exact hour if present,
// otherwise the first hour after the current time, otherwise the last hour.
func currentHourIndex(times []string, current string) int {
	for i, t := range times {
		if t == current {
			return i
		}
	}
	for i, t := range times {
		if t > current { // local ISO timestamps sort lexicographically
			return i
		}
	}
	return len(times) - 1
}

// hourAtOrBefore returns the latest hour not after current (the hour the
// current reading belongs to), or 0 if the series starts later.
func hourAtOrBefore(times []string, current string) int {
	idx := 0
	for i, t := range times {
		if t <= current {
			idx = i
		}
	}
	return idx
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Process cleans one raw snapshot and derives the metrics downstream services
// need. It is pure; the caller adds identity fields (location, observation ID,
// timestamps). It returns a *RejectError when the data cannot be trusted.
func Process(raw contracts.RawSnapshot) (contracts.ProcessedWeather, error) {
	if err := raw.Validate(); err != nil {
		return contracts.ProcessedWeather{}, reject(ReasonInvalid, "%v", err)
	}
	h, d := raw.Hourly, raw.Daily
	outOfRange := 0

	// ---- hourly series ----
	temp, okT := cleanSeries(h.Temperature2m, plausible(tempRange))
	rh, okH := cleanSeries(h.RelativeHumidity2m, humidity)
	if !okT || !okH {
		return contracts.ProcessedWeather{}, reject(ReasonTooMuchMissing, "hourly temperature or humidity has no usable values")
	}
	hours := len(h.Time)
	for name, s := range map[string]series{"temperature": temp, "humidity": rh} {
		if frac := float64(s.missing()) / float64(hours); frac > maxMissingFraction {
			return contracts.ProcessedWeather{}, reject(ReasonTooMuchMissing, "hourly %s is %.0f%% missing", name, frac*100)
		}
	}

	heatIndex := make([]float64, hours)
	for i := range heatIndex {
		heatIndex[i] = engine.HeatIndex(temp.vals[i], rh.vals[i])
	}

	apparent, okA := cleanSeries(h.ApparentTemperature, plausible(apparentRange))
	if !okA { // no apparent temperature at all: the heat index is the best stand-in
		apparent = series{vals: append([]float64(nil), heatIndex...), est: allTrue(hours)}
	}
	wind, okW := cleanSeries(h.WindSpeed10m, plausible(windRange))
	if !okW {
		wind = series{vals: make([]float64, hours), est: allTrue(hours)}
	}
	outOfRange += temp.outOfRange + rh.outOfRange + apparent.outOfRange + wind.outOfRange

	hourlyEstimated := 0
	for i := 0; i < hours; i++ {
		if temp.est[i] || rh.est[i] || (okA && apparent.est[i]) {
			hourlyEstimated++
		}
	}

	// ---- current conditions ----
	cur := raw.Current
	at := hourAtOrBefore(h.Time, cur.Time)
	var estimatedFields []string
	pick := func(name string, rawVal *float64, valid func(float64) (float64, bool), fallback float64) float64 {
		if rawVal != nil && !math.IsNaN(*rawVal) && !math.IsInf(*rawVal, 0) {
			v, ok := valid(*rawVal)
			if ok {
				return v
			}
			outOfRange++
		}
		estimatedFields = append(estimatedFields, name)
		return fallback
	}
	curTemp := pick("temperatureC", cur.Temperature2m, plausible(tempRange), temp.vals[at])
	curRH := pick("humidity", cur.RelativeHumidity2m, humidity, rh.vals[at])
	curHI := engine.HeatIndex(curTemp, curRH)
	curApparent := pick("apparentTemperatureC", cur.ApparentTemperature, plausible(apparentRange), apparentFallback(apparent, at, curHI, okA))
	curWind := pick("windSpeed", cur.WindSpeed10m, plausible(windRange), wind.vals[at])
	curDNI := pick("directNormalIrradiance", cur.DirectNormalIrradiance, plausible(dniRange), 0)
	curCode := pick("weatherCode", cur.WeatherCode, plausible([2]float64{0, 99}), 0)

	coreEstimated := 0
	for _, f := range estimatedFields {
		if f == "temperatureC" || f == "humidity" || f == "apparentTemperatureC" {
			coreEstimated++
		}
	}

	// ---- daily series ----
	days := len(d.Time)
	hoursByDate := map[string][]int{}
	for i, t := range h.Time {
		if len(t) >= 10 {
			hoursByDate[t[:10]] = append(hoursByDate[t[:10]], i)
		}
	}
	realTempHours := func(date string) int {
		n := 0
		for _, i := range hoursByDate[date] {
			if !temp.est[i] {
				n++
			}
		}
		return n
	}

	dayEst := make([]bool, days)
	tmax, tmin, appMax, err := cleanDailyTemps(d, hoursByDate, temp, apparent, realTempHours, dayEst, &outOfRange)
	if err != nil {
		return contracts.ProcessedWeather{}, err
	}
	uv, okUV := cleanSeries(d.UVIndexMax, plausible(uvRange))
	if !okUV {
		uv = series{vals: make([]float64, days), est: allTrue(days)}
	}
	precip, _ := cleanSeries(d.PrecipitationSum, plausible(precipRange))
	if precip.real == 0 {
		precip = series{vals: make([]float64, days), est: allTrue(days), outOfRange: precip.outOfRange}
	}
	for i := range precip.vals { // rain is intermittent: a gap means "unknown", never "average of neighbours"
		if precip.est[i] {
			precip.vals[i] = 0
		}
	}
	outOfRange += uv.outOfRange + precip.outOfRange

	today := ""
	if len(cur.Time) >= 10 {
		today = cur.Time[:10]
	}
	out := make([]contracts.ProcessedDay, days)
	dailyEstimated := 0
	forecastMax := []float64{}
	for i := 0; i < days; i++ {
		date := d.Time[i]
		hiMax := appMax[i]
		if idxs := hoursByDate[date]; len(idxs) > 0 {
			hiMax = math.Inf(-1)
			for _, j := range idxs {
				hiMax = math.Max(hiMax, heatIndex[j])
			}
		}
		est := dayEst[i] || uv.est[i] || precip.est[i]
		if est {
			dailyEstimated++
		}
		out[i] = contracts.ProcessedDay{
			Date:               date,
			Forecast:           date >= today,
			TempMaxC:           round2(tmax[i]),
			TempMinC:           round2(tmin[i]),
			ApparentTempMaxC:   round2(appMax[i]),
			HeatIndexMaxC:      round2(hiMax),
			UVIndexMax:         round2(uv.vals[i]),
			PrecipitationSumMm: round2(precip.vals[i]),
			Estimated:          est,
		}
		if out[i].Forecast {
			forecastMax = append(forecastMax, out[i].TempMaxC)
		}
	}
	if len(forecastMax) == 0 {
		return contracts.ProcessedWeather{}, reject(ReasonInvalid, "no forecast days on or after %s", today)
	}
	trend := engine.ComputeTrendAnomaly(forecastMax)

	// ---- quality ----
	score := 1 - 0.4*float64(hourlyEstimated)/float64(hours) -
		0.3*float64(dailyEstimated)/float64(days) -
		0.3*float64(coreEstimated)/3
	if estimatedFields == nil {
		estimatedFields = []string{}
	}

	return contracts.ProcessedWeather{
		Current: contracts.ProcessedCurrent{
			Time:                   cur.Time,
			TemperatureC:           round2(curTemp),
			Humidity:               round2(curRH),
			ApparentTemperatureC:   round2(curApparent),
			HeatIndexC:             round2(curHI),
			WindSpeed:              round2(curWind),
			WeatherCode:            int(curCode),
			DirectNormalIrradiance: round2(curDNI),
		},
		Hourly: &contracts.ProcessedHourly{
			Time:                 h.Time,
			TemperatureC:         roundAll(temp.vals),
			Humidity:             roundAll(rh.vals),
			ApparentTemperatureC: roundAll(apparent.vals),
			HeatIndexC:           roundAll(heatIndex),
			WindSpeed:            roundAll(wind.vals),
			CurrentIndex:         currentHourIndex(h.Time, cur.Time),
		},
		Days:  out,
		Trend: contracts.Trend{IsRising: trend.IsRising, AnomalyC: round2(trend.AnomalyC)},
		Quality: contracts.Quality{
			Score:            round2(math.Min(1, math.Max(0, score))),
			HourlyTotal:      hours,
			HourlyEstimated:  hourlyEstimated,
			DailyTotal:       days,
			DailyEstimated:   dailyEstimated,
			CurrentEstimated: estimatedFields,
			OutOfRange:       outOfRange,
		},
	}, nil
}

// apparentFallback picks the apparent temperature to use when the current
// reading lacks one: the hourly value if the source supplied apparent
// temperatures at all, otherwise the heat index.
func apparentFallback(apparent series, at int, heatIndex float64, haveApparent bool) float64 {
	if haveApparent {
		return apparent.vals[at]
	}
	return heatIndex
}

// cleanDailyTemps cleans daily max/min/apparent-max. A missing day value is
// derived from that date's hourly samples when enough genuine ones exist,
// otherwise interpolated from neighbouring days. dayEst is updated in place.
func cleanDailyTemps(d contracts.RawDaily, hoursByDate map[string][]int, temp, apparent series,
	realTempHours func(string) int, dayEst []bool, outOfRange *int) (tmax, tmin, appMax []float64, err error) {

	type col struct {
		name   string
		raw    []*float64
		valid  func(float64) (float64, bool)
		derive func(idxs []int) float64
	}
	maxOf := func(vals []float64) func([]int) float64 {
		return func(idxs []int) float64 {
			m := math.Inf(-1)
			for _, i := range idxs {
				m = math.Max(m, vals[i])
			}
			return m
		}
	}
	minOf := func(idxs []int) float64 {
		m := math.Inf(1)
		for _, i := range idxs {
			m = math.Min(m, temp.vals[i])
		}
		return m
	}
	cols := []col{
		{"temperature2mMax", d.Temperature2mMax, plausible(tempRange), maxOf(temp.vals)},
		{"temperature2mMin", d.Temperature2mMin, plausible(tempRange), minOf},
		{"apparentTemperatureMax", d.ApparentTemperatureMax, plausible(apparentRange), maxOf(apparent.vals)},
	}

	results := make([][]float64, len(cols))
	for c, col := range cols {
		s, _ := cleanSeries(col.raw, col.valid)
		*outOfRange += s.outOfRange
		good := make([]bool, len(s.vals))
		for i := range good {
			good[i] = !s.est[i]
			if good[i] {
				continue
			}
			date := d.Time[i]
			if realTempHours(date) >= minRealHoursPerDay {
				s.vals[i], good[i] = col.derive(hoursByDate[date]), true
			}
			dayEst[i] = true
		}
		if !fill(s.vals, good) {
			return nil, nil, nil, reject(ReasonTooMuchMissing, "daily %s has no usable values", col.name)
		}
		results[c] = s.vals
	}
	tmax, tmin, appMax = results[0], results[1], results[2]
	for i := range tmax {
		if tmin[i] > tmax[i] {
			tmin[i], tmax[i] = tmax[i], tmin[i]
			dayEst[i] = true
		}
	}
	return tmax, tmin, appMax, nil
}

func allTrue(n int) []bool {
	b := make([]bool, n)
	for i := range b {
		b[i] = true
	}
	return b
}

func roundAll(vs []float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = round2(v)
	}
	return out
}

// Summary renders the repairs made, for logs.
func Summary(q contracts.Quality) string {
	parts := []string{fmt.Sprintf("quality=%.2f", q.Score)}
	if q.HourlyEstimated > 0 {
		parts = append(parts, fmt.Sprintf("hourly_estimated=%d/%d", q.HourlyEstimated, q.HourlyTotal))
	}
	if q.DailyEstimated > 0 {
		parts = append(parts, fmt.Sprintf("daily_estimated=%d/%d", q.DailyEstimated, q.DailyTotal))
	}
	if len(q.CurrentEstimated) > 0 {
		fields := append([]string(nil), q.CurrentEstimated...)
		sort.Strings(fields)
		parts = append(parts, "current_estimated="+strings.Join(fields, ","))
	}
	if q.OutOfRange > 0 {
		parts = append(parts, fmt.Sprintf("out_of_range=%d", q.OutOfRange))
	}
	return strings.Join(parts, " ")
}
