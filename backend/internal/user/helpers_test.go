package user

import (
	"context"
	"testing"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/sqlitex"
)

func init() {
	// Cheap hashing keeps the suite fast. Production uses the OWASP minimums in password.go.
	argonMemoryKiB, argonTime = 8, 1
}

var epoch = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

const goodPassword = "correct-horse-battery"

type harness struct {
	t   *testing.T
	s   *Service
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, now: epoch}
	h.s = &Service{DB: db, Clock: func() time.Time { return h.now }, Hasher: NewHasher(4), SessionTTL: 24 * time.Hour, MaxSessions: 3, MaxWatchlist: 3}
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

// register creates an ordinary account and returns it with its session token.
func (h *harness) register(email string) (User, string) {
	h.t.Helper()
	u, token, err := h.s.Register(context.Background(), RegisterInput{Email: email, Password: goodPassword, DisplayName: "Test"})
	if err != nil {
		h.t.Fatalf("register %s: %v", email, err)
	}
	return u, token
}

// admin creates an administrator through the same path the service uses at startup.
func (h *harness) admin(email string) User {
	h.t.Helper()
	hash, err := h.s.hasher().Hash(context.Background(), goodPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	u, _, err := h.s.createAccount(context.Background(), email, "Admin", hash, RoleAdmin, "test")
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) login(email, password, ip string) (User, string, error) {
	return h.s.Login(context.Background(), LoginInput{Email: email, Password: password, ClientIP: ip})
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}
