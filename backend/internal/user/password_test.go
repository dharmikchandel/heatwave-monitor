package user

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPasswordPolicy(t *testing.T) {
	good := []string{"correct-horse-battery", "a long passphrase with spaces", "Tr0ub4dor&3xyz", strings.Repeat("ab", 60)}
	for _, p := range good {
		if err := CheckPassword(p, "me@example.org"); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	bad := map[string]string{
		"too short":       "short",
		"nine characters": "123456789",
		"too long":        strings.Repeat("x1", 70),
		"common":          "Password123",
		"common (case)":   "QWERTYUIOP",
		"is the email":    "me@example.org",
		"one repeated":    "aaaaaaaaaaaa",
	}
	for name, p := range bad {
		var weak ErrWeakPassword
		err := CheckPassword(p, "me@example.org")
		if err == nil {
			t.Errorf("%s: %q accepted", name, p)
			continue
		}
		if e, ok := err.(ErrWeakPassword); !ok || e.Reason == "" {
			t.Errorf("%s: error %T %v, want a ErrWeakPassword with a reason", name, err, err)
		}
		_ = weak
	}
	// Length is counted in characters, not bytes.
	if err := CheckPassword(strings.Repeat("é", 10), ""); err != nil {
		t.Errorf("ten two-byte characters should count as ten: %v", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{"Me@Example.ORG": "me@example.org", "  a@b.co ": "a@b.co", "first.last+tag@sub.example.com": "first.last+tag@sub.example.com"} {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "plain", "@example.org", "a@", "a@b", "Name <a@b.co>", "a b@c.co", strings.Repeat("x", 250) + "@b.co", "a@b.co, c@d.co"} {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted as %q", in, got)
		}
	}
}

func TestHashAndVerify(t *testing.T) {
	h, ctx := NewHasher(2), context.Background()
	a, err := h.Hash(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Hash(ctx, goodPassword)
	if a == b {
		t.Error("two hashes of the same password were identical: the salt is not random")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=8,t=1,p=1$") || strings.Contains(a, goodPassword) {
		t.Errorf("hash = %q", a)
	}
	for _, c := range []struct {
		password string
		want     bool
	}{{goodPassword, true}, {goodPassword + "x", false}, {"", false}, {strings.ToUpper(goodPassword), false}} {
		if ok, err := h.Verify(ctx, c.password, a); err != nil || ok != c.want {
			t.Errorf("Verify(%q) = %v, %v; want %v", c.password, ok, err, c.want)
		}
	}
}

func TestVerifyNeverMatchesMalformedOrHostileHashes(t *testing.T) {
	h, ctx := NewHasher(1), context.Background()
	good, _ := h.Hash(ctx, goodPassword)
	parts := strings.Split(good, "$")
	cases := map[string]string{
		"empty":           "",
		"not a hash":      "plaintext",
		"wrong algorithm": strings.Replace(good, "argon2id", "argon2i", 1),
		"wrong version":   strings.Replace(good, "v=19", "v=16", 1),
		"bad params":      strings.Replace(good, "m=8,t=1,p=1", "m=x,t=1,p=1", 1),
		"bad salt":        "$" + strings.Join([]string{parts[1], parts[2], parts[3], "!!!", parts[5]}, "$"),
		"empty key":       "$" + strings.Join([]string{parts[1], parts[2], parts[3], parts[4], ""}, "$"),
		"too few fields":  "$argon2id$v=19$m=8,t=1,p=1$c2FsdA",
		"huge memory":     strings.Replace(good, "m=8,", "m=4194304,", 1),
		"huge iterations": strings.Replace(good, "t=1,", "t=4000000,", 1),
		"zero threads":    strings.Replace(good, "p=1", "p=0", 1),
	}
	for name, enc := range cases {
		start := time.Now()
		ok, err := h.Verify(ctx, goodPassword, enc)
		if ok || err != nil {
			t.Errorf("%s: Verify = %v, %v; want false, nil", name, ok, err)
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s: took %v; a tampered hash must not make the server do heavy work", name, time.Since(start))
		}
	}
}

func TestHashingConcurrencyIsCapped(t *testing.T) {
	h := NewHasher(1)
	if err := h.acquire(context.Background()); err != nil { // occupy the only slot
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, goodPassword); err == nil {
		t.Fatal("hashed although every slot was taken; memory use is not bounded")
	}
	if _, err := h.Verify(ctx, goodPassword, "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHRzYWx0$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"); err == nil {
		t.Fatal("verified although every slot was taken")
	}
	h.release()
	if _, err := h.Hash(context.Background(), goodPassword); err != nil {
		t.Errorf("hashing should work again once a slot frees up: %v", err)
	}
	if NewHasher(0).slots == nil || cap(NewHasher(-3).slots) != 1 {
		t.Error("concurrency below 1 must be raised to 1")
	}
}
