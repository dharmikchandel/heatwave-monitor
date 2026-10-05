package contracts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/alert"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/gateway"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/processing"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/risk"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/weather"
)

// Shared fixtures for the Go <-> Python contract (repo-root testdata/):
//
//	weather-processed.sample.json    written by Go, must be accepted by the Python service
//	heatwave-predicted.sample.json   written by Python, must decode strictly into Go's types
//	climate.sample.json              written by Go: what GET /api/v1/locations/{id}/climate returns;
//	                                 the frontend's tests parse it, so its types track the real API
//
// Regenerate the Go-written one with: UPDATE_FIXTURES=1 go test ./internal/contracts

const fixtureDir = "../../../testdata"

type scenario weather.Scenario

func (s scenario) ScenarioFor(context.Context, int64) (weather.Scenario, error) {
	return weather.Scenario(s), nil
}

func sampleProcessed(t *testing.T) []byte {
	t.Helper()
	loc := contracts.Location{ID: 1, Name: "Mumbai", Country: "India", Admin1: "Maharashtra", Latitude: 19.076, Longitude: 72.8777, Timezone: "Asia/Kolkata"}
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	snap, err := (&weather.Simulated{Scenarios: scenario(weather.ScenarioBuilding), Now: func() time.Time { return now }}).Fetch(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := processing.Process(snap)
	if err != nil {
		t.Fatal(err)
	}
	pw.ObservationID, pw.Location, pw.Source = 42, loc, "simulated"
	pw.FetchedAt, pw.ProcessedAt = now, now.Add(time.Second)
	pw.Hourly = nil // events to prediction omit the hourly series

	out, err := json.MarshalIndent(pw, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestProcessedSampleMatchesWhatGoProduces(t *testing.T) {
	path := filepath.Join(fixtureDir, "weather-processed.sample.json")
	want := sampleProcessed(t)

	if os.Getenv("UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture (regenerate with UPDATE_FIXTURES=1): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: the processed-weather shape or the simulator/processing output changed.\n"+
			"Regenerate with: UPDATE_FIXTURES=1 go test ./internal/contracts   (then re-run the Python tests)", path)
	}
}

func TestPredictedSampleDecodesStrictly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "heatwave-predicted.sample.json"))
	if err != nil {
		t.Fatalf("read fixture (generate with the Python tests, UPDATE_FIXTURES=1): %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields() // a field Python adds or renames must fail here, not at runtime
	var ev contracts.HeatwavePredicted
	if err := dec.Decode(&ev); err != nil {
		t.Fatalf("Python's heatwave.predicted payload does not match the Go contract: %v", err)
	}

	p := ev.Prediction
	if p.Method == "" || p.ModelVersion == "" || p.GeneratedAt.IsZero() || len(p.Days) == 0 {
		t.Errorf("prediction incomplete: %+v", p)
	}
	best := p.Days[0]
	for _, d := range p.Days {
		if d.Probability < 0 || d.Probability > 1 {
			t.Errorf("probability out of range: %+v", d)
		}
		if d.Probability > best.Probability {
			best = d
		}
	}
	if p.Peak.Probability != best.Probability {
		t.Errorf("peak %+v is not the most likely day %+v", p.Peak, best)
	}
	if ev.Weather.Location.ID == 0 || len(ev.Weather.Days) == 0 || ev.Weather.Hourly != nil {
		t.Errorf("forwarded weather payload wrong: %+v", ev.Weather.Location)
	}
}

// sampleClimate builds what the gateway serves for one location, from the real code of
// every service: simulated weather -> processing -> (the Python service's committed
// prediction) -> risk -> an alert -> the gateway's composed response.
func sampleClimate(t *testing.T) []byte {
	t.Helper()
	loc := contracts.Location{ID: 1, Name: "Mumbai", Country: "India", Admin1: "Maharashtra", Latitude: 19.076, Longitude: 72.8777, Timezone: "Asia/Kolkata"}
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	snap, err := (&weather.Simulated{Scenarios: scenario(weather.ScenarioBuilding), Now: func() time.Time { return now }}).Fetch(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := processing.Process(snap) // keeps the hourly series: the processing API serves it
	if err != nil {
		t.Fatal(err)
	}
	pw.ObservationID, pw.Location, pw.Source = 42, loc, "simulated"
	pw.FetchedAt, pw.ProcessedAt = now, now.Add(time.Second)

	raw, err := os.ReadFile(filepath.Join(fixtureDir, "heatwave-predicted.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	var predicted contracts.HeatwavePredicted
	if err := json.Unmarshal(raw, &predicted); err != nil {
		t.Fatal(err)
	}
	assessment, err := risk.Assess(pw, predicted.Prediction, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	openAlert := alert.Alert{
		ID: 3, LocationID: loc.ID, LocationName: loc.Name, Status: "open",
		CurrentLevel: assessment.AlertLevel, PeakLevel: assessment.AlertLevel,
		Headline: "Extreme Danger heat alert for Mumbai", Summary: "Heatwave expected.",
		Details: alert.AlertDetails{ObservationID: 42, Now: assessment.Now, Peak: assessment.Peak, HeatwaveExpected: true,
			WarningDays: assessment.WarningDays, Rationale: assessment.Rationale, Method: assessment.Method, ModelVersion: assessment.ModelVersion},
		OpenedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second),
	}

	marshal := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	out, err := json.Marshal(gateway.Climate{
		Location:    marshal(weather.Location{Location: loc, Active: true, CreatedAt: now}),
		Weather:     marshal(pw),
		Prediction:  marshal(predicted.Prediction),
		Risk:        marshal(assessment),
		Alerts:      marshal([]alert.Alert{openAlert}),
		Status:      "ok",
		Sources:     map[string]string{"weather": "ok", "processing": "ok", "prediction": "ok", "risk": "ok", "alerts": "ok"},
		GeneratedAt: now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestClimateSampleMatchesWhatGoProduces(t *testing.T) {
	path := filepath.Join(fixtureDir, "climate.sample.json")
	want := sampleClimate(t)

	if os.Getenv("UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture (regenerate with UPDATE_FIXTURES=1): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: a service's output or the gateway's response shape changed.\n"+
			"Regenerate with: UPDATE_FIXTURES=1 go test ./internal/contracts   (then run the frontend tests)", path)
	}
}
