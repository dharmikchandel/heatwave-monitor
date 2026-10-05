// Package engine is the Go port of the dashboard's heatwave engine
// (lib/heatwaveEngine.ts). It is pure and deterministic: numbers in,
// classification out.
//
// The TypeScript engine is the source of truth. testdata/engine-vectors.json is
// generated from it (`make vectors`) and engine_test.go replays every vector,
// so any divergence between the two implementations fails a test. Keep the
// arithmetic here in the same order as the TypeScript — including its quirks,
// e.g. AssessHeatwave counts consecutive danger days at the *end* of the series.
package engine

import (
	"fmt"
	"math"
)

// RiskLevel is one of the five WMO-aligned heat risk tiers. The string values
// are the same slugs the frontend uses.
type RiskLevel string

const (
	Normal         RiskLevel = "normal"
	Caution        RiskLevel = "caution"
	ExtremeCaution RiskLevel = "extreme-caution"
	Danger         RiskLevel = "danger"
	ExtremeDanger  RiskLevel = "extreme-danger"
)

// RiskLevelOrder lists the tiers from mildest to most severe.
var RiskLevelOrder = []RiskLevel{Normal, Caution, ExtremeCaution, Danger, ExtremeDanger}

// RiskLevelLabel is the human-readable name of each tier.
var RiskLevelLabel = map[RiskLevel]string{
	Normal:         "Normal",
	Caution:        "Caution",
	ExtremeCaution: "Extreme Caution",
	Danger:         "Danger",
	ExtremeDanger:  "Extreme Danger",
}

// Severity returns the tier's position in RiskLevelOrder (0 = Normal), or -1 for
// an unknown level.
func (l RiskLevel) Severity() int {
	for i, v := range RiskLevelOrder {
		if v == l {
			return i
		}
	}
	return -1
}

// Escalate returns the next more severe tier (ExtremeDanger stays put).
func (l RiskLevel) Escalate() RiskLevel {
	i := l.Severity()
	if i < 0 || i >= len(RiskLevelOrder)-1 {
		return l
	}
	return RiskLevelOrder[i+1]
}

// WMO-aligned apparent-temperature thresholds, in Celsius.
const (
	CautionC        = 32.0
	ExtremeCautionC = 38.0
	DangerC         = 41.0
	ExtremeDangerC  = 54.0
)

func celsiusToFahrenheit(c float64) float64 { return (c*9)/5 + 32 }
func fahrenheitToCelsius(f float64) float64 { return ((f - 32) * 5) / 9 }

// HeatIndex returns the Steadman/NWS Rothfusz regression heat index in Celsius.
// Below the regression's validity band (roughly 27°C / 40% RH) it falls back to
// the simple average-based approximation, per NWS guidance. Humidity is clamped
// to 0–100.
func HeatIndex(tempC, humidity float64) float64 {
	T := celsiusToFahrenheit(tempC)
	RH := math.Min(100, math.Max(0, humidity))

	simpleHI := 0.5 * (T + 61 + (T-68)*1.2 + RH*0.094)
	if (simpleHI+T)/2 < 80 {
		return fahrenheitToCelsius(simpleHI)
	}

	HI := -42.379 +
		2.04901523*T +
		10.14333127*RH -
		0.22475541*T*RH -
		0.00683783*T*T -
		0.05481717*RH*RH +
		0.00122874*T*T*RH +
		0.00085282*T*RH*RH -
		0.00000199*T*T*RH*RH

	if RH < 13 && T >= 80 && T <= 112 {
		HI -= ((13 - RH) / 4) * math.Sqrt((17-math.Abs(T-95))/17)
	} else if RH > 85 && T >= 80 && T <= 87 {
		HI += ((RH - 85) / 10) * ((87 - T) / 5)
	}

	return fahrenheitToCelsius(HI)
}

// ConsecutiveDaysAtOrAbove counts how many values at the end of the series are
// at or above threshold, stopping at the first one below it.
func ConsecutiveDaysAtOrAbove(values []float64, threshold float64) int {
	count := 0
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] < threshold {
			break
		}
		count++
	}
	return count
}

// EvaluateHeatRisk determines the heat risk tier for an apparent temperature.
// daysMaxTemp is a trailing series of daily max apparent temperatures (oldest
// first, today last); the Danger tier needs at least two consecutive days at or
// above the danger threshold, otherwise a single hot day is Extreme Caution. A
// nil or empty series is treated as just the current reading.
func EvaluateHeatRisk(apparentTempC float64, daysMaxTemp []float64) RiskLevel {
	if apparentTempC >= ExtremeDangerC {
		return ExtremeDanger
	}
	if apparentTempC >= DangerC {
		series := daysMaxTemp
		if len(series) == 0 {
			series = []float64{apparentTempC}
		}
		if ConsecutiveDaysAtOrAbove(series, DangerC) >= 2 {
			return Danger
		}
		return ExtremeCaution
	}
	if apparentTempC >= ExtremeCautionC {
		return ExtremeCaution
	}
	if apparentTempC >= CautionC {
		return Caution
	}
	return Normal
}

