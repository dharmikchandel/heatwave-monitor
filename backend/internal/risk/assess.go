// Package risk implements the risk assessment service: it turns processed
// weather plus heatwave probabilities into risk tiers (Normal .. Extreme
// Danger), scores and plain-language reasons, and hands them to the alert service.
//
// Tiers come from the shared engine (the same rules as the dashboard), so the
// backend and the frontend agree. The service adds three things the dashboard
// cannot do: it uses the days before today (so "two consecutive days" is judged
// on real history, not just the forecast), it raises a tier when a heatwave
// warning is likely, and it explains each verdict.
package risk

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
)

// EscalationProbability is the chance of a heatwave warning at or above which a
// day's tier is raised one step (only for days between Caution and Extreme
// Caution; Danger and above already are the warning).
const EscalationProbability = 0.7

// tierFloor is the lowest score a tier may carry, so a score never reads lower
// than its own tier (e.g. a Danger day at 41 °C is not "52/100").
var tierFloor = map[engine.RiskLevel]int{
	engine.Normal:         0,
	engine.Caution:        25,
	engine.ExtremeCaution: 45,
	engine.Danger:         65,
	engine.ExtremeDanger:  90,
}

// Reasons an input is rejected. Retrying the same bad data would never help.
const (
	ReasonInvalid    = "invalid_payload"
	ReasonUndecoded  = "undecodable_payload"
	ReasonNoForecast = "no_forecast_days"
)

// RejectError means the input cannot be assessed.
type RejectError struct {
	Reason string
	Detail string
}

func (e *RejectError) Error() string { return e.Reason + ": " + e.Detail }

