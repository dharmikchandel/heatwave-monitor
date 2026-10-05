package user

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: OWASP's recommended minimum profile (19 MiB, 2 passes, 1 lane).
// Memory-hard hashing makes stolen hashes expensive to crack; the cost is ~19 MiB and a
// few tens of milliseconds per login, so concurrent hashing is capped (see Hasher).
//
// These are variables only so tests can lower them for speed; Verify always uses the
// parameters stored inside each hash, so existing hashes keep working if they change.
var (
	argonMemoryKiB uint32 = 19 * 1024
	argonTime      uint32 = 2
)

const (
	argonThreads uint8 = 1
	argonKeyLen        = 32
	saltLen            = 16
)

// Password rules. Length matters most; the cap stops absurd inputs being hashed.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

// ErrWeakPassword explains why a password was refused.
type ErrWeakPassword struct{ Reason string }

func (e ErrWeakPassword) Error() string { return e.Reason }

// commonPasswords is a small denylist of the passwords attackers try first. It is a
// backstop for the length rule, not a substitute for a breached-password check.
var commonPasswords = map[string]bool{
	"password": true, "password1": true, "password12": true, "password123": true, "passw0rd123": true, "1234567890": true,
	"12345678910": true, "qwertyuiop": true, "qwerty12345": true, "1q2w3e4r5t": true, "iloveyou123": true, "letmein1234": true,
	"admin12345": true, "administrator": true, "welcome123": true, "abc1234567": true, "0123456789": true, "1111111111": true,
}

// CheckPassword applies the password policy. email may be empty.
func CheckPassword(password, email string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLen:
		return ErrWeakPassword{fmt.Sprintf("password must be at least %d characters", MinPasswordLen)}
	case n > MaxPasswordLen:
		return ErrWeakPassword{fmt.Sprintf("password must be at most %d characters", MaxPasswordLen)}
	case commonPasswords[strings.ToLower(password)]:
		return ErrWeakPassword{"that password is too common; choose another"}
	case email != "" && strings.EqualFold(password, email):
		return ErrWeakPassword{"password must not be your email address"}
	case strings.Count(password, password[:1]) == len(password):
		return ErrWeakPassword{"password must not be a single repeated character"}
	}
	return nil
}

// NormalizeEmail validates an address and lower-cases it, so "A@x.org" and "a@x.org" are one account.
func NormalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 254 {
		return "", errors.New("a valid email address is required")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw { // reject "Name <a@b>" forms: only a bare address
		return "", errors.New("a valid email address is required")
	}
	local, domain, ok := strings.Cut(addr.Address, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") {
		return "", errors.New("a valid email address is required")
	}
	return strings.ToLower(addr.Address), nil
}

// Hasher hashes and verifies passwords, allowing only a few hashes at once so a burst
// of logins cannot exhaust the service's memory (each hash needs ~19 MiB).
type Hasher struct{ slots chan struct{} }

// NewHasher allows up to concurrency simultaneous hash operations (minimum 1).
func NewHasher(concurrency int) *Hasher {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Hasher{slots: make(chan struct{}, concurrency)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns a PHC-format argon2id hash with a fresh random salt.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password matches an encoded hash, in constant time. A malformed
// hash never matches.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	var (
		version, memory, iterations uint32
		threads                     uint8
		saltB64, keyB64             string
	)
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, nil
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, nil
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, nil
	}
	saltB64, keyB64 = parts[4], parts[5]
	salt, err1 := base64.RawStdEncoding.DecodeString(saltB64)
	want, err2 := base64.RawStdEncoding.DecodeString(keyB64)
	// Refuse parameters far above ours: a tampered hash must not make Verify consume gigabytes.
	if err1 != nil || err2 != nil || len(want) == 0 || memory > 256*1024 || iterations > 10 || threads == 0 {
		return false, nil
	}

	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
