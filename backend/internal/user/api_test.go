package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

type apiEnv struct {
	t *testing.T
	h *harness
	m http.Handler
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	h := newHarness(t)
	app := httpx.New("user")
	(&API{Svc: h.s}).Register(app.Mux)
	return &apiEnv{t: t, h: h, m: app.Handler()}
}

type callOpt func(*http.Request)

func asUser(u User) callOpt {
	return func(r *http.Request) {
		r.Header.Set(httpx.HeaderUserID, strconv.FormatInt(u.ID, 10))
		r.Header.Set(httpx.HeaderUserRole, u.Role)
	}
}
func bearerToken(t string) callOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+t) }
}
func fromIP(ip string) callOpt {
	return func(r *http.Request) { r.Header.Set(httpx.HeaderClientIP, ip) }
}

func (e *apiEnv) do(method, path, body string, opts ...callOpt) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestAPIRegisterLoginResolveLogout(t *testing.T) {
	e := newAPIEnv(t)
	rec, out := e.do("POST", "/register", `{"email":"Ada@Example.org","password":"`+goodPassword+`","displayName":"Ada"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %v", rec.Code, out)
	}
	user, _ := out["user"].(map[string]any)
	if user["email"] != "ada@example.org" || user["role"] != "user" || user["displayName"] != "Ada" {
		t.Errorf("user = %v", user)
	}
	for _, leak := range []string{"password", "passwordHash", "password_hash"} {
		if _, ok := user[leak]; ok {
			t.Errorf("response exposes %q", leak)
		}
	}
	if strings.Contains(rec.Body.String(), "argon2") {
		t.Error("the response contains a password hash")
	}

	rec, out = e.do("POST", "/login", `{"email":"ada@example.org","password":"`+goodPassword+`"}`, fromIP("1.1.1.1"))
	token, _ := out["token"].(string)
	if rec.Code != http.StatusOK || token == "" {
		t.Fatalf("login = %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/sessions/resolve", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusOK || out["role"] != "user" || out["email"] != "ada@example.org" || out["userId"] == nil {
		t.Fatalf("resolve = %d %v", rec.Code, out)
	}
	if rec, _ = e.do("POST", "/logout", "", bearerToken(token)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec, out = e.do("POST", "/sessions/resolve", `{"token":"`+token+`"}`); rec.Code != http.StatusUnauthorized || errCode(out) != "invalid_session" {
		t.Errorf("resolve after logout = %d %v", rec.Code, out)
	}
}

func TestAPIErrorMapping(t *testing.T) {
	e := newAPIEnv(t)
	e.h.register("ada@example.org")
	cases := []struct {
		name, method, path, body string
		opts                     []callOpt
		status                   int
		code                     string
	}{
		{"invalid JSON", "POST", "/register", `{`, nil, 400, ""},
		{"unknown field", "POST", "/register", `{"email":"a@example.org","password":"` + goodPassword + `","role":"admin"}`, nil, 400, ""},
		{"bad email", "POST", "/register", `{"email":"nope","password":"` + goodPassword + `"}`, nil, 400, "validation_failed"},
		{"weak password", "POST", "/register", `{"email":"b@example.org","password":"short"}`, nil, 400, "validation_failed"},
		{"duplicate email", "POST", "/register", `{"email":"ada@example.org","password":"` + goodPassword + `"}`, nil, 409, "email_taken"},
		{"wrong password", "POST", "/login", `{"email":"ada@example.org","password":"wrong-password-xx"}`, nil, 401, "invalid_credentials"},
		{"unknown user", "POST", "/login", `{"email":"ghost@example.org","password":"wrong-password-xx"}`, nil, 401, "invalid_credentials"},
		{"resolve garbage", "POST", "/sessions/resolve", `{"token":"zzz"}`, nil, 401, "invalid_session"},
	}
	for _, c := range cases {
		rec, out := e.do(c.method, c.path, c.body, c.opts...)
		if rec.Code != c.status || (c.code != "" && errCode(out) != c.code) {
			t.Errorf("%s: %d %v, want %d %s", c.name, rec.Code, out, c.status, c.code)
		}
	}
}

func TestAPIPrivateRoutesNeedTheTrustedHeaders(t *testing.T) {
	e := newAPIEnv(t)
	for _, r := range [][2]string{
		{"GET", "/me"}, {"POST", "/me/password"}, {"DELETE", "/me"},
		{"GET", "/watchlist"}, {"PUT", "/watchlist/1"}, {"DELETE", "/watchlist/1"},
		{"GET", "/admin/users"}, {"POST", "/admin/users/1/disable"}, {"POST", "/admin/users/1/enable"},
	} {
		if rec, out := e.do(r[0], r[1], `{}`); rec.Code != http.StatusUnauthorized || errCode(out) != "unauthenticated" {
			t.Errorf("%s %s without headers = %d %v", r[0], r[1], rec.Code, out)
		}
	}
	// A header that is not a positive integer is not an identity.
	for _, id := range []string{"abc", "0", "-4", "1;DROP", ""} {
		rec, _ := e.do("GET", "/me", "", func(r *http.Request) { r.Header.Set(httpx.HeaderUserID, id) })
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("X-User-ID %q accepted: %d", id, rec.Code)
		}
	}
}

func TestAPIAdminRoutesRequireTheAdminRole(t *testing.T) {
	e := newAPIEnv(t)
	admin := e.h.admin("root@example.org")
	ada, _ := e.h.register("ada@example.org")

	for _, r := range [][2]string{{"GET", "/admin/users"}, {"POST", "/admin/users/" + strconv.FormatInt(ada.ID, 10) + "/disable"}} {
		if rec, out := e.do(r[0], r[1], "", asUser(ada)); rec.Code != http.StatusForbidden || errCode(out) != "forbidden" {
			t.Errorf("%s %s as a user = %d %v", r[0], r[1], rec.Code, out)
		}
	}
	rec, out := e.do("GET", "/admin/users", "", asUser(admin))
	users, _ := out["users"].([]any)
	if rec.Code != http.StatusOK || len(users) != 2 {
		t.Fatalf("list = %d %v", rec.Code, out)
	}
	path := "/admin/users/" + strconv.FormatInt(ada.ID, 10)
	if rec, out = e.do("POST", path+"/disable", "", asUser(admin)); rec.Code != http.StatusOK || out["disabled"] != true {
		t.Errorf("disable = %d %v", rec.Code, out)
	}
	if rec, out = e.do("POST", path+"/enable", "", asUser(admin)); rec.Code != http.StatusOK || out["disabled"] != false {
		t.Errorf("enable = %d %v", rec.Code, out)
	}
	if rec, out = e.do("POST", "/admin/users/"+strconv.FormatInt(admin.ID, 10)+"/disable", "", asUser(admin)); rec.Code != http.StatusConflict {
		t.Errorf("self-disable = %d %v", rec.Code, out)
	}
	if rec, _ = e.do("POST", "/admin/users/9999/disable", "", asUser(admin)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown target = %d", rec.Code)
	}
	if rec, _ = e.do("POST", "/admin/users/abc/disable", "", asUser(admin)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", rec.Code)
	}
}

func TestAPILoginThrottleReports429WithRetryAfter(t *testing.T) {
	e := newAPIEnv(t)
	e.h.register("ada@example.org")
	for i := 0; i < maxFailures; i++ {
		e.do("POST", "/login", `{"email":"ada@example.org","password":"wrong-password-xx"}`, fromIP("7.7.7.7"))
	}
	rec, out := e.do("POST", "/login", `{"email":"ada@example.org","password":"`+goodPassword+`"}`, fromIP("7.7.7.7"))
	if rec.Code != http.StatusTooManyRequests || errCode(out) != "too_many_attempts" {
		t.Fatalf("locked login = %d %v", rec.Code, out)
	}
	if n, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || n <= 0 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

func TestAPIAccountManagement(t *testing.T) {
	e := newAPIEnv(t)
	ada, _ := e.h.register("ada@example.org")

	if rec, out := e.do("GET", "/me", "", asUser(ada)); rec.Code != http.StatusOK || out["email"] != "ada@example.org" {
		t.Errorf("me = %d %v", rec.Code, out)
	}
	rec, out := e.do("POST", "/me/password", `{"current":"`+goodPassword+`","new":"a-brand-new-passphrase"}`, asUser(ada))
	if rec.Code != http.StatusOK || out["token"] == nil {
		t.Fatalf("change password = %d %v", rec.Code, out)
	}
	if rec, out = e.do("POST", "/me/password", `{"current":"nope-nope-nope","new":"another-passphrase-1"}`, asUser(ada)); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong current password = %d %v", rec.Code, out)
	}
	if rec, out = e.do("DELETE", "/me", `{"password":"nope-nope-nope"}`, asUser(ada)); rec.Code != http.StatusUnauthorized {
		t.Errorf("delete with a wrong password = %d %v", rec.Code, out)
	}
	if rec, _ = e.do("DELETE", "/me", `{"password":"a-brand-new-passphrase"}`, asUser(ada)); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec, _ = e.do("GET", "/me", "", asUser(ada)); rec.Code != http.StatusNotFound {
		t.Errorf("me after delete = %d", rec.Code)
	}
}

func TestAPIWatchlist(t *testing.T) {
	e := newAPIEnv(t)
	ada, _ := e.h.register("ada@example.org")
	bob, _ := e.h.register("bob@example.org")

	put := `{"name":"Delhi","country":"India","latitude":28.6,"longitude":77.2,"timezone":"Asia/Kolkata"}`
	if rec, out := e.do("PUT", "/watchlist/5", put, asUser(ada)); rec.Code != http.StatusNoContent {
		t.Fatalf("watch = %d %v", rec.Code, out)
	}
	rec, out := e.do("GET", "/watchlist", "", asUser(ada))
	cities, _ := out["cities"].([]any)
	if rec.Code != http.StatusOK || len(cities) != 1 || cities[0].(map[string]any)["name"] != "Delhi" || cities[0].(map[string]any)["locationId"] != float64(5) {
		t.Fatalf("watchlist = %d %v", rec.Code, out)
	}
	if _, out = e.do("GET", "/watchlist", "", asUser(bob)); len(out["cities"].([]any)) != 0 {
		t.Error("another user sees the watchlist")
	}
	for name, c := range map[string]struct{ path, body string }{
		"bad id":     {"/watchlist/abc", put},
		"no name":    {"/watchlist/6", `{"latitude":1,"longitude":1}`},
		"bad coords": {"/watchlist/6", `{"name":"X","latitude":999,"longitude":1}`},
	} {
		if rec, _ := e.do("PUT", c.path, c.body, asUser(ada)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec, _ := e.do("DELETE", "/watchlist/5", "", asUser(bob)); rec.Code != http.StatusNotFound {
		t.Errorf("deleting another user's entry = %d", rec.Code)
	}
	if rec, _ := e.do("DELETE", "/watchlist/5", "", asUser(ada)); rec.Code != http.StatusNoContent {
		t.Errorf("unwatch = %d", rec.Code)
	}
}

func TestAPIWatchlistFullIsAConflict(t *testing.T) {
	e := newAPIEnv(t)
	ada, _ := e.h.register("ada@example.org")
	for i := 1; i <= 3; i++ {
		e.do("PUT", "/watchlist/"+strconv.Itoa(i), `{"name":"C","latitude":1,"longitude":1}`, asUser(ada))
	}
	rec, out := e.do("PUT", "/watchlist/4", `{"name":"C","latitude":1,"longitude":1}`, asUser(ada))
	if rec.Code != http.StatusConflict || errCode(out) != "conflict" {
		t.Errorf("= %d %v", rec.Code, out)
	}
}
