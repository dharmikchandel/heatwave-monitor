package processing

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/inbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
)

// API exposes the service over HTTP.
type API struct {
	Svc        *Service
	Dispatcher *outbox.Dispatcher // optional; woken after each processed event
}

// Register adds the processing routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	var wake func()
	if a.Dispatcher != nil {
		wake = a.Dispatcher.Notify
	}
	mux.Handle("POST /internal/events", inbox.HandlerAfter(a.Svc.DB, a.Svc.HandleEvent, wake))
	mux.HandleFunc("GET /locations/{id}/metrics", a.metrics)
	mux.HandleFunc("GET /rejections", a.rejections)
}

// metrics returns the latest processed snapshot. ?hourly=false omits the (large)
// hourly series.
func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	pw, err := a.Svc.Latest(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "no_data", "no processed weather data for this location yet")
		return
	}
	if err != nil {
		httpx.Logger(r.Context()).Error("load latest", "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if r.URL.Query().Get("hourly") == "false" {
		pw.Hourly = nil
	}
	httpx.WriteJSON(w, http.StatusOK, pw)
}

func (a *API) rejections(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	list, err := a.Svc.RecentRejections(r.Context(), limit)
	if err != nil {
		httpx.Logger(r.Context()).Error("list rejections", "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rejections": list})
}
