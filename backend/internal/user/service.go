// Package user implements the user service: accounts, login sessions, each user's
// watchlist of cities, and the admin operations on accounts.
//
// Sessions are opaque random tokens whose hashes are stored in the database (not JWTs),
// so they can be revoked instantly: on logout, password change, or when an account is
// disabled. The API gateway resolves a token to a user through this service (with a short
// cache) and tells the other services who is calling.
package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the service's SQL migrations for sqlitex.Open.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	return sub
}

// Roles.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// Errors the API maps to HTTP statuses.
var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrDisabled           = errors.New("this account has been disabled")
	ErrEmailTaken         = errors.New("an account with this email already exists")
	ErrNotFound           = errors.New("not found")
	ErrInvalidSession     = errors.New("invalid or expired session")
	ErrValidation         = errors.New("validation failed")
	ErrWatchlistFull      = errors.New("watchlist is full")
	ErrLastAdmin          = errors.New("the last administrator cannot be removed or disabled")
	ErrSelf               = errors.New("you cannot do that to your own account")
)

// LockedError means too many failed logins: try again after RetryAfter.
type LockedError struct{ RetryAfter time.Duration }

func (e *LockedError) Error() string {
	return fmt.Sprintf("too many failed sign-in attempts; try again in %d minutes", int(e.RetryAfter.Minutes())+1)
}

func validationError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// User is an account, as shown to its owner and to administrators. Never includes the hash.
type User struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

// Principal is who a valid session belongs to.
type Principal struct {
	UserID int64  `json:"userId"`
	Role   string `json:"role"`
	Email  string `json:"email"`
}

// Service owns all account state.
type Service struct {
	DB     *sql.DB
	Log    *slog.Logger
	Hasher *Hasher

	Clock        func() time.Time // defaults to time.Now
	SessionTTL   time.Duration    // default 7 days
	MaxSessions  int              // per user; the oldest are dropped; default 10
	MaxWatchlist int              // default 50

	dummyOnce sync.Once
	dummyHash string
	throttle  throttle
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *Service) hasher() *Hasher {
	if s.Hasher == nil {
		s.Hasher = NewHasher(4)
	}
	return s.Hasher
}

func (s *Service) sessionTTL() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return 7 * 24 * time.Hour
}

func (s *Service) maxSessions() int {
	if s.MaxSessions > 0 {
		return s.MaxSessions
	}
	return 10
}

func (s *Service) maxWatchlist() int {
	if s.MaxWatchlist > 0 {
		return s.MaxWatchlist
	}
	return 50
}

// ---- tokens ----

func newToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- login throttling ----

// throttle locks a (email, client address) pair after repeated failures. Keying on the
// pair means an attacker guessing from their own address locks themselves out, not the
// victim. State is in memory: the service is a single replica, and a restart merely
// forgives (the gateway's per-address rate limit still applies).
type throttle struct {
	mu      sync.Mutex
	entries map[string]*attempts
}

type attempts struct {
	failures    int
	firstAt     time.Time
	lockedUntil time.Time
}

const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
	lockDuration  = 15 * time.Minute
)

func (t *throttle) locked(key string, now time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a := t.entries[key]; a != nil && now.Before(a.lockedUntil) {
		return a.lockedUntil.Sub(now), true
	}
	return 0, false
}

func (t *throttle) fail(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[string]*attempts{}
	}
	if len(t.entries) > 10000 { // bound memory: drop everything that has expired
		for k, a := range t.entries {
			if now.After(a.lockedUntil) && now.Sub(a.firstAt) > failureWindow {
				delete(t.entries, k)
			}
		}
	}
	a := t.entries[key]
	if a == nil || now.Sub(a.firstAt) > failureWindow {
		a = &attempts{firstAt: now}
		t.entries[key] = a
	}
	a.failures++
	if a.failures >= maxFailures {
		a.lockedUntil = now.Add(lockDuration)
	}
}

func (t *throttle) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// ---- accounts ----

