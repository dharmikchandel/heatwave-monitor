package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
)

// DefaultOpenMeteoURL is the public Open-Meteo forecast endpoint (free, keyless,
// non-commercial use).
const DefaultOpenMeteoURL = "https://api.open-meteo.com/v1/forecast"

// OpenMeteo fetches real weather from the Open-Meteo forecast API.
type OpenMeteo struct {
	BaseURL  string       // defaults to DefaultOpenMeteoURL
	Client   *http.Client // defaults to a 15s-timeout client
	PastDays int          // history days included before today (0–92)
	Forecast int          // forecast days (1–16); default 7
}

func (o *OpenMeteo) Name() string { return "openmeteo" }

type omResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	Current   *struct {
		Time                   string   `json:"time"`
		Temperature2m          *float64 `json:"temperature_2m"`
		RelativeHumidity2m     *float64 `json:"relative_humidity_2m"`
		ApparentTemperature    *float64 `json:"apparent_temperature"`
		WeatherCode            *float64 `json:"weather_code"`
		WindSpeed10m           *float64 `json:"wind_speed_10m"`
		DirectNormalIrradiance *float64 `json:"direct_normal_irradiance"`
	} `json:"current"`
	Hourly struct {
		Time                []string   `json:"time"`
		Temperature2m       []*float64 `json:"temperature_2m"`
		RelativeHumidity2m  []*float64 `json:"relative_humidity_2m"`
		ApparentTemperature []*float64 `json:"apparent_temperature"`
		WindSpeed10m        []*float64 `json:"wind_speed_10m"`
	} `json:"hourly"`
	Daily struct {
		Time                   []string   `json:"time"`
		Temperature2mMax       []*float64 `json:"temperature_2m_max"`
		Temperature2mMin       []*float64 `json:"temperature_2m_min"`
		ApparentTemperatureMax []*float64 `json:"apparent_temperature_max"`
		UVIndexMax             []*float64 `json:"uv_index_max"`
		PrecipitationSum       []*float64 `json:"precipitation_sum"`
	} `json:"daily"`
}

func (o *OpenMeteo) Fetch(ctx context.Context, loc contracts.Location) (contracts.RawSnapshot, error) {
	base := o.BaseURL
	if base == "" {
		base = DefaultOpenMeteoURL
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	forecast := o.Forecast
	if forecast <= 0 {
		forecast = 7
	}

	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Latitude, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m,direct_normal_irradiance")
	q.Set("hourly", "temperature_2m,relative_humidity_2m,apparent_temperature,wind_speed_10m")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,apparent_temperature_max,uv_index_max,precipitation_sum")
	q.Set("timezone", "auto")
	q.Set("forecast_days", strconv.Itoa(forecast))
	if o.PastDays > 0 {
		q.Set("past_days", strconv.Itoa(o.PastDays))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return contracts.RawSnapshot{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return contracts.RawSnapshot{}, &SourceError{Msg: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return contracts.RawSnapshot{}, &SourceError{Status: resp.StatusCode, Msg: "reading response: " + err.Error()}
	}
	if resp.StatusCode/100 != 2 {
		return contracts.RawSnapshot{}, &SourceError{Status: resp.StatusCode, Msg: upstreamReason(body, resp.Status)}
	}

	var r omResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return contracts.RawSnapshot{}, &SourceError{Status: resp.StatusCode, Msg: "malformed response: " + err.Error()}
	}
	if r.Current == nil {
		return contracts.RawSnapshot{}, &SourceError{Status: resp.StatusCode, Msg: "response has no current conditions"}
	}

	snap := contracts.RawSnapshot{
		Latitude:  r.Latitude,
		Longitude: r.Longitude,
		Timezone:  r.Timezone,
		Current: contracts.RawCurrent{
			Time:                   r.Current.Time,
			Temperature2m:          r.Current.Temperature2m,
			RelativeHumidity2m:     r.Current.RelativeHumidity2m,
			ApparentTemperature:    r.Current.ApparentTemperature,
			WeatherCode:            r.Current.WeatherCode,
			WindSpeed10m:           r.Current.WindSpeed10m,
			DirectNormalIrradiance: r.Current.DirectNormalIrradiance,
		},
		Hourly: contracts.RawHourly{
			Time:                r.Hourly.Time,
			Temperature2m:       r.Hourly.Temperature2m,
			RelativeHumidity2m:  r.Hourly.RelativeHumidity2m,
			ApparentTemperature: r.Hourly.ApparentTemperature,
			WindSpeed10m:        r.Hourly.WindSpeed10m,
		},
		Daily: contracts.RawDaily{
			Time:                   r.Daily.Time,
			Temperature2mMax:       r.Daily.Temperature2mMax,
			Temperature2mMin:       r.Daily.Temperature2mMin,
			ApparentTemperatureMax: r.Daily.ApparentTemperatureMax,
			UVIndexMax:             r.Daily.UVIndexMax,
			PrecipitationSum:       r.Daily.PrecipitationSum,
		},
	}
	if err := snap.Validate(); err != nil {
		return contracts.RawSnapshot{}, &SourceError{Status: resp.StatusCode, Msg: "inconsistent response: " + err.Error()}
	}
	return snap, nil
}

// upstreamReason extracts Open-Meteo's {"error":true,"reason":"..."} message,
// falling back to the HTTP status line.
func upstreamReason(body []byte, status string) string {
	var e struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &e) == nil && e.Reason != "" {
		return e.Reason
	}
	return strings.TrimSpace(fmt.Sprint(status))
}
