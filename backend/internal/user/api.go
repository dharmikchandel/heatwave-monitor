package user

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// API exposes the service over HTTP.
type API struct{ Svc *Service }

// Register adds the user routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /register", a.register)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("POST /sessions/resolve", a.resolve)

	mux.HandleFunc("GET /me", a.me)
	mux.HandleFunc("POST /me/password", a.changePassword)
	mux.HandleFunc("DELETE /me", a.deleteMe)

	mux.HandleFunc("GET /watchlist", a.watchlist)
	mux.HandleFunc("PUT /watchlist/{locationId}", a.watch)
	mux.HandleFunc("DELETE /watchlist/{locationId}", a.unwatch)

	mux.HandleFunc("GET /admin/users", a.adminUsers)
	mux.HandleFunc("POST /admin/users/{id}/disable", a.adminSetDisabled(true))
	mux.HandleFunc("POST /admin/users/{id}/enable", a.adminSetDisabled(false))
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var locked *LockedError
	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int(locked.RetryAfter.Seconds())+1))
		httpx.WriteError(w, r, http.StatusTooManyRequests, "too_many_attempts", locked.Error())
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case errors.Is(err, ErrDisabled):
		httpx.WriteError(w, r, http.StatusForbidden, "account_disabled", err.Error())
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, r, http.StatusConflict, "email_taken", err.Error())
	case errors.Is(err, ErrWatchlistFull), errors.Is(err, ErrLastAdmin), errors.Is(err, ErrSelf):
		httpx.WriteError(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, ErrInvalidSession):
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_session", err.Error())
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, "validation_failed", strings.TrimPrefix(err.Error(), ErrValidation.Error()+": "))
	default:
		httpx.Logger(r.Context()).Error("request failed", "path", r.URL.Path, "err", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// sessionResponse carries a new session. The token is for the gateway only: it turns
// it into a cookie and does not pass it on.
type sessionResponse struct {
	User      User      `json:"user"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (a *API) session(u User, token string) sessionResponse {
	return sessionResponse{User: u, Token: token, ExpiresAt: a.Svc.now().Add(a.Svc.sessionTTL()).UTC()}
}

type credentials struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	u, token, err := a.Svc.Register(r.Context(), RegisterInput{Email: in.Email, Password: in.Password, DisplayName: in.DisplayName, UserAgent: r.UserAgent()})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, a.session(u, token))
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	u, token, err := a.Svc.Login(r.Context(), LoginInput{Email: in.Email, Password: in.Password, ClientIP: r.Header.Get(httpx.HeaderClientIP), UserAgent: r.UserAgent()})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.session(u, token))
}

func bearer(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Svc.Logout(r.Context(), bearer(r)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	p, err := a.Svc.Resolve(r.Context(), in.Token)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	u, err := a.Svc.GetUser(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	token, err := a.Svc.ChangePassword(r.Context(), id, in.Current, in.New, r.UserAgent())
	if err != nil {
		fail(w, r, err)
		return
	}
	u, err := a.Svc.GetUser(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.session(u, token))
}

func (a *API) deleteMe(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	if err := a.Svc.DeleteAccount(r.Context(), id, in.Password); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) watchlist(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	list, err := a.Svc.Watchlist(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cities": list})
}

func (a *API) watch(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	locationID, ok := httpx.PathID(w, r, "locationId")
	if !ok {
		return
	}
	var in struct {
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Timezone  string  `json:"timezone"`
	}
	if httpx.DecodeJSON(w, r, &in) != nil {
		return
	}
	city := WatchedCity{LocationID: locationID, Name: in.Name, Country: in.Country, Admin1: in.Admin1, Latitude: in.Latitude, Longitude: in.Longitude, Timezone: in.Timezone}
	if err := a.Svc.Watch(r.Context(), id, city); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) unwatch(w http.ResponseWriter, r *http.Request) {
	id, _, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	locationID, ok := httpx.PathID(w, r, "locationId")
	if !ok {
		return
	}
	if err := a.Svc.Unwatch(r.Context(), id, locationID); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// admin double-checks the role the gateway reports (the gateway already enforces it).
func admin(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, role, ok := httpx.Caller(w, r)
	if !ok {
		return 0, false
	}
	if role != RoleAdmin {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "administrator access is required")
		return 0, false
	}
	return id, true
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := admin(w, r); !ok {
		return
	}
	users, err := a.Svc.ListUsers(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (a *API) adminSetDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := admin(w, r)
		if !ok {
			return
		}
		target, ok := httpx.PathID(w, r, "id")
		if !ok {
			return
		}
		u, err := a.Svc.SetDisabled(r.Context(), actor, target, disabled)
		if err != nil {
			fail(w, r, err)
			return
		}
		a.Svc.log().Info("account state changed", "actor", actor, "target", target, "disabled", disabled)
		httpx.WriteJSON(w, http.StatusOK, u)
	}
}