const userColumns = `id, email, display_name, role, disabled, created_at, last_login_at`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var (
		u        User
		disabled int
		created  int64
		last     sql.NullInt64
	)
	if err := r.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &disabled, &created, &last); err != nil {
		return User{}, err
	}
	u.Disabled, u.CreatedAt = disabled == 1, time.UnixMilli(created).UTC()
	if last.Valid {
		t := time.UnixMilli(last.Int64).UTC()
		u.LastLoginAt = &t
	}
	return u, nil
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getUser(ctx context.Context, q queryer, id int64) (User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetUser returns one account.
func (s *Service) GetUser(ctx context.Context, id int64) (User, error) { return getUser(ctx, s.DB, id) }

// RegisterInput creates an ordinary (non-admin) account.
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	UserAgent   string
}

// Register creates an account and signs it in, returning the new session token.
func (s *Service) Register(ctx context.Context, in RegisterInput) (User, string, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return User{}, "", validationError("%v", err)
	}
	name := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(name) > 60 {
		return User{}, "", validationError("name must be at most 60 characters")
	}
	if err := CheckPassword(in.Password, email); err != nil {
		return User{}, "", fmt.Errorf("%w: %s", ErrValidation, err)
	}

	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, email).Scan(&exists); err != nil {
		return User{}, "", err
	}
	if exists > 0 {
		return User{}, "", ErrEmailTaken
	}
	hash, err := s.hasher().Hash(ctx, in.Password)
	if err != nil {
		return User{}, "", err
	}
	return s.createAccount(ctx, email, name, hash, RoleUser, in.UserAgent)
}

func (s *Service) createAccount(ctx context.Context, email, name, hash, role, userAgent string) (User, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (email, display_name, password_hash, role, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?)`,
		email, name, hash, role, s.now().UnixMilli(), s.now().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", ErrEmailTaken // lost a race with a concurrent registration
		}
		return User{}, "", fmt.Errorf("create account: %w", err)
	}
	id, _ := res.LastInsertId()
	token, err := s.newSession(ctx, tx, id, userAgent)
	if err != nil {
		return User{}, "", err
	}
	u, err := getUser(ctx, tx, id)
	if err != nil {
		return User{}, "", err
	}
	return u, token, tx.Commit()
}

func (s *Service) newSession(ctx context.Context, tx queryer, userID int64, userAgent string) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, expires_at, user_agent) VALUES (?, ?, ?, ?, ?)`,
		hash, userID, now.UnixMilli(), now.Add(s.sessionTTL()).UnixMilli(), userAgent); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	// Keep only the newest sessions: forgotten browsers eventually fall off.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token_hash NOT IN
		   (SELECT token_hash FROM sessions WHERE user_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		userID, userID, s.maxSessions()); err != nil {
		return "", fmt.Errorf("trim sessions: %w", err)
	}
	return token, nil
}

// LoginInput is a sign-in attempt. ClientIP (supplied by the gateway) scopes the throttle.
type LoginInput struct {
	Email     string
	Password  string
	ClientIP  string
	UserAgent string
}

// Login checks credentials and opens a session. Every failure looks the same to the
// caller (and takes about as long), so the API does not reveal which emails have accounts.
func (s *Service) Login(ctx context.Context, in LoginInput) (User, string, error) {
	key := strings.ToLower(strings.TrimSpace(in.Email)) + "|" + in.ClientIP
	if retry, locked := s.throttle.locked(key, s.now()); locked {
		return User{}, "", &LockedError{RetryAfter: retry}
	}

	email, emailErr := NormalizeEmail(in.Email)
	var (
		id       int64
		hash     string
		disabled int
	)
	found := false
	if emailErr == nil {
		err := s.DB.QueryRowContext(ctx, `SELECT id, password_hash, disabled FROM users WHERE email = ?`, email).Scan(&id, &hash, &disabled)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, sql.ErrNoRows):
			return User{}, "", err
		}
	}
	if !found {
		hash = s.dummy(ctx) // spend the same effort as a real check
	}
	ok, err := s.hasher().Verify(ctx, in.Password, hash)
	if err != nil {
		return User{}, "", err
	}
	if !found || !ok {
		s.throttle.fail(key, s.now())
		return User{}, "", ErrInvalidCredentials
	}
	if disabled == 1 {
		return User{}, "", ErrDisabled // only revealed to someone who knew the password
	}

	s.throttle.reset(key)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, s.now().UnixMilli(), id); err != nil {
		return User{}, "", err
	}
	token, err := s.newSession(ctx, tx, id, in.UserAgent)
	if err != nil {
		return User{}, "", err
	}
	u, err := getUser(ctx, tx, id)
	if err != nil {
		return User{}, "", err
	}
	return u, token, tx.Commit()
}

