package risk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/contracts"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
)

var t0 = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// loadSample reads what the Python prediction service really emits (see
// testdata/heatwave-predicted.sample.json): 3 history days then 7 forecast days
// climbing from 41.8 °C to 55.2 °C apparent, with a probability for each.
func loadSample(t *testing.T) contracts.HeatwavePredicted {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/heatwave-predicted.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in contracts.HeatwavePredicted
	if err := dec.Decode(&in); err != nil {
		t.Fatal(err)
	}
	return in
}

// spec describes a day by its apparent max; max/min temperature are derived.
type spec struct {
	apparent float64
	forecast bool
}

// weatherOf builds a ProcessedWeather from day specs, with the current apparent temperature.
func weatherOf(currentApparent float64, days ...spec) contracts.ProcessedWeather {
	w := contracts.ProcessedWeather{
		Location: contracts.Location{ID: 1, Name: "Testville"},
		Current:  contracts.ProcessedCurrent{ApparentTemperatureC: currentApparent, TemperatureC: currentApparent - 3, HeatIndexC: currentApparent},
	}
	for i, d := range days {
		w.Days = append(w.Days, contracts.ProcessedDay{
			Date:             fmt.Sprintf("2026-05-%02d", i+1),
			Forecast:         d.forecast,
			TempMaxC:         d.apparent - 3,
			TempMinC:         d.apparent - 13,
			ApparentTempMaxC: d.apparent,
		})
	}
	return w
}

func hist(vs ...float64) []spec {
	var out []spec
	for _, v := range vs {
		out = append(out, spec{v, false})
	}
	return out
}

func fcst(vs ...float64) []spec {
	var out []spec
	for _, v := range vs {
		out = append(out, spec{v, true})
	}
	return out
}

func concat(parts ...[]spec) []spec {
	var out []spec
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// predictionFor gives each forecast day the given probability.
func predictionFor(w contracts.ProcessedWeather, probs ...float64) contracts.Prediction {
	p := contracts.Prediction{Method: "model", ModelVersion: "lr-test", GeneratedAt: t0}
	i := 0
	for _, d := range w.Days {
		if !d.Forecast {
			continue
		}
		prob := 0.0
		if i < len(probs) {
			prob = probs[i]
		}
		p.Days = append(p.Days, contracts.DayProbability{Date: d.Date, Horizon: i, Probability: prob})
		i++
	}
	return p
}

func assess(t *testing.T, w contracts.ProcessedWeather, p contracts.Prediction) contracts.Assessment {
	t.Helper()
	a, err := Assess(w, p, t0)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	return a
}

func rejectReason(t *testing.T, w contracts.ProcessedWeather, p contracts.Prediction) string {
	t.Helper()
	_, err := Assess(w, p, t0)
	var rej *RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want RejectError", err)
	}
	return rej.Reason
}

func levels(a contracts.Assessment) []engine.RiskLevel {
	var out []engine.RiskLevel
	for _, d := range a.Days {
		out = append(out, d.Level)
	}
	return out
}

func TestAssessesWhatThePredictionServiceActuallyEmits(t *testing.T) {
	in := loadSample(t)
	a := assess(t, in.Weather, in.Prediction)

	// Now: 41.8 °C, but the day before was only 38.2 °C, so Danger's "two days in a row" is not met yet.
	if a.Now.Level != engine.ExtremeCaution || a.Now.ConsecutiveHotDays != 1 {
		t.Errorf("now = %+v, want extreme-caution after one hot day", a.Now)
	}

	want := []engine.RiskLevel{engine.ExtremeCaution, engine.Danger, engine.Danger, engine.Danger, engine.ExtremeDanger, engine.Danger, engine.Danger}
	got := levels(a)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("forecast levels = %v, want %v", got, want)
	}
	for i, d := range a.Days {
		if d.Horizon != i || d.Date != in.Prediction.Days[i].Date || d.Probability != in.Prediction.Days[i].Probability {
			t.Errorf("day %d does not line up with the prediction: %+v", i, d)
		}
	}

	if !a.HeatwaveExpected || len(a.WarningDays) != 6 || a.WarningDays[0] != "2026-05-02" {
		t.Errorf("warning days = %v", a.WarningDays)
	}
	if a.Peak.Date != "2026-05-05" || a.Peak.Level != engine.ExtremeDanger || a.AlertLevel != engine.ExtremeDanger {
		t.Errorf("peak = %+v, alert level %s", a.Peak, a.AlertLevel)
	}
	if a.Method != "model" || a.ModelVersion != in.Prediction.ModelVersion || !a.AssessedAt.Equal(t0) {
		t.Errorf("provenance wrong: %s / %s / %v", a.Method, a.ModelVersion, a.AssessedAt)
	}
}

