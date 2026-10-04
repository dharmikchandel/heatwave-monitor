package contracts

import (
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/engine"
)

// Event type and target for the risk service's output.
const (
	EventRiskAssessed = "risk.assessed"

	TargetAlert = "alert"
)

// DayRisk is the risk tier for one forecast day.
type DayRisk struct {
	Date    string `json:"date"`
	Horizon int    `json:"horizon"` // 0 = today
	// TempMaxC and ApparentTempMaxC are the forecast values the tier is based on.
	TempMaxC         float64 `json:"tempMaxC"`
	ApparentTempMaxC float64 `json:"apparentTempMaxC"`
	// BaseLevel comes from the temperatures alone (the dashboard's rule). Level is
	// BaseLevel after probability escalation, so Level >= BaseLevel always.
	BaseLevel   engine.RiskLevel `json:"baseLevel"`
	Level       engine.RiskLevel `json:"level"`
	Probability float64          `json:"probability"` // chance this day is a heatwave-warning day
	Escalated   bool             `json:"escalated"`
	Score       int              `json:"score"` // 0-100, never contradicts Level
	Reason      string           `json:"reason"`
}

// NowRisk is the risk right now, from the current observation and the days just before.
type NowRisk struct {
	Level              engine.RiskLevel `json:"level"`
	Score              int              `json:"score"`
	ApparentTempC      float64          `json:"apparentTempC"`
	TemperatureC       float64          `json:"temperatureC"`
	HeatIndexC         float64          `json:"heatIndexC"`
	ConsecutiveHotDays int              `json:"consecutiveHotDays"` // days up to now at or above the Danger threshold
	Reason             string           `json:"reason"`
}

// Assessment is the risk service's verdict for one location.
type Assessment struct {
	Now  NowRisk   `json:"now"`
	Days []DayRisk `json:"days"` // forecast days only
	// Peak is the most severe forecast day (ties: higher probability, then earlier).
	Peak DayRisk `json:"peak"`
	// AlertLevel is the most severe tier the location is in now or is expected to
	// reach within the forecast: max(Now.Level, Peak.Level). This is what alerting keys on.
	AlertLevel engine.RiskLevel `json:"alertLevel"`
	// HeatwaveExpected is true when any forecast day is Danger or Extreme Danger
	// (the dashboard's early-warning banner condition); WarningDays lists those dates.
	HeatwaveExpected bool      `json:"heatwaveExpected"`
	WarningDays      []string  `json:"warningDays"`
	Method           string    `json:"method"`       // prediction method: "model" or "rules"
	ModelVersion     string    `json:"modelVersion"` // what produced the probabilities
	Rationale        []string  `json:"rationale"`    // human-readable summary, most important first
	AssessedAt       time.Time `json:"assessedAt"`
}

// RiskAssessed is the payload of EventRiskAssessed. It carries the weather and
// prediction it was derived from, so alerting needs no calls back.
type RiskAssessed struct {
	Weather    ProcessedWeather `json:"weather"`
	Prediction Prediction       `json:"prediction"`
	Assessment Assessment       `json:"assessment"`
}
