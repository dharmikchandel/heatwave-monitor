package contracts

import "time"

// ProcessedCurrent is the cleaned current observation: every value is a real
// number (nulls filled), and HeatIndexC is computed by the engine.
type ProcessedCurrent struct {
	Time                   string  `json:"time"`
	TemperatureC           float64 `json:"temperatureC"`
	Humidity               float64 `json:"humidity"`
	ApparentTemperatureC   float64 `json:"apparentTemperatureC"`
	HeatIndexC             float64 `json:"heatIndexC"`
	WindSpeed              float64 `json:"windSpeed"`
	WeatherCode            int     `json:"weatherCode"`
	DirectNormalIrradiance float64 `json:"directNormalIrradiance"`
}

// ProcessedHourly holds cleaned hourly series (local time, oldest first, past
// days included). CurrentIndex is the position the dashboard treats as "now":
// the exact current hour if present, otherwise the first hour after it.
type ProcessedHourly struct {
	Time                 []string  `json:"time"`
	TemperatureC         []float64 `json:"temperatureC"`
	Humidity             []float64 `json:"humidity"`
	ApparentTemperatureC []float64 `json:"apparentTemperatureC"`
	HeatIndexC           []float64 `json:"heatIndexC"`
	WindSpeed            []float64 `json:"windSpeed"`
	CurrentIndex         int       `json:"currentIndex"`
}

// ProcessedDay is one cleaned day. Forecast is true for today and later, false
// for the history days before it. Estimated is true when any field had to be
// derived or interpolated because the source reported it missing or invalid.
type ProcessedDay struct {
	Date               string  `json:"date"`
	Forecast           bool    `json:"forecast"`
	TempMaxC           float64 `json:"tempMaxC"`
	TempMinC           float64 `json:"tempMinC"`
	ApparentTempMaxC   float64 `json:"apparentTempMaxC"`
	HeatIndexMaxC      float64 `json:"heatIndexMaxC"`
	UVIndexMax         float64 `json:"uvIndexMax"`
	PrecipitationSumMm float64 `json:"precipitationSumMm"`
	Estimated          bool    `json:"estimated"`
}

// Trend says whether the forecast's last day runs hotter than the days before it.
type Trend struct {
	IsRising bool    `json:"isRising"`
	AnomalyC float64 `json:"anomalyC"`
}

// Quality summarises how much of the snapshot had to be repaired. Score is 1.0
// for a snapshot that needed no repair and falls as more had to be estimated.
type Quality struct {
	Score            float64  `json:"score"`
	HourlyTotal      int      `json:"hourlyTotal"`
	HourlyEstimated  int      `json:"hourlyEstimated"`
	DailyTotal       int      `json:"dailyTotal"`
	DailyEstimated   int      `json:"dailyEstimated"`
	CurrentEstimated []string `json:"currentEstimated"` // field names filled from other data
	OutOfRange       int      `json:"outOfRange"`       // implausible values discarded
}

// ProcessedWeather is the cleaned, enriched view of one weather observation.
type ProcessedWeather struct {
	ObservationID int64            `json:"observationId"`
	Location      Location         `json:"location"`
	Source        string           `json:"source"`
	FetchedAt     time.Time        `json:"fetchedAt"`
	ProcessedAt   time.Time        `json:"processedAt"`
	Current       ProcessedCurrent `json:"current"`
	// Hourly is served by the processing API but left out of events: downstream
	// services work from the daily series, and the series is large.
	Hourly  *ProcessedHourly `json:"hourly,omitempty"`
	Days    []ProcessedDay   `json:"days"`
	Trend   Trend            `json:"trend"`
	Quality Quality          `json:"quality"`
}