func TestBaseLevelsMatchTheDashboardEngine(t *testing.T) {
	in := loadSample(t)
	a := assess(t, in.Weather, in.Prediction)

	var daily engine.DailyWeather
	for _, d := range in.Weather.Days {
		daily.Time = append(daily.Time, d.Date)
		daily.Temperature2mMax = append(daily.Temperature2mMax, d.TempMaxC)
		daily.Temperature2mMin = append(daily.Temperature2mMin, d.TempMinC)
		daily.ApparentTemperatureMax = append(daily.ApparentTemperatureMax, d.ApparentTempMaxC)
		daily.UVIndexMax = append(daily.UVIndexMax, d.UVIndexMax)
		daily.PrecipitationSum = append(daily.PrecipitationSum, d.PrecipitationSumMm)
	}
	all := engine.BuildDailyRiskForecast(daily)
	for i, d := range a.Days {
		if d.BaseLevel != all[3+i].RiskLevel { // 3 history days come first
			t.Errorf("%s: base level %s != engine %s", d.Date, d.BaseLevel, all[3+i].RiskLevel)
		}
	}
}

func TestEscalationRules(t *testing.T) {
	cases := []struct {
		name     string
		apparent float64
		prob     float64
		want     engine.RiskLevel
		escalate bool
		history  []float64 // defaults to a cool spell, so one hot day cannot already be Danger
	}{
		{"caution at exactly 0.7 escalates", 33, 0.70, engine.ExtremeCaution, true, nil},
		{"caution just under 0.7 does not", 33, 0.699, engine.Caution, false, nil},
		{"extreme caution escalates to danger", 39, 0.85, engine.Danger, true, nil},
		{"single hot day (extreme caution) escalates", 42, 0.9, engine.Danger, true, nil},
		{"danger is not raised further", 45, 0.99, engine.Danger, false, []float64{45, 45, 45}},
		{"extreme danger stays", 55, 1.0, engine.ExtremeDanger, false, nil},
		{"normal is never raised", 25, 1.0, engine.Normal, false, nil},
		{"low probability changes nothing", 39, 0.1, engine.ExtremeCaution, false, nil},
	}
	for _, c := range cases {
		h := c.history
		if h == nil {
			h = []float64{25, 25, 25}
		}
		w := weatherOf(25, concat(hist(h...), fcst(c.apparent))...)
		a := assess(t, w, predictionFor(w, c.prob))
		d := a.Days[0]
		if d.Level != c.want || d.Escalated != c.escalate {
			t.Errorf("%s: level %s escalated=%v, want %s / %v", c.name, d.Level, d.Escalated, c.want, c.escalate)
		}
		if d.Escalated && (d.Level.Severity() != d.BaseLevel.Severity()+1) {
			t.Errorf("%s: escalation moved more than one tier (%s -> %s)", c.name, d.BaseLevel, d.Level)
		}
		if c.escalate && !strings.Contains(d.Reason, "Raised to") {
			t.Errorf("%s: reason does not explain the escalation: %q", c.name, d.Reason)
		}
	}
}

func TestEscalatedDaysCountAsWarningDays(t *testing.T) {
	w := weatherOf(25, concat(hist(25, 25, 25), fcst(42, 30))...)
	a := assess(t, w, predictionFor(w, 0.9, 0))
	if !a.HeatwaveExpected || len(a.WarningDays) != 1 || a.AlertLevel != engine.Danger {
		t.Errorf("a day raised to Danger by probability must raise the early warning: %+v", a)
	}
}