func reject(reason, format string, args ...any) *RejectError {
	return &RejectError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// temperatureScore maps an apparent temperature onto 0-100: 27 °C and below is 0,
// the extreme-danger threshold (54 °C) and above is 100, linear in between.
func temperatureScore(apparentC float64) float64 {
	return math.Min(1, math.Max(0, (apparentC-27)/(engine.ExtremeDangerC-27))) * 100
}

// score is the worst of how hot it is, how likely a warning is, and the tier's floor.
func score(apparentC, probability float64, level engine.RiskLevel) int {
	s := math.Max(temperatureScore(apparentC), probability*100)
	return int(math.Round(math.Max(s, float64(tierFloor[level]))))
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func validate(w contracts.ProcessedWeather, p contracts.Prediction) error {
	if len(w.Days) == 0 {
		return reject(ReasonInvalid, "weather has no days")
	}
	forecast := 0
	for i, d := range w.Days {
		if !finite(d.TempMaxC, d.TempMinC, d.ApparentTempMaxC, d.UVIndexMax, d.PrecipitationSumMm) {
			return reject(ReasonInvalid, "day %s has a non-finite value", d.Date)
		}
		if i > 0 && d.Date <= w.Days[i-1].Date {
			return reject(ReasonInvalid, "days are not in strictly increasing date order at %s", d.Date)
		}
		if d.Forecast {
			forecast++
		}
	}
	if forecast == 0 {
		return reject(ReasonNoForecast, "weather contains no forecast days")
	}
	if !finite(w.Current.ApparentTemperatureC, w.Current.TemperatureC, w.Current.HeatIndexC) {
		return reject(ReasonInvalid, "current conditions have a non-finite value")
	}
	for _, d := range p.Days {
		if !finite(d.Probability) || d.Probability < 0 || d.Probability > 1 {
			return reject(ReasonInvalid, "probability %v for %s is outside 0-1", d.Probability, d.Date)
		}
	}
	return nil
}

// Assess computes the risk assessment for one observation. now stamps the result.
func Assess(w contracts.ProcessedWeather, p contracts.Prediction, now time.Time) (contracts.Assessment, error) {
	if err := validate(w, p); err != nil {
		return contracts.Assessment{}, err
	}

	// Tier every day, history included, with the dashboard's rolling rule: that way
	// the first forecast day knows whether yesterday was already dangerously hot.
	daily := engine.DailyWeather{}
	apparent := make([]float64, len(w.Days))
	for i, d := range w.Days {
		daily.Time = append(daily.Time, d.Date)
		daily.Temperature2mMax = append(daily.Temperature2mMax, d.TempMaxC)
		daily.Temperature2mMin = append(daily.Temperature2mMin, d.TempMinC)
		daily.ApparentTemperatureMax = append(daily.ApparentTemperatureMax, d.ApparentTempMaxC)
		daily.UVIndexMax = append(daily.UVIndexMax, d.UVIndexMax)
		daily.PrecipitationSum = append(daily.PrecipitationSum, d.PrecipitationSumMm)
		apparent[i] = d.ApparentTempMaxC
	}
	base := engine.BuildDailyRiskForecast(daily)

	probByDate := make(map[string]float64, len(p.Days))
	for _, d := range p.Days {
		probByDate[d.Date] = d.Probability
	}

	var (
		days    []contracts.DayRisk
		history []float64
	)
	for i, d := range w.Days {
		if !d.Forecast {
			history = append(history, d.ApparentTempMaxC)
			continue
		}
		prob := probByDate[d.Date] // a day the predictor skipped counts as "no signal", never as certainty
		level, escalated := escalate(base[i].RiskLevel, prob)
		dr := contracts.DayRisk{
			Date:             d.Date,
			Horizon:          len(days),
			TempMaxC:         d.TempMaxC,
			ApparentTempMaxC: d.ApparentTempMaxC,
			BaseLevel:        base[i].RiskLevel,
			Level:            level,
			Probability:      prob,
			Escalated:        escalated,
			Score:            score(d.ApparentTempMaxC, prob, level),
		}
		dr.Reason = dayReason(dr, engine.ConsecutiveDaysAtOrAbove(apparent[:i+1], engine.DangerC))
		days = append(days, dr)
	}

	current := nowRisk(w.Current, history)

	peak := days[0]
	var warning []string
	for _, d := range days {
		if d.Level.Severity() >= engine.Danger.Severity() {
			warning = append(warning, d.Date)
		}
		if morePressing(d, peak) {
			peak = d
		}
	}
	alertLevel := current.Level
	if peak.Level.Severity() > alertLevel.Severity() {
		alertLevel = peak.Level
	}

	a := contracts.Assessment{
		Now:              current,
		Days:             days,
		Peak:             peak,
		AlertLevel:       alertLevel,
		HeatwaveExpected: len(warning) > 0,
		WarningDays:      append([]string{}, warning...),
		Method:           p.Method,
		ModelVersion:     p.ModelVersion,
		AssessedAt:       now.UTC(),
	}
	a.Rationale = rationale(a, w.Trend)
	return a, nil
}

// escalate raises a Caution or Extreme Caution day one tier when a heatwave
// warning is likely. Normal days are left alone (the probability cannot be
// that high there), and Danger and above are already the warning.
func escalate(base engine.RiskLevel, probability float64) (engine.RiskLevel, bool) {
	if probability >= EscalationProbability &&
		base.Severity() >= engine.Caution.Severity() && base.Severity() < engine.Danger.Severity() {
		return base.Escalate(), true
	}
	return base, false
}

// morePressing orders days for picking the peak: higher tier first, then higher
// probability, then the earlier date.
func morePressing(a, b contracts.DayRisk) bool {
	if a.Level.Severity() != b.Level.Severity() {
		return a.Level.Severity() > b.Level.Severity()
	}
	return a.Probability > b.Probability
}

// nowRisk judges the current observation against the days just before it: the
// Danger tier needs two consecutive days at or above 41 °C, so yesterday counts
// when it is known.
func nowRisk(cur contracts.ProcessedCurrent, history []float64) contracts.NowRisk {
	series := append(append([]float64{}, history...), cur.ApparentTemperatureC)
	level := engine.EvaluateHeatRisk(cur.ApparentTemperatureC, series)
	consecutive := engine.ConsecutiveDaysAtOrAbove(series, engine.DangerC)
	n := contracts.NowRisk{
		Level:              level,
		Score:              score(cur.ApparentTemperatureC, 0, level),
		ApparentTempC:      cur.ApparentTemperatureC,
		TemperatureC:       cur.TemperatureC,
		HeatIndexC:         cur.HeatIndexC,
		ConsecutiveHotDays: consecutive,
	}
	n.Reason = "Now: " + tierSentence(level, cur.ApparentTemperatureC, consecutive)
	return n
}

// tierSentence explains a tier from the numbers that produced it.
func tierSentence(level engine.RiskLevel, apparent float64, consecutive int) string {
	head := fmt.Sprintf("%s — apparent temperature %.1f °C", engine.RiskLevelLabel[level], apparent)
	switch level {
	case engine.Normal:
		return head + " is below the 32 °C caution threshold."
	case engine.Caution:
		return head + " is at or above the 32 °C caution threshold."
	case engine.ExtremeCaution:
		if apparent >= engine.DangerC {
			return head + fmt.Sprintf(" is at or above 41 °C, but only for %d day in a row; Danger needs 2 consecutive days.", consecutive)
		}
		return head + " is at or above the 38 °C extreme-caution threshold."
	case engine.Danger:
		return head + fmt.Sprintf(" has been at or above 41 °C for %d consecutive days.", consecutive)
	case engine.ExtremeDanger:
		return head + " is at or above the 54 °C extreme-danger threshold."
	}
	return head + "."
}

func dayReason(d contracts.DayRisk, consecutive int) string {
	s := tierSentence(d.BaseLevel, d.ApparentTempMaxC, consecutive)
	if d.Escalated {
		s += fmt.Sprintf(" Raised to %s because the chance of a heatwave warning is %.0f%%.", engine.RiskLevelLabel[d.Level], d.Probability*100)
	}
	return s
}

func rationale(a contracts.Assessment, trend contracts.Trend) []string {
	out := []string{a.Now.Reason}

	switch {
	case a.HeatwaveExpected:
		out = append(out, fmt.Sprintf("Heatwave expected: Danger or worse on %d forecast day(s) (%s); worst is %s on %s.",
			len(a.WarningDays), strings.Join(a.WarningDays, ", "), engine.RiskLevelLabel[a.Peak.Level], a.Peak.Date))
	case a.Peak.Level.Severity() > engine.Normal.Severity():
		out = append(out, fmt.Sprintf("No Danger days in the forecast; the hottest is %s on %s (%.1f °C).",
			engine.RiskLevelLabel[a.Peak.Level], a.Peak.Date, a.Peak.ApparentTempMaxC))
	default:
		out = append(out, "No forecast day rises above Normal.")
	}

	if best := mostLikely(a.Days); best.Probability >= 0.05 {
		how := "rule-based estimate"
		if a.Method == "model" {
			how = "trained model " + a.ModelVersion
		}
		out = append(out, fmt.Sprintf("Heatwave-warning probability peaks at %.0f%% on %s (%s).", best.Probability*100, best.Date, how))
	}
	if trend.IsRising {
		out = append(out, fmt.Sprintf("The forecast is trending hotter (+%.1f °C against the earlier days).", trend.AnomalyC))
	}
	return out
}

func mostLikely(days []contracts.DayRisk) contracts.DayRisk {
	best := days[0]
	for _, d := range days[1:] {
		if d.Probability > best.Probability {
			best = d
		}
	}
	return best
}
