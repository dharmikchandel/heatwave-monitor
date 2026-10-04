package contracts

import "time"

// Event type and target for the prediction service's output. The prediction
// service is written in Python; testdata/heatwave-predicted.sample.json keeps
// both sides honest about this shape.
const (
	EventHeatwavePredicted = "heatwave.predicted"

	TargetRisk = "risk"
)

// DayProbability is the estimated chance that a given forecast day is a heatwave
// day. Horizon 0 is the first forecast day (today).
type DayProbability struct {
	Date        string  `json:"date"`
	Horizon     int     `json:"horizon"`
	Probability float64 `json:"probability"`
}

// Prediction is the output of the prediction service.
type Prediction struct {
	// ModelVersion identifies what produced the numbers: a trained model's
	// version, or "rules-v1" for the rule-based fallback.
	ModelVersion string `json:"modelVersion"`
	// Method is "model" (trained, location inside its region) or "rules".
	Method      string           `json:"method"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Days        []DayProbability `json:"days"`
	// Peak is the forecast day with the highest probability.
	Peak DayProbability `json:"peak"`
}

// HeatwavePredicted is the payload of EventHeatwavePredicted. It carries the
// processed weather it was derived from, so the risk service needs no call back.
type HeatwavePredicted struct {
	Weather    ProcessedWeather `json:"weather"`
	Prediction Prediction       `json:"prediction"`
}