func TestYesterdayCountsTowardsDangerNow(t *testing.T) {
	// Two hot days in a row (yesterday and now) is Danger; the same reading after a cool day is not.
	w := weatherOf(43, concat(hist(30, 30, 42), fcst(43, 43))...)
	if a := assess(t, w, predictionFor(w)); a.Now.Level != engine.Danger || a.Now.ConsecutiveHotDays != 2 {
		t.Errorf("now = %+v, want Danger on the second hot day", a.Now)
	}
	w = weatherOf(43, concat(hist(30, 30, 30), fcst(43, 43))...)
	if a := assess(t, w, predictionFor(w)); a.Now.Level != engine.ExtremeCaution {
		t.Errorf("now = %+v, want Extreme Caution after a cool day", a.Now)
	}
	w = weatherOf(43, fcst(43, 43)...) // no history at all: still works, conservatively
	if a := assess(t, w, predictionFor(w)); a.Now.Level != engine.ExtremeCaution {
		t.Errorf("no history: now = %+v", a.Now)
	}
}

func TestHistoryInformsTheFirstForecastDay(t *testing.T) {
	w := weatherOf(43, concat(hist(30, 42, 42), fcst(43, 30))...)
	a := assess(t, w, predictionFor(w))
	if a.Days[0].Level != engine.Danger || !strings.Contains(a.Days[0].Reason, "3 consecutive days") {
		t.Errorf("first forecast day = %+v", a.Days[0])
	}
	if a.Days[1].Level != engine.Normal {
		t.Errorf("a cool day after the heat must drop back: %s", a.Days[1].Level)
	}
}

func TestAlertLevelIsTheWorseOfNowAndTheForecast(t *testing.T) {
	// Dangerous now, cooling down: the alert level must not forget about now.
	w := weatherOf(55, concat(hist(40, 45, 50), fcst(55, 30, 30))...)
	a := assess(t, w, predictionFor(w))
	if a.Now.Level != engine.ExtremeDanger || a.AlertLevel != engine.ExtremeDanger {
		t.Errorf("now %s, alert %s", a.Now.Level, a.AlertLevel)
	}
	// Mild now, heatwave coming: the alert level looks ahead.
	w = weatherOf(28, concat(hist(28, 28, 28), fcst(28, 43, 44, 28))...)
	a = assess(t, w, predictionFor(w))
	if a.Now.Level != engine.Normal || a.AlertLevel != engine.Danger || !a.HeatwaveExpected {
		t.Errorf("now %s, alert %s, expected %v", a.Now.Level, a.AlertLevel, a.HeatwaveExpected)
	}
}

func TestPeakPrefersSeverityThenProbabilityThenEarlierDay(t *testing.T) {
	w := weatherOf(25, concat(hist(25, 25, 25), fcst(25, 44, 44, 44))...)
	// d1 (single hot day, extreme caution then danger from d2 on): d2, d3 share the level; d3 more probable
	a := assess(t, w, predictionFor(w, 0, 0.5, 0.6, 0.9))
	if a.Peak.Date != "2026-05-07" {
		t.Errorf("peak = %s, want the most probable of the equally severe days (05-07)", a.Peak.Date)
	}
	a = assess(t, w, predictionFor(w, 0, 0.5, 0.6, 0.6))
	if a.Peak.Date != "2026-05-06" {
		t.Errorf("peak = %s, want the earlier day on a tie", a.Peak.Date)
	}
}

func TestMissingPredictionDayIsNoSignalNotCertainty(t *testing.T) {
	w := weatherOf(25, concat(hist(25, 25, 25), fcst(33, 33))...)
	p := predictionFor(w, 0.95, 0.95)
	p.Days = p.Days[:1] // the predictor skipped the second day
	a := assess(t, w, p)
	if a.Days[0].Level != engine.ExtremeCaution || a.Days[1].Level != engine.Caution || a.Days[1].Probability != 0 {
		t.Errorf("days = %+v", a.Days)
	}
}

