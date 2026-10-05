package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// newSession relays a call that opens a session (register, login, change password).
// The user service answers with a token; the gateway turns it into the session cookie
// and keeps it out of the body, so page scripts never see it. Command-line clients that
// ask with X-Return-Token get it in the body instead.
func (g *Gateway) newSession(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Signing in is not authenticated, so the cookie check does not cover it: without this a
		// page on another site could sign a visitor in to an account of the attacker's choosing.
		if crossSite(r) {
			g.deny(w, r, http.StatusForbidden, "cross_site_request", "this request was made from another site")
			return
		}
		body, ok := g.readBody(w, r)
		if !ok {
			return
		}
		res, err := g.user.Do(r.Context(), r.Method, path, "", body, g.outbound(r))
		if err != nil {
			g.writeCallError(w, r, err)
			return
		}
		if res.Status/100 != 2 {
			g.relay(w, res)
			return
		}
		var s struct {
			User      json.RawMessage `json:"user"`
			Token     string          `json:"token"`
			ExpiresAt time.Time       `json:"expiresAt"`
		}
		if err := json.Unmarshal(res.Body, &s); err != nil || s.Token == "" {
			g.log.Error("unreadable session answer", "request_id", httpx.RequestID(r.Context()), "path", path)
			httpx.WriteError(w, r, http.StatusBadGateway, "upstream_unavailable", "the user service gave an unreadable answer")
			return
		}
		g.sessions.clear() // a password change ends every older session
		g.setCookie(w, r, s.Token, s.ExpiresAt)
		out := map[string]any{"user": s.User}
		if r.Header.Get(returnTokenHeader) == "1" {
			out["token"], out["expiresAt"] = s.Token, s.ExpiresAt
		}
		httpx.WriteJSON(w, res.Status, out)
	}
}

// session answers "who am I?" for a page that has just loaded. Not being signed in is a
// normal answer, not an error, so anonymous visitors do not fill the console with 401s.
func (g *Gateway) session(w http.ResponseWriter, r *http.Request) {
	anonymous := func() { httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": nil}) }
	token, viaCookie := credential(r)
	if token == "" {
		anonymous()
		return
	}
	p, err := g.resolve(r.Context(), token)
	if err != nil {
		if err == errNoSession {
			if viaCookie {
				g.clearCookie(w, r)
			}
			anonymous()
			return
		}
		g.writeCallError(w, r, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
	res, err := g.user.Do(r.Context(), http.MethodGet, "/me", "", nil, g.outbound(r))
	if err != nil {
		g.writeCallError(w, r, err)
		return
	}
	if res.Status != http.StatusOK {
		anonymous() // the account vanished between the two calls
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": json.RawMessage(res.Body)})
}

// logout ends the caller's session. It always succeeds: signing out must work even when
// the session is already gone.
func (g *Gateway) logout(w http.ResponseWriter, r *http.Request) {
	if token, _ := credential(r); token != "" {
		g.sessions.forget(token)
		h := g.outbound(r)
		h.Set("Authorization", "Bearer "+token)
		if _, err := g.user.Do(r.Context(), http.MethodPost, "/logout", "", nil, h); err != nil {
			g.log.Warn("logout could not reach the user service", "request_id", httpx.RequestID(r.Context()), "err", err)
		}
	}
	g.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// deleteAccount removes the caller's account, then what other services hold for it.
// The user service checks the password first, so nothing is deleted on a wrong one.
func (g *Gateway) deleteAccount(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r)
	body, ok := g.readBody(w, r)
	if !ok {
		return
	}
	res, err := g.user.Do(r.Context(), http.MethodDelete, "/me", "", body, g.outbound(r))
	if err != nil {
		g.writeCallError(w, r, err)
		return
	}
	if res.Status/100 != 2 {
		g.relay(w, res)
		return
	}
	g.sessions.clear()
	g.clearCookie(w, r)
	// The account is gone and ids are never reused, so anything left behind is unreachable;
	// a failure here is logged for the operator rather than shown to someone already signed out.
	if _, err := g.alert.Do(r.Context(), http.MethodDelete, fmt.Sprintf("/internal/users/%d", p.UserID), "", nil, g.outbound(r)); err != nil {
		g.log.Error("could not remove a deleted account's alert data", "request_id", httpx.RequestID(r.Context()), "user_id", p.UserID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// setDisabled relays an administrator enabling or disabling an account, then drops cached
// sessions so a disabled account is locked out at once.
func (g *Gateway) setDisabled(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpx.PathID(w, r, "id")
		if !ok {
			return
		}
		status := g.forward(w, r, g.user, fmt.Sprintf("/admin/users/%d/%s", id, action))
		if status/100 == 2 {
			g.sessions.clear()
		}
	}
}
