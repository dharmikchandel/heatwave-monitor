package weather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
)

const omFixture = `{
  "latitude": 19.125, "longitude": 72.875, "timezone": "Asia/Kolkata",
  "current": {"time": "2026-05-01T14:30", "temperature_2m": 36.4, "relative_humidity_2m": 41,
              "apparent_temperature": 38.2, "weather_code": 1, "wind_speed_10m": 11.2, "direct_normal_irradiance": null},
  "hourly": {"time": ["2026-05-01T00:00","2026-05-01T01:00"],
             "temperature_2m": [29.1, null], "relative_humidity_2m": [60, 61],
             "apparent_temperature": [31.0, 31.4], "wind_speed_10m": [8.0, null]},
  "daily": {"time": ["2026-05-01"], "temperature_2m_max": [37.0], "temperature_2m_min": [27.5],
            "apparent_temperature_max": [40.1], "uv_index_max": [null], "precipitation_sum": [0.0]}
}`

func omServer(t *testing.T, status int, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(r.Context())
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestOpenMeteoMapsResponseAndKeepsNulls(t *testing.T) {
	srv, req := omServer(t, 200, omFixture)
	src := &OpenMeteo{BaseURL: srv.URL, PastDays: 3}

	snap, err := src.Fetch(context.Background(), contracts.Location{Latitude: 19.076, Longitude: 72.8777})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Timezone != "Asia/Kolkata" || snap.Current.Time != "2026-05-01T14:30" || *snap.Current.Temperature2m != 36.4 {
		t.Errorf("unexpected mapping: %+v", snap.Current)
	}
	if snap.Current.DirectNormalIrradiance != nil || snap.Hourly.Temperature2m[1] != nil || snap.Daily.UVIndexMax[0] != nil {
		t.Error("API nulls were not preserved")
	}
	if *snap.Daily.ApparentTemperatureMax[0] != 40.1 {
		t.Errorf("daily mapping wrong: %+v", snap.Daily)
	}

	q := req.URL.Query()
	for k, want := range map[string]string{
		"latitude": "19.076", "longitude": "72.8777", "timezone": "auto", "forecast_days": "7", "past_days": "3",
	} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, field := range []string{"temperature_2m", "relative_humidity_2m", "apparent_temperature", "wind_speed_10m"} {
		if !strings.Contains(q.Get("hourly"), field) {
			t.Errorf("hourly query is missing %s: %s", field, q.Get("hourly"))
		}
	}
}

func TestOpenMeteoOmitsPastDaysWhenZero(t *testing.T) {
	srv, req := omServer(t, 200, omFixture)
	if _, err := (&OpenMeteo{BaseURL: srv.URL}).Fetch(context.Background(), contracts.Location{}); err != nil {
		t.Fatal(err)
	}
	if req.URL.Query().Has("past_days") {
		t.Error("past_days sent although PastDays is 0")
	}
}

func TestOpenMeteoErrors(t *testing.T) {
	cases := map[string]struct {
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		"rate limited":  {429, `{"error":true,"reason":"Too many requests"}`, 429, "Too many requests"},
		"server error":  {503, `upstream down`, 503, "503"},
		"not json":      {200, `<html>`, 200, "malformed"},
		"no current":    {200, `{"hourly":{"time":[]}}`, 200, "no current"},
		"ragged series": {200, strings.Replace(omFixture, `"temperature_2m": [29.1, null]`, `"temperature_2m": [29.1]`, 1), 200, "inconsistent"},
	}
	for name, c := range cases {
		srv, _ := omServer(t, c.status, c.body)
		_, err := (&OpenMeteo{BaseURL: srv.URL}).Fetch(context.Background(), contracts.Location{})
		var serr *SourceError
		if !errors.As(err, &serr) {
			t.Errorf("%s: err = %v, want SourceError", name, err)
			continue
		}
		if serr.Status != c.wantStatus || !strings.Contains(serr.Error(), c.wantText) {
			t.Errorf("%s: got %q (status %d), want status %d containing %q", name, serr.Error(), serr.Status, c.wantStatus, c.wantText)
		}
	}
}

func TestOpenMeteoUnreachableIsSourceError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := (&OpenMeteo{BaseURL: url}).Fetch(context.Background(), contracts.Location{})
	var serr *SourceError
	if !errors.As(err, &serr) || serr.Status != 0 {
		t.Errorf("err = %v, want transport SourceError with status 0", err)
	}
}

func TestOpenMeteoHonoursContextCancellation(t *testing.T) {
	srv, _ := omServer(t, 200, omFixture)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&OpenMeteo{BaseURL: srv.URL}).Fetch(ctx, contracts.Location{}); err == nil {
		t.Error("expected an error for a cancelled context")
	}
}