func TestScoresNeverContradictTheirTier(t *testing.T) {
	prev := -1
	for _, c := range []struct {
		apparent float64
		level    engine.RiskLevel
	}{{20, engine.Normal}, {33, engine.Caution}, {39, engine.ExtremeCaution}, {44, engine.Danger}, {56, engine.ExtremeDanger}} {
		w := weatherOf(c.apparent, concat(hist(c.apparent, c.apparent, c.apparent), fcst(c.apparent, c.apparent))...)
		a := assess(t, w, predictionFor(w))
		s := a.Days[1].Score
		if s < tierFloor[a.Days[1].Level] || s > 100 || s < 0 {
			t.Errorf("%s: score %d outside [%d, 100]", c.level, s, tierFloor[a.Days[1].Level])
		}
		if s <= prev {
			t.Errorf("%s: score %d did not rise above the milder tier's %d", c.level, s, prev)
		}
		prev = s
	}

	// A likely warning lifts the score even when the temperature alone would not.
	w := weatherOf(25, concat(hist(25, 25, 25), fcst(33))...)
	low, high := assess(t, w, predictionFor(w, 0)).Days[0].Score, assess(t, w, predictionFor(w, 0.75)).Days[0].Score
	if high < 75 || high <= low {
		t.Errorf("probability 0.75 gave score %d (vs %d without it)", high, low)
	}
	if math.Abs(temperatureScore(54)-100) > 1e-9 || temperatureScore(27) != 0 || temperatureScore(10) != 0 || temperatureScore(90) != 100 {
		t.Error("temperatureScore anchors are wrong")
	}
}

func TestInvariantsHoldForRandomWeather(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for n := 0; n < 3000; n++ {
		var days []spec
		for i := rng.Intn(4); i > 0; i-- {
			days = append(days, spec{20 + rng.Float64()*40, false})
		}
		forecast := 1 + rng.Intn(8)
		for i := 0; i < forecast; i++ {
			days = append(days, spec{20 + rng.Float64()*40, true})
		}
		w := weatherOf(20+rng.Float64()*40, days...)
		probs := make([]float64, forecast)
		for i := range probs {
			probs[i] = rng.Float64()
		}
		a := assess(t, w, predictionFor(w, probs...))

		if len(a.Days) != forecast {
			t.Fatalf("got %d days for %d forecast days", len(a.Days), forecast)
		}
		best := a.Days[0]
		warnings := 0
		for i, d := range a.Days {
			if d.Level.Severity() < d.BaseLevel.Severity() {
				t.Fatalf("level below base: %+v", d)
			}
			if d.Escalated != (d.Level != d.BaseLevel) || (d.Escalated && d.Level.Severity() != d.BaseLevel.Severity()+1) {
				t.Fatalf("bad escalation: %+v", d)
			}
			if d.Escalated && (d.Probability < EscalationProbability || d.BaseLevel.Severity() >= engine.Danger.Severity()) {
				t.Fatalf("escalated without grounds: %+v", d)
			}
			if d.Score < tierFloor[d.Level] || d.Score > 100 || d.Probability != probs[i] || d.Horizon != i {
				t.Fatalf("bad day: %+v", d)
			}
			if d.Level.Severity() >= engine.Danger.Severity() {
				warnings++
			}
			if d.Level.Severity() > best.Level.Severity() {
				best = d
			}
		}
		if a.Peak.Level != best.Level {
			t.Fatalf("peak level %s, but a day reaches %s", a.Peak.Level, best.Level)
		}
		if a.HeatwaveExpected != (warnings > 0) || len(a.WarningDays) != warnings {
			t.Fatalf("warning bookkeeping wrong: %+v", a)
		}
		wantAlert := a.Now.Level
		if a.Peak.Level.Severity() > wantAlert.Severity() {
			wantAlert = a.Peak.Level
		}
		if a.AlertLevel != wantAlert || len(a.Rationale) < 2 {
			t.Fatalf("alert level %s want %s; rationale %v", a.AlertLevel, wantAlert, a.Rationale)
		}
	}
}