func (s *Service) dummy(ctx context.Context) string {
	s.dummyOnce.Do(func() {
		s.dummyHash, _ = s.hasher().Hash(context.Background(), "not-a-real-password-"+time.Now().String())
	})
	return s.dummyHash
}

// Resolve maps a session token to its user. It writes nothing (it is on every request's path).
func (s *Service) Resolve(ctx context.Context, token string) (Principal, error) {
	if token == "" || len(token) > 200 {
		return Principal{}, ErrInvalidSession
	}
	var (
		p        Principal
		expires  int64
		disabled int
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT u.id, u.role, u.email, s.expires_at, u.disabled FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`,
		hashToken(token)).Scan(&p.UserID, &p.Role, &p.Email, &expires, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrInvalidSession
	}
	if err != nil {
		return Principal{}, err
	}
	if disabled == 1 || !s.now().Before(time.UnixMilli(expires)) {
		return Principal{}, ErrInvalidSession
	}
	return p, nil
}

// Logout ends one session. Unknown tokens are not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// ChangePassword verifies the current password, sets the new one, ends every session
// (including any stolen one) and returns a fresh session for the caller.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, userAgent string) (string, error) {
	var email, hash string
	if err := s.DB.QueryRowContext(ctx, `SELECT email, password_hash FROM users WHERE id = ?`, userID).Scan(&email, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	ok, err := s.hasher().Verify(ctx, current, hash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	if err := CheckPassword(next, email); err != nil {
		return "", fmt.Errorf("%w: %s", ErrValidation, err)
	}
	if next == current {
		return "", validationError("the new password must differ from the current one")
	}
	newHash, err := s.hasher().Hash(ctx, next)
	if err != nil {
		return "", err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return "", err
	}
	token, err := s.newSession(ctx, tx, userID, userAgent)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// DeleteAccount removes an account and everything of its own (sessions, watchlist) after
// confirming the password. The last administrator cannot be deleted.
func (s *Service) DeleteAccount(ctx context.Context, userID int64, password string) error {
	var role, hash string
	if err := s.DB.QueryRowContext(ctx, `SELECT role, password_hash FROM users WHERE id = ?`, userID).Scan(&role, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	ok, err := s.hasher().Verify(ctx, password, hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if role == RoleAdmin {
		if n, err := s.enabledAdmins(ctx); err != nil {
			return err
		} else if n <= 1 {
			return ErrLastAdmin
		}
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	return err
}

func (s *Service) enabledAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	return n, err
}

// SeedAdmin creates the first administrator from configuration, only when no admin exists.
// It reports whether it created one. An empty password means "do not create one" (the
// default configuration), not an error: people can still register, and nobody is an admin.
func (s *Service) SeedAdmin(ctx context.Context, email, password string) (bool, error) {
	if password == "" {
		return false, nil
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	norm, err := NormalizeEmail(email)
	if err != nil {
		return false, fmt.Errorf("ADMIN_EMAIL: %w", err)
	}
	if err := CheckPassword(password, norm); err != nil {
		return false, fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	hash, err := s.hasher().Hash(ctx, password)
	if err != nil {
		return false, err
	}
	if _, _, err := s.createAccount(ctx, norm, "Administrator", hash, RoleAdmin, "seed"); err != nil {
		return false, err
	}
	return true, nil
}

// ---- administration ----

// ListUsers returns every account, newest first (capped).
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetDisabled enables or disables an account (disabling also ends its sessions). Admins
// cannot disable themselves, and the last enabled admin cannot be disabled.
func (s *Service) SetDisabled(ctx context.Context, actorID, targetID int64, disabled bool) (User, error) {
	if actorID == targetID {
		return User{}, ErrSelf
	}
	target, err := s.GetUser(ctx, targetID)
	if err != nil {
		return User{}, err
	}
	if disabled && target.Role == RoleAdmin {
		if n, err := s.enabledAdmins(ctx); err != nil {
			return User{}, err
		} else if n <= 1 && !target.Disabled {
			return User{}, ErrLastAdmin
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	flag := 0
	if disabled {
		flag = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, flag, targetID); err != nil {
		return User{}, err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, targetID); err != nil {
			return User{}, err
		}
	}
	u, err := getUser(ctx, tx, targetID)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

// ---- watchlist ----

// WatchedCity is a city a user follows.
type WatchedCity struct {
	LocationID int64     `json:"locationId"`
	Name       string    `json:"name"`
	Country    string    `json:"country"`
	Admin1     string    `json:"admin1,omitempty"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	Timezone   string    `json:"timezone"`
	AddedAt    time.Time `json:"addedAt"`
}

// Watchlist returns a user's cities, newest first.
func (s *Service) Watchlist(ctx context.Context, userID int64) ([]WatchedCity, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT location_id, name, country, admin1, latitude, longitude, timezone, added_at FROM watchlist WHERE user_id = ? ORDER BY added_at DESC, location_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WatchedCity{}
	for rows.Next() {
		var (
			c     WatchedCity
			added int64
		)
		if err := rows.Scan(&c.LocationID, &c.Name, &c.Country, &c.Admin1, &c.Latitude, &c.Longitude, &c.Timezone, &added); err != nil {
			return nil, err
		}
		c.AddedAt = time.UnixMilli(added).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Watch adds (or refreshes) a city on a user's watchlist.
func (s *Service) Watch(ctx context.Context, userID int64, c WatchedCity) error {
	c.Name = strings.TrimSpace(c.Name)
	switch {
	case c.LocationID <= 0:
		return validationError("locationId must be a positive integer")
	case c.Name == "" || utf8.RuneCountInString(c.Name) > 100:
		return validationError("name is required and must be at most 100 characters")
	case c.Latitude < -90 || c.Latitude > 90 || c.Longitude < -180 || c.Longitude > 180:
		return validationError("latitude/longitude out of range")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var already, total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watchlist WHERE user_id = ? AND location_id = ?`, userID, c.LocationID).Scan(&already); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watchlist WHERE user_id = ?`, userID).Scan(&total); err != nil {
		return err
	}
	if already == 0 && total >= s.maxWatchlist() {
		return ErrWatchlistFull
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO watchlist (user_id, location_id, name, country, admin1, latitude, longitude, timezone, added_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, location_id) DO UPDATE SET name = excluded.name, country = excluded.country, admin1 = excluded.admin1,
		   latitude = excluded.latitude, longitude = excluded.longitude, timezone = excluded.timezone`,
		userID, c.LocationID, c.Name, strings.TrimSpace(c.Country), strings.TrimSpace(c.Admin1), c.Latitude, c.Longitude, strings.TrimSpace(c.Timezone), s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// Unwatch removes a city from a user's watchlist.
func (s *Service) Unwatch(ctx context.Context, userID, locationID int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM watchlist WHERE user_id = ? AND location_id = ?`, userID, locationID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- housekeeping ----

// PurgeExpired deletes sessions past their expiry.
func (s *Service) PurgeExpired(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, s.now().UnixMilli())
	return err
}

// Run purges expired sessions hourly until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
				s.log().Error("purge sessions failed", "err", err)
			}
		}
	}
}

// Counts returns how many accounts and live sessions exist (for /metrics).
func (s *Service) Counts(ctx context.Context) (users, sessions int, err error) {
	if err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE expires_at > ?`, s.now().UnixMilli()).Scan(&sessions)
	return
}