// Assessment is the overall heat assessment for a location right now.
type Assessment struct {
	RiskLevel             RiskLevel `json:"riskLevel"`
	HeatIndexC            float64   `json:"heatIndexC"`
	ConsecutiveDangerDays int       `json:"consecutiveDangerDays"`
	IsHeatwaveWarning     bool      `json:"isHeatwaveWarning"`
	Message               string    `json:"message"`
}

// AssessHeatwave combines the current reading with the daily apparent-max series.
func AssessHeatwave(tempC, apparentTempC, humidity float64, dailyApparentMax []float64) Assessment {
	level := EvaluateHeatRisk(apparentTempC, dailyApparentMax)
	consecutive := ConsecutiveDaysAtOrAbove(dailyApparentMax, DangerC)

	plural := "s"
	if consecutive == 1 {
		plural = ""
	}
	messages := map[RiskLevel]string{
		Normal:         "Conditions are within a safe, comfortable range.",
		Caution:        "Heat is building. Stay hydrated and limit strenuous outdoor activity during peak hours.",
		ExtremeCaution: "Elevated heat stress risk. Heat cramps and exhaustion are possible with prolonged exposure.",
		Danger:         fmt.Sprintf("Heatwave warning: dangerous heat has persisted for %d consecutive day%s. Heat exhaustion is likely, heat stroke is possible.", consecutive, plural),
		ExtremeDanger:  "Extreme danger: heat stroke is imminent with continued exposure. Avoid outdoor activity entirely.",
	}

	return Assessment{
		RiskLevel:             level,
		HeatIndexC:            HeatIndex(tempC, humidity),
		ConsecutiveDangerDays: consecutive,
		IsHeatwaveWarning:     level == Danger || level == ExtremeDanger,
		Message:               messages[level],
	}
}

// Trend describes whether the latest day runs hotter than the earlier baseline.
type Trend struct {
	IsRising bool    `json:"isRising"`
	AnomalyC float64 `json:"anomalyC"`
}

// ComputeTrendAnomaly compares the last day's max temperature with the mean of
// the days before it. With fewer than two days there is no trend.
func ComputeTrendAnomaly(dailyTempMax []float64) Trend {
	if len(dailyTempMax) < 2 {
		return Trend{}
	}
	baseline := dailyTempMax[:len(dailyTempMax)-1]
	sum := 0.0
	for _, t := range baseline {
		sum += t
	}
	anomaly := dailyTempMax[len(dailyTempMax)-1] - sum/float64(len(baseline))
	return Trend{IsRising: anomaly > 1, AnomalyC: anomaly}
}

// DailyWeather holds parallel per-day series (same shape as the frontend type).
type DailyWeather struct {
	Time                   []string  `json:"time"`
	Temperature2mMax       []float64 `json:"temperature2mMax"`
	Temperature2mMin       []float64 `json:"temperature2mMin"`
	ApparentTemperatureMax []float64 `json:"apparentTemperatureMax"`
	UVIndexMax             []float64 `json:"uvIndexMax"`
	PrecipitationSum       []float64 `json:"precipitationSum"`
}

// DailyRiskForecast is one forecast day with its computed risk tier.
type DailyRiskForecast struct {
	Date             string    `json:"date"`
	TempMax          float64   `json:"tempMax"`
	TempMin          float64   `json:"tempMin"`
	ApparentTempMax  float64   `json:"apparentTempMax"`
	UVIndexMax       float64   `json:"uvIndexMax"`
	PrecipitationSum float64   `json:"precipitationSum"`
	RiskLevel        RiskLevel `json:"riskLevel"`
}

// BuildDailyRiskForecast classifies each day using the rolling series of
// apparent maxima up to and including that day. If the parallel series differ in
// length, only the days present in all of them are returned.
func BuildDailyRiskForecast(d DailyWeather) []DailyRiskForecast {
	n := min(len(d.Time), len(d.Temperature2mMax), len(d.Temperature2mMin),
		len(d.ApparentTemperatureMax), len(d.UVIndexMax), len(d.PrecipitationSum))

	out := make([]DailyRiskForecast, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, DailyRiskForecast{
			Date:             d.Time[i],
			TempMax:          d.Temperature2mMax[i],
			TempMin:          d.Temperature2mMin[i],
			ApparentTempMax:  d.ApparentTemperatureMax[i],
			UVIndexMax:       d.UVIndexMax[i],
			PrecipitationSum: d.PrecipitationSum[i],
			RiskLevel:        EvaluateHeatRisk(d.ApparentTemperatureMax[i], d.ApparentTemperatureMax[:i+1]),
		})
	}
	return out
}