func TestRationaleReadsSensibly(t *testing.T) {
	in := loadSample(t)
	a := assess(t, in.Weather, in.Prediction)
	text := strings.Join(a.Rationale, "\n")
	for _, want := range []string{
		"Now: Extreme Caution",
		"only for 1 day in a row",
		"Heatwave expected: Danger or worse on 6 forecast day(s)",
		"worst is Extreme Danger on 2026-05-05",
		"Heatwave-warning probability peaks at 100%",
		"trained model " + in.Prediction.ModelVersion,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rationale is missing %q:\n%s", want, text)
		}
	}

	calm := weatherOf(25, concat(hist(25, 25, 25), fcst(25, 26, 25))...)
	if r := assess(t, calm, predictionFor(calm)).Rationale; len(r) != 2 || r[1] != "No forecast day rises above Normal." {
		t.Errorf("calm rationale = %v", r)
	}

	warm := weatherOf(25, concat(hist(25, 25, 25), fcst(25, 34, 25))...)
	r := assess(t, warm, predictionFor(warm))
	if !strings.Contains(strings.Join(r.Rationale, " "), "No Danger days in the forecast; the hottest is Caution on 2026-05-05 (34.0 °C)") {
		t.Errorf("warm rationale = %v", r.Rationale)
	}

	rules := loadSample(t)
	rules.Prediction.Method, rules.Prediction.ModelVersion = "rules", "rules-v1"
	if txt := strings.Join(assess(t, rules.Weather, rules.Prediction).Rationale, "\n"); !strings.Contains(txt, "rule-based estimate") {
		t.Errorf("rules-based prediction should be labelled as such:\n%s", txt)
	}

	rising := weatherOf(25, concat(hist(25, 25, 25), fcst(25, 26))...)
	rising.Trend = contracts.Trend{IsRising: true, AnomalyC: 2.5}
	if txt := strings.Join(assess(t, rising, predictionFor(rising)).Rationale, "\n"); !strings.Contains(txt, "trending hotter (+2.5 °C") {
		t.Errorf("trend missing:\n%s", txt)
	}
}

func TestRejectsUnusableInput(t *testing.T) {
	good := weatherOf(30, concat(hist(30, 30, 30), fcst(33, 33))...)

	if r := rejectReason(t, contracts.ProcessedWeather{}, contracts.Prediction{}); r != ReasonInvalid {
		t.Errorf("empty weather: %s", r)
	}
	noForecast := weatherOf(30, hist(30, 30, 30)...)
	if r := rejectReason(t, noForecast, predictionFor(noForecast)); r != ReasonNoForecast {
		t.Errorf("no forecast days: %s", r)
	}
	nan := weatherOf(30, concat(hist(30, 30, 30), fcst(33))...)
	nan.Days[3].ApparentTempMaxC = math.NaN()
	if r := rejectReason(t, nan, predictionFor(nan)); r != ReasonInvalid {
		t.Errorf("NaN day: %s", r)
	}
	badCurrent := weatherOf(math.Inf(1), concat(hist(30, 30, 30), fcst(33))...)
	if r := rejectReason(t, badCurrent, predictionFor(badCurrent)); r != ReasonInvalid {
		t.Errorf("infinite current: %s", r)
	}
	unordered := weatherOf(30, concat(hist(30, 30, 30), fcst(33, 33))...)
	unordered.Days[1].Date = unordered.Days[0].Date
	if r := rejectReason(t, unordered, predictionFor(unordered)); r != ReasonInvalid {
		t.Errorf("duplicate dates: %s", r)
	}
	for _, p := range []float64{-0.1, 1.5, math.NaN()} {
		if r := rejectReason(t, good, predictionFor(good, p)); r != ReasonInvalid {
			t.Errorf("probability %v: %s", p, r)
		}
	}
}

func TestAssessDoesNotModifyItsInput(t *testing.T) {
	in := loadSample(t)
	before, _ := json.Marshal(in)
	assess(t, in.Weather, in.Prediction)
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Error("Assess mutated its input")
	}
}
