// Package contracts holds the event payloads services exchange. Payloads are
// self-contained so a consumer never has to call the producer back. JSON uses
// camelCase, matching the frontend types.
package contracts

import (
	"errors"
	"fmt"
	"time"
)

// Event types and the outbox target names used to route them.
const (
	EventWeatherUpdated   = "weather.updated"
	EventWeatherProcessed = "weather.processed"

	TargetProcessing = "processing"
	TargetPrediction = "prediction"
)

// Location identifies a watched place. It travels inside events so downstream
// services need no location registry of their own.
type Location struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	Admin1    string  `json:"admin1,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

// RawCurrent is the latest observation exactly as the weather source reported
// it. Any value may be null: cleaning is the processing service's job.
type RawCurrent struct {
	Time                   string   `json:"time"`
	Temperature2m          *float64 `json:"temperature2m"`
	RelativeHumidity2m     *float64 `json:"relativeHumidity2m"`
	ApparentTemperature    *float64 `json:"apparentTemperature"`
	WeatherCode            *float64 `json:"weatherCode"`
	WindSpeed10m           *float64 `json:"windSpeed10m"`
	DirectNormalIrradiance *float64 `json:"directNormalIrradiance"`
}

// RawHourly holds parallel hourly series (local time, oldest first). Entries
// may be null.
type RawHourly struct {
	Time                []string   `json:"time"`
	Temperature2m       []*float64 `json:"temperature2m"`
	RelativeHumidity2m  []*float64 `json:"relativeHumidity2m"`
	ApparentTemperature []*float64 `json:"apparentTemperature"`
	WindSpeed10m        []*float64 `json:"windSpeed10m"`
}

// RawDaily holds parallel daily series (local dates, oldest first, past days
// first). Entries may be null.
type RawDaily struct {
	Time                   []string   `json:"time"`
	Temperature2mMax       []*float64 `json:"temperature2mMax"`
	Temperature2mMin       []*float64 `json:"temperature2mMin"`
	ApparentTemperatureMax []*float64 `json:"apparentTemperatureMax"`
	UVIndexMax             []*float64 `json:"uvIndexMax"`
	PrecipitationSum       []*float64 `json:"precipitationSum"`
}

// RawSnapshot is one uncleaned weather observation for a location: the current
// conditions, the hourly series and the daily series (a few past days followed
// by the forecast).
type RawSnapshot struct {
	Latitude  float64    `json:"latitude"`
	Longitude float64    `json:"longitude"`
	Timezone  string     `json:"timezone"`
	Current   RawCurrent `json:"current"`
	Hourly    RawHourly  `json:"hourly"`
	Daily     RawDaily   `json:"daily"`
}

// Validate checks structural consistency (not value quality): every series must
// be as long as its time axis, so consumers can index them in parallel.
func (s RawSnapshot) Validate() error {
	if s.Current.Time == "" {
		return errors.New("snapshot has no current time")
	}
	h, d := s.Hourly, s.Daily
	if len(h.Time) == 0 || len(d.Time) == 0 {
		return errors.New("snapshot has an empty hourly or daily time axis")
	}
	for name, n := range map[string]int{
		"hourly.temperature2m":       len(h.Temperature2m),
		"hourly.relativeHumidity2m":  len(h.RelativeHumidity2m),
		"hourly.apparentTemperature": len(h.ApparentTemperature),
		"hourly.windSpeed10m":        len(h.WindSpeed10m),
	} {
		if n != len(h.Time) {
			return fmt.Errorf("%s has %d values for %d hours", name, n, len(h.Time))
		}
	}
	for name, n := range map[string]int{
		"daily.temperature2mMax":       len(d.Temperature2mMax),
		"daily.temperature2mMin":       len(d.Temperature2mMin),
		"daily.apparentTemperatureMax": len(d.ApparentTemperatureMax),
		"daily.uvIndexMax":             len(d.UVIndexMax),
		"daily.precipitationSum":       len(d.PrecipitationSum),
	} {
		if n != len(d.Time) {
			return fmt.Errorf("%s has %d values for %d days", name, n, len(d.Time))
		}
	}
	return nil
}

// WeatherUpdated is the payload of EventWeatherUpdated.
type WeatherUpdated struct {
	ObservationID int64       `json:"observationId"`
	Location      Location    `json:"location"`
	Source        string      `json:"source"` // "openmeteo" or "simulated"
	FetchedAt     time.Time   `json:"fetchedAt"`
	Snapshot      RawSnapshot `json:"snapshot"`
}
