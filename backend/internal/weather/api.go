package weather

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// API exposes the service over HTTP.
type API struct{ Svc *Service }

// Register adds the weather routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /locations", a.listLocations)
	mux.HandleFunc("POST /locations", a.addLocation)
	mux.HandleFunc("GET /locations/{id}", a.getLocation)
	mux.HandleFunc("DELETE /locations/{id}", a.removeLocation)
	mux.HandleFunc("GET /locations/{id}/raw", a.rawObservation)
	mux.HandleFunc("POST /locations/{id}/refresh", a.refresh)
	mux.HandleFunc("GET /simulation", a.getSimulation)
	mux.HandleFunc("PUT /simulation", a.putSimulation)
}

func locationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_id", "location id must be a positive integer")
		return 0, false
	}
	return id, true
}

// fail maps service errors to HTTP responses; anything unexpected is a logged 500.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		verr ValidationError
		serr *SourceError
	)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "location not found")
	case errors.Is(err, ErrTooManyLocations):
		httpx.WriteError(w, r, http.StatusConflict, "location_limit", "the maximum number of watched locations has been reached")
	case errors.As(err, &verr):
		httpx.WriteError(w, r, http.StatusBadRequest, "validation_failed", verr.Error())
	case errors.As(err, &serr):
		httpx.WriteError(w, r, http.StatusBadGateway, "source_unavailable", serr.Error())
	default:
		httpx.Logger(r.Context()).Error("request failed", "path", r.URL.Path, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (a *API) listLocations(w http.ResponseWriter, r *http.Request) {
	locs, err := a.Svc.ListLocations(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"locations": locs})
}

type addLocationRequest struct {
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	Admin1    string  `json:"admin1"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

func (a *API) addLocation(w http.ResponseWriter, r *http.Request) {
	var req addLocationRequest
	if httpx.DecodeJSON(w, r, &req) != nil {
		return
	}
	loc, created, err := a.Svc.AddLocation(r.Context(), NewLocation(req))
	if err != nil {
		fail(w, r, err)
		return
	}

	// Fetch immediately for a new (or never-fetched) location so data flows
	// without waiting for the next poll cycle.
	if created || loc.LastFetchedAt == nil {
		id := loc.ID
		a.Svc.background(r.Context(), func(ctx context.Context) {
			if _, err := a.Svc.Refresh(ctx, id); err != nil {
				a.Svc.log().Warn("initial refresh failed", "location_id", id, "err", err)
			}
		})
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, loc)
}

func (a *API) getLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := locationID(w, r)
	if !ok {
		return
	}
	loc, err := a.Svc.GetLocation(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loc)
}

func (a *API) removeLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := locationID(w, r)
	if !ok {
		return
	}
	if err := a.Svc.RemoveLocation(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) rawObservation(w http.ResponseWriter, r *http.Request) {
	id, ok := locationID(w, r)
	if !ok {
		return
	}
	obs, err := a.Svc.LatestObservation(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		if _, gerr := a.Svc.GetLocation(r.Context(), id); gerr != nil {
			fail(w, r, gerr)
			return
		}
		httpx.WriteError(w, r, http.StatusNotFound, "no_observation", "no weather data has been fetched for this location yet")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, obs)
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	id, ok := locationID(w, r)
	if !ok {
		return
	}
	obs, err := a.Svc.Refresh(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"observationId": obs.ID, "locationId": obs.LocationID, "fetchedAt": obs.FetchedAt, "source": obs.Source,
	})
}

func (a *API) getSimulation(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"enabled": a.Svc.SimulationEnabled(), "source": a.Svc.Source.Name(), "available": Scenarios}
	if a.Svc.SimulationEnabled() {
		st, err := a.Svc.Simulation(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		resp["scenario"] = st.Scenario
		resp["overrides"] = st.Overrides
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

type putSimulationRequest struct {
	Scenario   string `json:"scenario"`
	LocationID *int64 `json:"locationId"`
	Refresh    *bool  `json:"refresh"` // default true: re-fetch so the change flows through now
}

func (a *API) putSimulation(w http.ResponseWriter, r *http.Request) {
	if !a.Svc.SimulationEnabled() {
		httpx.WriteError(w, r, http.StatusConflict, "not_simulated", "the weather source is "+a.Svc.Source.Name()+"; set WEATHER_SOURCE=simulated to use scenarios")
		return
	}
	var req putSimulationRequest
	if httpx.DecodeJSON(w, r, &req) != nil {
		return
	}
	sc, err := ParseScenario(req.Scenario)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := a.Svc.SetScenario(r.Context(), req.LocationID, sc); err != nil {
		fail(w, r, err)
		return
	}

	refreshed := 0
	if req.Refresh == nil || *req.Refresh {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		if req.LocationID != nil {
			if _, err := a.Svc.Refresh(ctx, *req.LocationID); err == nil {
				refreshed = 1
			}
		} else {
			refreshed, _ = a.Svc.RefreshAll(ctx)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"scenario": sc, "locationId": req.LocationID, "refreshed": refreshed})
}
