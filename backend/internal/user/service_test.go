package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var bg = context.Background()

func TestRegisterCreatesAccountAndSession(t *testing.T) {
	h := newHarness(t)
	u, token := h.register("  Ada@Example.ORG ")
	if u.Email != "ada@example.org" || u.Role != RoleUser || u.Disabled || u.ID == 0 {
		t.Fatalf("user = %+v", u)
	}
	p, err := h.s.Resolve(bg, token)
	if err != nil || p.UserID != u.ID || p.Role != RoleUser || p.Email != "ada@example.org" {
		t.Fatalf("Resolve = %+v, %v", p, err)
	}
	// Only a digest of the token is stored, never the token or the password.
	var stored string
	if err := h.s.DB.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || strings.Contains(stored, token) {
		t.Error("session token is stored in the clear")
	}
	var pw string
	h.s.DB.QueryRow(`SELECT password_hash FROM users`).Scan(&pw)
	if strings.Contains(pw, goodPassword) || !strings.HasPrefix(pw, "$argon2id$") {
		t.Errorf("password_hash = %q", pw)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	h.register("taken@example.org")
	cases := []struct {
		name string
		in   RegisterInput
		want error
	}{
		{"bad email", RegisterInput{Email: "nope", Password: goodPassword}, ErrValidation},
		{"weak password", RegisterInput{Email: "a@example.org", Password: "short"}, ErrValidation},
		{"password is the email", RegisterInput{Email: "a@example.org", Password: "a@example.org"}, ErrValidation},
		{"name too long", RegisterInput{Email: "a@example.org", Password: goodPassword, DisplayName: strings.Repeat("n", 61)}, ErrValidation},
		{"duplicate", RegisterInput{Email: "taken@example.org", Password: goodPassword}, ErrEmailTaken},
		{"duplicate differing in case", RegisterInput{Email: "TAKEN@Example.org", Password: goodPassword}, ErrEmailTaken},
	}
	for _, c := range cases {
		if _, _, err := h.s.Register(bg, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("%d accounts exist after rejected registrations, want 1", n)
	}
}

func TestLogin(t *testing.T) {
	h := newHarness(t)
	reg, _ := h.register("ada@example.org")
	h.advance(time.Hour)

	u, token, err := h.login("ADA@example.org", goodPassword, "1.2.3.4")
	if err != nil || u.ID != reg.ID {
		t.Fatalf("login = %+v, %v", u, err)
	}
	if !u.LastLoginAt.Equal(h.now) {
		t.Errorf("last login = %v, want %v", u.LastLoginAt, h.now)
	}
	if _, err := h.s.Resolve(bg, token); err != nil {
		t.Errorf("fresh session does not resolve: %v", err)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	h.register("ada@example.org")
	for name, c := range map[string][2]string{
		"wrong password":    {"ada@example.org", "wrong-password-xx"},
		"unknown email":     {"ghost@example.org", goodPassword},
		"malformed email":   {"not an email", goodPassword},
		"empty credentials": {"", ""},
	} {
		_, _, err := h.login(c[0], c[1], "9.9.9.9")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

func TestLoginThrottle(t *testing.T) {
	h := newHarness(t)
	h.register("ada@example.org")
	for i := 0; i < maxFailures; i++ {
		if _, _, err := h.login("ada@example.org", "wrong-password-xx", "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Locked: even the right password is refused, and the lock reports how long it lasts.
	_, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1")
	var locked *LockedError
	if !errors.As(err, &locked) || locked.RetryAfter <= 0 || locked.RetryAfter > lockDuration {
		t.Fatalf("err = %v, want a LockedError of at most %v", err, lockDuration)
	}
	// The lock is per (email, address): the real owner elsewhere is unaffected,
	// and so is the same address trying another account.
	if _, _, err := h.login("ada@example.org", goodPassword, "2.2.2.2"); err != nil {
		t.Errorf("owner on another address: %v", err)
	}
	// Case and whitespace in the email cannot dodge the lock.
	if _, _, err := h.login("  ADA@example.org ", goodPassword, "1.1.1.1"); !errors.As(err, &locked) {
		t.Errorf("differently written email bypassed the lock: %v", err)
	}
	h.advance(lockDuration + time.Second)
	if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); err != nil {
		t.Errorf("lock should expire: %v", err)
	}
}

func TestLoginFailuresOutsideTheWindowDoNotAccumulate(t *testing.T) {
	h := newHarness(t)
	h.register("ada@example.org")
	for i := 0; i < maxFailures-1; i++ {
		h.login("ada@example.org", "wrong-password-xx", "1.1.1.1")
	}
	h.advance(failureWindow + time.Minute)
	h.login("ada@example.org", "wrong-password-xx", "1.1.1.1") // would be the 5th, but the window restarted
	if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); err != nil {
		t.Errorf("stale failures locked the account: %v", err)
	}
}

func TestSuccessfulLoginClearsFailures(t *testing.T) {
	h := newHarness(t)
	h.register("ada@example.org")
	for round := 0; round < 3; round++ {
		for i := 0; i < maxFailures-1; i++ {
			h.login("ada@example.org", "wrong-password-xx", "1.1.1.1")
		}
		if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

func TestDisabledAccountCannotLogInOrUseSessions(t *testing.T) {
	h := newHarness(t)
	admin := h.admin("root@example.org")
	u, token := h.register("ada@example.org")
	if _, err := h.s.SetDisabled(bg, admin.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Resolve(bg, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("session of a disabled account still resolves: %v", err)
	}
	if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); !errors.Is(err, ErrDisabled) {
		t.Errorf("login = %v, want ErrDisabled", err)
	}
	// Disabled is only revealed to someone who knows the password.
	if _, _, err := h.login("ada@example.org", "wrong-password-xx", "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password on a disabled account = %v, want ErrInvalidCredentials", err)
	}
	if _, err := h.s.SetDisabled(bg, admin.ID, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); err != nil {
		t.Errorf("re-enabled account cannot log in: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	h := newHarness(t)
	_, token := h.register("ada@example.org")
	h.advance(h.s.SessionTTL - time.Second)
	if _, err := h.s.Resolve(bg, token); err != nil {
		t.Errorf("session expired early: %v", err)
	}
	h.advance(time.Second)
	if _, err := h.s.Resolve(bg, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("session still valid at its expiry: %v", err)
	}
	if err := h.s.PurgeExpired(bg); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Errorf("%d expired sessions left after purge", n)
	}
}

func TestResolveRejectsGarbage(t *testing.T) {
	h := newHarness(t)
	_, token := h.register("ada@example.org")
	for _, bad := range []string{"", "nonsense", token + "x", token[:len(token)-1], strings.Repeat("a", 500), hashToken(token)} {
		if _, err := h.s.Resolve(bg, bad); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("Resolve(%.20q) = %v, want ErrInvalidSession", bad, err)
		}
	}
}

func TestLogoutEndsOnlyThatSession(t *testing.T) {
	h := newHarness(t)
	_, first := h.register("ada@example.org")
	_, second, _ := h.login("ada@example.org", goodPassword, "1.1.1.1")
	if err := h.s.Logout(bg, first); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Resolve(bg, first); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("logged-out session still works: %v", err)
	}
	if _, err := h.s.Resolve(bg, second); err != nil {
		t.Errorf("other session was ended too: %v", err)
	}
	if err := h.s.Logout(bg, "never-existed"); err != nil {
		t.Errorf("logging out an unknown token should be harmless: %v", err)
	}
}

func TestSessionsPerUserAreCapped(t *testing.T) {
	h := newHarness(t) // MaxSessions = 3
	_, oldest := h.register("ada@example.org")
	other, otherToken := h.register("bob@example.org")
	var newest string
	for i := 0; i < 3; i++ {
		h.advance(time.Minute)
		_, newest, _ = h.login("ada@example.org", goodPassword, "1.1.1.1")
	}
	if n := h.count(`SELECT COUNT(*) FROM sessions WHERE user_id != ?`, other.ID); n != 3 {
		t.Errorf("%d sessions for ada, want 3", n)
	}
	if _, err := h.s.Resolve(bg, oldest); !errors.Is(err, ErrInvalidSession) {
		t.Error("the oldest session survived the cap")
	}
	if _, err := h.s.Resolve(bg, newest); err != nil {
		t.Errorf("the newest session was dropped: %v", err)
	}
	if _, err := h.s.Resolve(bg, otherToken); err != nil {
		t.Errorf("another user's session was trimmed: %v", err)
	}
}

func TestChangePasswordRevokesEverySession(t *testing.T) {
	h := newHarness(t)
	u, stolen := h.register("ada@example.org")
	_, mine, _ := h.login("ada@example.org", goodPassword, "1.1.1.1")
	_, bobToken := h.register("bob@example.org")

	const next = "a-brand-new-passphrase"
	fresh, err := h.s.ChangePassword(bg, u.ID, goodPassword, next, "ua")
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"stolen": stolen, "current": mine} {
		if _, err := h.s.Resolve(bg, tok); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("%s session survived a password change: %v", name, err)
		}
	}
	if _, err := h.s.Resolve(bg, fresh); err != nil {
		t.Errorf("the replacement session is unusable: %v", err)
	}
	if _, err := h.s.Resolve(bg, bobToken); err != nil {
		t.Errorf("another user's session was revoked: %v", err)
	}
	if _, _, err := h.login("ada@example.org", goodPassword, "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, _, err := h.login("ada@example.org", next, "1.1.1.1"); err != nil {
		t.Errorf("new password does not work: %v", err)
	}
}

func TestChangePasswordValidation(t *testing.T) {
	h := newHarness(t)
	u, token := h.register("ada@example.org")
	if _, err := h.s.ChangePassword(bg, u.ID, "wrong-password-xx", "a-brand-new-passphrase", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong current password: %v", err)
	}
	if _, err := h.s.ChangePassword(bg, u.ID, goodPassword, "short", ""); !errors.Is(err, ErrValidation) {
		t.Errorf("weak new password: %v", err)
	}
	if _, err := h.s.ChangePassword(bg, u.ID, goodPassword, goodPassword, ""); !errors.Is(err, ErrValidation) {
		t.Errorf("unchanged password: %v", err)
	}
	if _, err := h.s.ChangePassword(bg, 9999, goodPassword, "a-brand-new-passphrase", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := h.s.Resolve(bg, token); err != nil {
		t.Errorf("a rejected change must leave sessions alone: %v", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	h := newHarness(t)
	u, token := h.register("ada@example.org")
	other, _ := h.register("bob@example.org")
	for _, id := range []int64{u.ID, other.ID} {
		if err := h.s.Watch(bg, id, WatchedCity{LocationID: 5, Name: "Delhi", Latitude: 28.6, Longitude: 77.2}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.DeleteAccount(bg, u.ID, "wrong-password-xx"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if n := h.count(`SELECT COUNT(*) FROM users WHERE id = ?`, u.ID); n != 1 {
		t.Fatal("account deleted despite a wrong password")
	}
	if err := h.s.DeleteAccount(bg, u.ID, goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Resolve(bg, token); !errors.Is(err, ErrInvalidSession) {
		t.Error("deleted account's session still works")
	}
	if n := h.count(`SELECT COUNT(*) FROM watchlist WHERE user_id = ?`, u.ID); n != 0 {
		t.Errorf("%d watchlist rows outlived the account", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM watchlist WHERE user_id = ?`, other.ID); n != 1 {
		t.Error("another user's watchlist was deleted")
	}
	if err := h.s.DeleteAccount(bg, u.ID, goodPassword); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	h := newHarness(t)
	a1 := h.admin("root@example.org")
	if err := h.s.DeleteAccount(bg, a1.ID, goodPassword); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the only admin: %v", err)
	}
	a2 := h.admin("second@example.org")
	// With two admins, one may go, and then the survivor is protected.
	if err := h.s.DeleteAccount(bg, a2.ID, goodPassword); err != nil {
		t.Fatalf("deleting one of two admins: %v", err)
	}
	if err := h.s.DeleteAccount(bg, a1.ID, goodPassword); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the remaining admin: %v", err)
	}
}

func TestSetDisabledGuards(t *testing.T) {
	h := newHarness(t)
	a1 := h.admin("root@example.org")
	a2 := h.admin("second@example.org")
	u, _ := h.register("ada@example.org")

	if _, err := h.s.SetDisabled(bg, a1.ID, a1.ID, true); !errors.Is(err, ErrSelf) {
		t.Errorf("self-disable: %v", err)
	}
	if _, err := h.s.SetDisabled(bg, a1.ID, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
	got, err := h.s.SetDisabled(bg, a1.ID, a2.ID, true)
	if err != nil || !got.Disabled {
		t.Fatalf("disabling a second admin: %+v, %v", got, err)
	}
	// a2 is disabled, so a1 is now the only enabled admin; a third party cannot remove them.
	if _, err := h.s.SetDisabled(bg, u.ID, a1.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the last enabled admin: %v", err)
	}
	if err := h.s.DeleteAccount(bg, a1.ID, goodPassword); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("a disabled admin must not count as a spare: %v", err)
	}
}

func TestSeedAdminWithoutAPasswordDoesNothing(t *testing.T) {
	// The default configuration names an administrator but gives no password; that must not stop the service.
	h := newHarness(t)
	made, err := h.s.SeedAdmin(bg, "admin@heatwave.local", "")
	if err != nil || made {
		t.Errorf("seed = %v, %v; want false, nil", made, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM users`); n != 0 {
		t.Errorf("%d accounts created without a password", n)
	}
}

func TestSeedAdmin(t *testing.T) {
	h := newHarness(t)
	if _, err := h.s.SeedAdmin(bg, "bad", goodPassword); err == nil {
		t.Error("bad admin email accepted")
	}
	if _, err := h.s.SeedAdmin(bg, "root@example.org", "short"); err == nil {
		t.Error("weak admin password accepted")
	}
	made, err := h.s.SeedAdmin(bg, "Root@example.org", goodPassword)
	if err != nil || !made {
		t.Fatalf("seed = %v, %v", made, err)
	}
	u, token, err := h.login("root@example.org", goodPassword, "1.1.1.1")
	if err != nil || u.Role != RoleAdmin {
		t.Fatalf("seeded admin login = %+v, %v", u, err)
	}
	if p, _ := h.s.Resolve(bg, token); p.Role != RoleAdmin {
		t.Errorf("principal role = %q", p.Role)
	}
	// Idempotent, and it never overrides an existing administrator.
	made, err = h.s.SeedAdmin(bg, "someone-else@example.org", "another-password-1")
	if err != nil || made {
		t.Errorf("second seed = %v, %v; want false, nil", made, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("%d accounts, want 1", n)
	}
}

func TestWatchlist(t *testing.T) {
	h := newHarness(t) // MaxWatchlist = 3
	u, _ := h.register("ada@example.org")
	other, _ := h.register("bob@example.org")
	city := func(id int64, name string) WatchedCity {
		return WatchedCity{LocationID: id, Name: name, Country: "India", Latitude: 20, Longitude: 78, Timezone: "Asia/Kolkata"}
	}

	for i, name := range []string{"Delhi", "Mumbai", "Pune"} {
		h.advance(time.Minute)
		if err := h.s.Watch(bg, u.ID, city(int64(i+1), name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.Watch(bg, u.ID, city(4, "Goa")); !errors.Is(err, ErrWatchlistFull) {
		t.Errorf("a fourth city: %v, want ErrWatchlistFull", err)
	}
	// Re-adding an existing city is an update, so a full list can still refresh its entries.
	if err := h.s.Watch(bg, u.ID, city(2, "Bombay")); err != nil {
		t.Errorf("refreshing an existing city on a full list: %v", err)
	}
	list, err := h.s.Watchlist(bg, u.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].Name != "Pune" || list[1].Name != "Bombay" || list[2].Name != "Delhi" {
		t.Errorf("order/names = %v %v %v; want newest first", list[0].Name, list[1].Name, list[2].Name)
	}
	if list[1].AddedAt.Equal(h.now) {
		t.Error("refreshing a city must not reset when it was added")
	}
	if got, _ := h.s.Watchlist(bg, other.ID); len(got) != 0 {
		t.Errorf("watchlists leak between users: %+v", got)
	}

	if err := h.s.Unwatch(bg, u.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unwatch(bg, u.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
	if err := h.s.Unwatch(bg, other.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing someone else's city: %v", err)
	}
	if err := h.s.Watch(bg, u.ID, city(4, "Goa")); err != nil {
		t.Errorf("a slot freed up but adding failed: %v", err)
	}
}

func TestWatchValidation(t *testing.T) {
	h := newHarness(t)
	u, _ := h.register("ada@example.org")
	ok := WatchedCity{LocationID: 1, Name: "Delhi", Latitude: 28, Longitude: 77}
	mut := func(f func(*WatchedCity)) WatchedCity { c := ok; f(&c); return c }
	for name, c := range map[string]WatchedCity{
		"zero id":        mut(func(c *WatchedCity) { c.LocationID = 0 }),
		"negative id":    mut(func(c *WatchedCity) { c.LocationID = -3 }),
		"blank name":     mut(func(c *WatchedCity) { c.Name = "   " }),
		"long name":      mut(func(c *WatchedCity) { c.Name = strings.Repeat("n", 101) }),
		"latitude high":  mut(func(c *WatchedCity) { c.Latitude = 90.5 }),
		"latitude low":   mut(func(c *WatchedCity) { c.Latitude = -91 }),
		"longitude high": mut(func(c *WatchedCity) { c.Longitude = 181 }),
	} {
		if err := h.s.Watch(bg, u.ID, c); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v, want ErrValidation", name, err)
		}
	}
	if err := h.s.Watch(bg, u.ID, mut(func(c *WatchedCity) { c.Latitude, c.Longitude = 90, -180 })); err != nil {
		t.Errorf("boundary coordinates rejected: %v", err)
	}
}

func TestCountsAndListUsers(t *testing.T) {
	h := newHarness(t)
	h.admin("root@example.org")
	h.register("ada@example.org")
	h.register("bob@example.org")
	users, sessions, err := h.s.Counts(bg)
	if err != nil || users != 3 || sessions != 3 {
		t.Errorf("Counts = %d, %d, %v", users, sessions, err)
	}
	list, err := h.s.ListUsers(bg)
	if err != nil || len(list) != 3 || list[0].Email != "bob@example.org" {
		t.Errorf("ListUsers = %+v, %v; want newest first", list, err)
	}
	h.advance(h.s.SessionTTL + time.Hour)
	if _, sessions, _ = h.s.Counts(bg); sessions != 0 {
		t.Errorf("expired sessions are counted as live: %d", sessions)
	}
}

// Disabling an account has two independent effects, each worth its own check: its
// sessions are deleted, and Resolve refuses the account even if a session row remained.
func TestDisableDeletesSessionsAndResolveChecksTheFlag(t *testing.T) {
	h := newHarness(t)
	admin := h.admin("root@example.org")
	u, token := h.register("ada@example.org")
	if _, err := h.s.SetDisabled(bg, admin.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID); n != 0 {
		t.Errorf("%d sessions remain for a disabled account", n)
	}

	_, token = h.register("bob@example.org")
	if _, err := h.s.DB.Exec(`UPDATE users SET disabled = 1 WHERE email = 'bob@example.org'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Resolve(bg, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("Resolve trusted a session of a disabled account: %v", err)
	}
}
