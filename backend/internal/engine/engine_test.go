package engine

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// Floating-point results may differ in the last bits between JS and Go (e.g. the
// compiler may fuse multiply-adds on some CPUs), so floats are compared with a
// tolerance far below anything that could change a classification.
const tol = 1e-9

func near(a, b float64) bool { return math.Abs(a-b) <= tol*math.Max(1, math.Abs(b)) }

type vectors struct {
	ThresholdsC struct {
		Caution        float64 `json:"caution"`
		ExtremeCaution float64 `json:"extremeCaution"`
		Danger         float64 `json:"danger"`
		ExtremeDanger  float64 `json:"extremeDanger"`
	} `json:"thresholdsC"`

	HeatIndex []struct {
		TempC    float64 `json:"tempC"`
		Humidity float64 `json:"humidity"`
		Expected float64 `json:"expected"`
	} `json:"heatIndex"`

	EvaluateHeatRisk []struct {
		TempC         float64   `json:"tempC"`
		ApparentTempC float64   `json:"apparentTempC"`
		DaysMaxTemp   []float64 `json:"daysMaxTemp"`
		Expected      RiskLevel `json:"expected"`
	} `json:"evaluateHeatRisk"`

	AssessHeatwave []struct {
		TempC            float64    `json:"tempC"`
		ApparentTempC    float64    `json:"apparentTempC"`
		Humidity         float64    `json:"humidity"`
		DailyApparentMax []float64  `json:"dailyApparentMax"`
		Expected         Assessment `json:"expected"`
	} `json:"assessHeatwave"`

	TrendAnomaly []struct {
		DailyTempMax []float64 `json:"dailyTempMax"`
		Expected     Trend     `json:"expected"`
	} `json:"trendAnomaly"`

	DailyRiskForecast []struct {
		Daily    DailyWeather        `json:"daily"`
		Expected []DailyRiskForecast `json:"expected"`
	} `json:"dailyRiskForecast"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	// Repo-root testdata/, shared with the TypeScript tests.
	raw, err := os.ReadFile("../../../testdata/engine-vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.HeatIndex) == 0 || len(v.EvaluateHeatRisk) == 0 || len(v.AssessHeatwave) == 0 ||
		len(v.TrendAnomaly) == 0 || len(v.DailyRiskForecast) == 0 {
		t.Fatal("vector file is missing a section; regenerate with `make vectors`")
	}
	return v
}

func TestThresholdsMatchTypeScript(t *testing.T) {
	v := loadVectors(t).ThresholdsC
	if v.Caution != CautionC || v.ExtremeCaution != ExtremeCautionC || v.Danger != DangerC || v.ExtremeDanger != ExtremeDangerC {
		t.Errorf("thresholds differ from TypeScript engine: %+v", v)
	}
}

func TestHeatIndexVectors(t *testing.T) {
	for _, c := range loadVectors(t).HeatIndex {
		if got := HeatIndex(c.TempC, c.Humidity); !near(got, c.Expected) {
			t.Errorf("HeatIndex(%v, %v) = %v, want %v", c.TempC, c.Humidity, got, c.Expected)
		}
	}
}

func TestEvaluateHeatRiskVectors(t *testing.T) {
	for _, c := range loadVectors(t).EvaluateHeatRisk {
		if got := EvaluateHeatRisk(c.ApparentTempC, c.DaysMaxTemp); got != c.Expected {
			t.Errorf("EvaluateHeatRisk(%v, %v) = %s, want %s", c.ApparentTempC, c.DaysMaxTemp, got, c.Expected)
		}
	}
}

func TestAssessHeatwaveVectors(t *testing.T) {
	for i, c := range loadVectors(t).AssessHeatwave {
		got := AssessHeatwave(c.TempC, c.ApparentTempC, c.Humidity, c.DailyApparentMax)
		w := c.Expected
		if got.RiskLevel != w.RiskLevel || got.ConsecutiveDangerDays != w.ConsecutiveDangerDays ||
			got.IsHeatwaveWarning != w.IsHeatwaveWarning || got.Message != w.Message || !near(got.HeatIndexC, w.HeatIndexC) {
			t.Errorf("case %d: got %+v, want %+v", i, got, w)
		}
	}
}

func TestTrendAnomalyVectors(t *testing.T) {
	for _, c := range loadVectors(t).TrendAnomaly {
		got := ComputeTrendAnomaly(c.DailyTempMax)
		if got.IsRising != c.Expected.IsRising || !near(got.AnomalyC, c.Expected.AnomalyC) {
			t.Errorf("ComputeTrendAnomaly(%v) = %+v, want %+v", c.DailyTempMax, got, c.Expected)
		}
	}
}

func TestDailyRiskForecastVectors(t *testing.T) {
	for i, c := range loadVectors(t).DailyRiskForecast {
		got := BuildDailyRiskForecast(c.Daily)
		if len(got) != len(c.Expected) {
			t.Fatalf("case %d: %d days, want %d", i, len(got), len(c.Expected))
		}
		for j := range got {
			if got[j] != c.Expected[j] {
				t.Errorf("case %d day %d: got %+v, want %+v", i, j, got[j], c.Expected[j])
			}
		}
	}
}

// Independent of the vectors: the published NWS example (90°F, 70% RH → ~105.9°F).
func TestHeatIndexMatchesNWSExample(t *testing.T) {
	got := celsiusToFahrenheit(HeatIndex(fahrenheitToCelsius(90), 70))
	if math.Abs(got-105.9) > 0.05 {
		t.Errorf("heat index = %.2f°F, want ≈105.9°F", got)
	}
}

func TestDangerNeedsTwoConsecutiveDays(t *testing.T) {
	cases := []struct {
		name string
		days []float64
		want RiskLevel
	}{
		{"no history", nil, ExtremeCaution},
		{"one hot day", []float64{41}, ExtremeCaution},
		{"two hot days", []float64{41, 41}, Danger},
		{"streak broken by a cooler day", []float64{45, 40.9, 41}, ExtremeCaution},
		{"older streak does not count", []float64{45, 45, 30, 41}, ExtremeCaution},
	}
	for _, c := range cases {
		if got := EvaluateHeatRisk(41, c.days); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestExtremeDangerIgnoresHistory(t *testing.T) {
	if got := EvaluateHeatRisk(54, []float64{10}); got != ExtremeDanger {
		t.Errorf("got %s", got)
	}
}

func TestSeverityAndEscalate(t *testing.T) {
	for i, l := range RiskLevelOrder {
		if l.Severity() != i {
			t.Errorf("%s severity = %d, want %d", l, l.Severity(), i)
		}
		if RiskLevelLabel[l] == "" {
			t.Errorf("%s has no label", l)
		}
	}
	if Normal.Escalate() != Caution || Danger.Escalate() != ExtremeDanger {
		t.Error("Escalate did not move up one tier")
	}
	if ExtremeDanger.Escalate() != ExtremeDanger {
		t.Error("ExtremeDanger must stay at the top")
	}
	if RiskLevel("bogus").Severity() != -1 || RiskLevel("bogus").Escalate() != "bogus" {
		t.Error("unknown level should be left alone")
	}
}

func TestBuildDailyRiskForecastToleratesRaggedSeries(t *testing.T) {
	d := DailyWeather{
		Time:                   []string{"a", "b", "c"},
		Temperature2mMax:       []float64{30, 31, 32},
		Temperature2mMin:       []float64{20, 21},
		ApparentTemperatureMax: []float64{30, 31, 32},
		UVIndexMax:             []float64{5, 5, 5},
		PrecipitationSum:       []float64{0, 0, 0},
	}
	if got := BuildDailyRiskForecast(d); len(got) != 2 {
		t.Errorf("got %d days, want 2 (shortest series)", len(got))
	}
	if got := BuildDailyRiskForecast(DailyWeather{}); len(got) != 0 {
		t.Errorf("empty input gave %d days", len(got))
	}
}

func TestDangerMessageWording(t *testing.T) {
	// Danger needs two consecutive days, so the "1 day" singular form can never
	// appear in practice; this pins the plural wording for the smallest warning.
	got := AssessHeatwave(40, 42, 45, []float64{30, 42, 43})
	if got.RiskLevel != Danger || got.ConsecutiveDangerDays != 2 {
		t.Fatalf("got %+v", got)
	}
	want := "Heatwave warning: dangerous heat has persisted for 2 consecutive days. Heat exhaustion is likely, heat stroke is possible."
	if got.Message != want {
		t.Errorf("message = %q", got.Message)
	}
}
