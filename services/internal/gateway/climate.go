package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
)

// Source states reported in Climate.Sources.
const (
	SourceOK          = "ok"
	SourceNoData      = "no_data"     // the service answered: nothing computed yet
	SourceUnavailable = "unavailable" // down, erroring or circuit open
	SourceTimeout     = "timeout"
	SourceError       = "error" // answered with something unexpected
)

// Climate is the composed picture of one location.
type Climate struct {
	Location   json.RawMessage `json:"location"`
	Weather    json.RawMessage `json:"weather"`    // cleaned current/hourly/daily data (processing)
	Prediction json.RawMessage `json:"prediction"` // heatwave probabilities (prediction)
	Risk       json.RawMessage `json:"risk"`       // risk assessment (risk)
	Alerts     json.RawMessage `json:"alerts"`     // open alerts for the location (alert)
	// Status is "ok" (everything present), "partial" (something missing) or
	// "warming" (the location is new and the pipeline has produced nothing yet).
	Status string `json:"status"`
	// Degraded is true when a service is failing — as opposed to merely not having data yet.
	Degraded    bool              `json:"degraded"`
	Sources     map[string]string `json:"sources"`
	GeneratedAt time.Time         `json:"generatedAt"`
}

type part struct {
	state string
	body  []byte
}

// fetchPart calls one upstream and classifies the outcome.
func (g *Gateway) fetchPart(ctx context.Context, up *Upstream, path, rawQuery string) part {
	res, err := up.Do(ctx, http.MethodGet, path, rawQuery, nil, nil)
	if err != nil {
		if ce, ok := err.(*CallError); ok && ce.Kind == KindTimeout {
			return part{state: SourceTimeout}
		}
		return part{state: SourceUnavailable}
	}
	switch {
	case res.Status == http.StatusOK:
		return part{state: SourceOK, body: res.Body}
	case res.Status == http.StatusNotFound:
		return part{state: SourceNoData}
	case res.Status >= 500:
		return part{state: SourceUnavailable}
	default:
		return part{state: SourceError}
	}
}

// field decodes body as a JSON object and returns one of its fields. ok is false
// when body is not an object or lacks the field, which marks the source "error".
func field(body []byte, name string) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return nil, false
	}
	v, ok := obj[name]
	return v, ok && string(v) != "null"
}

// climateHandler composes weather, prediction, risk and alerts for one location,
// calling the services in parallel. A service that is down or slow costs only its
// own part of the answer: everything else is still returned, with per-source states.
func (g *Gateway) climateHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), g.cfg.ClimateTimeout)
	defer cancel()

	metricsQuery := ""
	if r.URL.Query().Get("hourly") == "false" {
		metricsQuery = "hourly=false"
	}

	var (
		wg                         sync.WaitGroup
		loc, wx, pred, rsk, alerts part
	)
	call := func(dst *part, up *Upstream, path, query string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			*dst = g.fetchPart(ctx, up, path, query)
		}()
	}
	call(&loc, g.weather, fmt.Sprintf("/locations/%d", id), "")
	call(&wx, g.processing, fmt.Sprintf("/locations/%d/metrics", id), metricsQuery)
	call(&pred, g.prediction, fmt.Sprintf("/locations/%d/prediction", id), "")
	call(&rsk, g.risk, fmt.Sprintf("/locations/%d/risk", id), "")
	call(&alerts, g.alert, "/alerts", fmt.Sprintf("locationId=%d&status=open&limit=20", id))
	wg.Wait()

	if r.Context().Err() != nil {
		return // the client gave up
	}
	if loc.state == SourceNoData { // the registry says this location does not exist
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "location not found")
		return
	}

	c := Climate{
		Sources: map[string]string{
			"weather": loc.state, "processing": wx.state, "prediction": pred.state, "risk": rsk.state, "alerts": alerts.state,
		},
		GeneratedAt: time.Now().UTC(),
	}

	// Each part is validated as it is attached; a malformed answer demotes that source to "error".
	attach := func(dst *json.RawMessage, p *part, src string, extract func([]byte) (json.RawMessage, bool)) {
		if p.state != SourceOK {
			return
		}
		v, ok := extract(p.body)
		if !ok {
			c.Sources[src] = SourceError
			return
		}
		*dst = v
	}
	whole := func(b []byte) (json.RawMessage, bool) { return b, json.Valid(b) }
	named := func(name string) func([]byte) (json.RawMessage, bool) {
		return func(b []byte) (json.RawMessage, bool) { return field(b, name) }
	}
	attach(&c.Location, &loc, "weather", whole)
	attach(&c.Weather, &wx, "processing", whole)
	attach(&c.Prediction, &pred, "prediction", named("prediction"))
	attach(&c.Risk, &rsk, "risk", named("assessment"))
	attach(&c.Alerts, &alerts, "alerts", named("alerts"))

	// If the registry could not name the location, the other services carry it too.
	if c.Location == nil {
		if wx.state == SourceOK {
			c.Location, _ = field(wx.body, "location")
		}
		if c.Location == nil && rsk.state == SourceOK {
			c.Location, _ = field(rsk.body, "location")
		}
	}

	failing, pending, usable := 0, 0, 0
	for name, state := range c.Sources {
		switch state {
		case SourceUnavailable, SourceTimeout, SourceError:
			failing++
		case SourceNoData:
			if name == "processing" || name == "prediction" || name == "risk" {
				pending++
			}
		case SourceOK:
			usable++
		}
	}
	switch {
	case usable == 0:
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "the backend services are not reachable")
		g.climate[1].Add(1)
		return
	case failing > 0:
		c.Status, c.Degraded = "partial", true
		g.climate[1].Add(1)
	case pending == 3:
		c.Status = "warming"
		g.climate[2].Add(1)
	case pending > 0:
		c.Status = "partial"
		g.climate[1].Add(1)
	default:
		c.Status = "ok"
		g.climate[0].Add(1)
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}
