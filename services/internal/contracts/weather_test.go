package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }

func validSnapshot() RawSnapshot {
	return RawSnapshot{
		Current: RawCurrent{Time: "2026-05-01T12:00"},
		Hourly: RawHourly{
			Time:                []string{"2026-05-01T00:00", "2026-05-01T01:00"},
			Temperature2m:       []*float64{f(30), nil},
			RelativeHumidity2m:  []*float64{f(50), f(51)},
			ApparentTemperature: []*float64{f(31), f(32)},
			WindSpeed10m:        []*float64{f(5), f(6)},
		},
		Daily: RawDaily{
			Time:                   []string{"2026-05-01"},
			Temperature2mMax:       []*float64{f(35)},
			Temperature2mMin:       []*float64{f(25)},
			ApparentTemperatureMax: []*float64{f(37)},
			UVIndexMax:             []*float64{f(9)},
			PrecipitationSum:       []*float64{f(0)},
		},
	}
}

func TestValidateAcceptsNullsButNotRaggedSeries(t *testing.T) {
	if err := validSnapshot().Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	cases := map[string]func(*RawSnapshot){
		"no current time":      func(s *RawSnapshot) { s.Current.Time = "" },
		"empty hourly axis":    func(s *RawSnapshot) { s.Hourly = RawHourly{} },
		"empty daily axis":     func(s *RawSnapshot) { s.Daily = RawDaily{} },
		"short hourly temp":    func(s *RawSnapshot) { s.Hourly.Temperature2m = s.Hourly.Temperature2m[:1] },
		"short hourly wind":    func(s *RawSnapshot) { s.Hourly.WindSpeed10m = nil },
		"long daily uv":        func(s *RawSnapshot) { s.Daily.UVIndexMax = append(s.Daily.UVIndexMax, f(1)) },
		"short daily apparent": func(s *RawSnapshot) { s.Daily.ApparentTemperatureMax = nil },
	}
	for name, mutate := range cases {
		s := validSnapshot()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNullsSurviveJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"temperature2m":[30,null]`) {
		t.Errorf("null was not preserved in JSON: %s", raw)
	}
	var back RawSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Hourly.Temperature2m[1] != nil || *back.Hourly.Temperature2m[0] != 30 {
		t.Error("null did not round-trip")
	}
}
