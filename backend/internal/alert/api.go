package alert

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/inbox"
)

// API exposes the service over HTTP.
type API struct {
	Svc      *Service
	Notifier *Notifier // optional; woken after each processed event
}

// Register adds the alert routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	var wake func()
	if a.Notifier != nil {
		wake = a.Notifier.Notify
	}
	mux.Handle("POST /internal/events", inbox.HandlerAfter(a.Svc.DB, a.Svc.HandleEvent, wake))

	mux.HandleFunc("GET /alerts", a.listAlerts)
	mux.HandleFunc("GET /alerts/{id}", a.getAlert)
	mux.HandleFunc("POST /alerts/{id}/ack", a.ack)

	mux.HandleFunc("GET /subscriptions", a.listSubscriptions)
	mux.HandleFunc("POST /subscriptions", a.createSubscription)
	mux.HandleFunc("DELETE /subscriptions/{id}", a.deleteSubscription)

	mux.HandleFunc("GET /my/subscriptions", a.mySubscriptions)
	mux.HandleFunc("POST /my/subscriptions", a.followLocation)
	mux.HandleFunc("DELETE /my/subscriptions/{id}", a.unfollow)
	mux.HandleFunc("GET /my/notifications", a.inbox)
	mux.HandleFunc("POST /my/notifications/read", a.readAll)
	mux.HandleFunc("POST /my/notifications/{id}/read", a.readOne)
	mux.HandleFunc("DELETE /internal/users/{id}", a.forgetUser)

	mux.HandleFunc("GET /notifications", a.listNotifications)
	mux.HandleFunc("GET /rejections", a.rejections)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, ErrTooManySubs):
		httpx.WriteError(w, r, http.StatusConflict, "subscription_limit", "the maximum number of subscriptions has been reached")
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, "validation_failed", strings.TrimPrefix(err.Error(), ErrValidation.Error()+": "))
	default:
		httpx.Logger(r.Context()).Error("request failed", "path", r.URL.Path, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// queryInt reads an optional integer query parameter within [min, max]. On bad
// input it writes the 400 itself and returns ok=false.
func queryInt(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_"+name, name+" must be between "+strconv.Itoa(min)+" and "+strconv.Itoa(max))
		return 0, false
	}
	return n, true
}

func optionalLocation(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	v := r.URL.Query().Get("locationId")
	if v == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locationId", "locationId must be a positive integer")
		return nil, false
	}
	return &id, true
}

func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all":
		status = ""
	case "open", "resolved":
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_status", "status must be open, resolved or all")
		return
	}
	loc, ok := optionalLocation(w, r)
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	alerts, err := a.Svc.ListAlerts(r.Context(), AlertFilter{Status: status, LocationID: loc, Limit: limit})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (a *API) getAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	d, err := a.Svc.GetAlert(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

func (a *API) ack(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	alert, err := a.Svc.Acknowledge(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, alert)
}

func (a *API) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	loc, ok := optionalLocation(w, r)
	if !ok {
		return
	}
	subs, err := a.Svc.ListSubscriptions(r.Context(), loc)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

type createSubscriptionRequest struct {
	LocationID *int64 `json:"locationId"`
	MinLevel   string `json:"minLevel"`
	Channel    string `json:"channel"`
	Target     string `json:"target"`
	Secret     string `json:"secret"`
	Label      string `json:"label"`
}

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req createSubscriptionRequest
	if httpx.DecodeJSON(w, r, &req) != nil {
		return
	}
	sub, err := a.Svc.CreateSubscription(r.Context(), NewSubscription{LocationID: req.LocationID, MinLevel: req.MinLevel, Channel: req.Channel, Target: req.Target, Secret: req.Secret, Label: req.Label})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, sub)
}

func (a *API) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	if err := a.Svc.DeleteSubscription(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listNotifications(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "sent", "failed", "cancelled":
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_status", "status must be pending, sent, failed or cancelled")
		return
	}
	limit, ok := queryInt(w, r, "limit", 50, 1, 500)
	if !ok {
		return
	}
	list, err := a.Svc.ListNotifications(r.Context(), status, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"notifications": list})
}

func (a *API) rejections(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryInt(w, r, "limit", 50, 1, 500)
	if !ok {
		return
	}
	list, err := a.Svc.RecentRejections(r.Context(), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rejections": list})
}

// ---- signed-in users: in-app subscriptions and the inbox ----

func (a *API) mySubscriptions(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subs, err := a.Svc.UserSubscriptions(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

// followLocation subscribes the caller to a location (or every location when locationId
// is omitted). Users only ever get the in-app channel.
func (a *API) followLocation(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req struct {
		LocationID *int64 `json:"locationId"`
		MinLevel   string `json:"minLevel"`
	}
	if httpx.DecodeJSON(w, r, &req) != nil {
		return
	}
	sub, err := a.Svc.CreateSubscription(r.Context(), NewSubscription{LocationID: req.LocationID, MinLevel: req.MinLevel, Channel: ChannelInApp, UserID: &id})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sub)
}

func (a *API) unfollow(w http.ResponseWriter, r *http.Request) {
	user, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	if err := a.Svc.DeleteUserSubscription(r.Context(), user, id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) inbox(w http.ResponseWriter, r *http.Request) {
	user, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit", 30, 1, 100)
	if !ok {
		return
	}
	box, err := a.Svc.Inbox(r.Context(), user, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, box)
}

func (a *API) readOne(w http.ResponseWriter, r *http.Request) {
	user, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	if err := a.Svc.MarkRead(r.Context(), user, id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) readAll(w http.ResponseWriter, r *http.Request) {
	user, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := a.Svc.MarkAllRead(r.Context(), user); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forgetUser removes everything a deleted account owned. Only the gateway calls it, as
// part of deleting an account; it is not routed from outside.
func (a *API) forgetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r, "id")
	if !ok {
		return
	}
	if err := a.Svc.ForgetUser(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
