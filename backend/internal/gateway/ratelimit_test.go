package gateway

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterBurstThenRefill(t *testing.T) {
	c := newClock()
	l := NewLimiter(RateConfig{PerMinute: 60, Burst: 3}, c.now) // 1 token per second
	for i := 0; i < 3; i++ {
		if ok, _, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d inside the burst was refused", i+1)
		}
	}
	ok, wait, remaining := l.Allow("a")
	if ok || remaining != 0 || wait < 900*time.Millisecond || wait > 1100*time.Millisecond {
		t.Fatalf("4th request: ok=%v wait=%v remaining=%d; want refused with ~1s wait", ok, wait, remaining)
	}
	c.advance(1100 * time.Millisecond)
	if ok, _, _ := l.Allow("a"); !ok {
		t.Fatal("a token should have refilled after a second")
	}
	if ok, _, _ := l.Allow("a"); ok {
		t.Fatal("only one token had refilled")
	}
	c.advance(time.Hour)
	for i := 0; i < 3; i++ {
		if ok, _, _ := l.Allow("a"); !ok {
			t.Fatal("the bucket must refill up to the burst, no further")
		}
	}
	if ok, _, _ := l.Allow("a"); ok {
		t.Fatal("refill exceeded the burst size")
	}
}

func TestLimiterIsPerClient(t *testing.T) {
	c := newClock()
	l := NewLimiter(RateConfig{PerMinute: 60, Burst: 1}, c.now)
	if ok, _, _ := l.Allow("a"); !ok {
		t.Fatal("a refused")
	}
	if ok, _, _ := l.Allow("a"); ok {
		t.Fatal("a not limited")
	}
	if ok, _, _ := l.Allow("b"); !ok {
		t.Fatal("one client's traffic must not limit another")
	}
}

func TestLimiterDisabledAllowsEverything(t *testing.T) {
	for _, cfg := range []RateConfig{{}, {PerMinute: 0, Burst: 5}, {PerMinute: 60, Burst: 0}} {
		l := NewLimiter(cfg, nil)
		for i := 0; i < 100; i++ {
			if ok, _, _ := l.Allow("a"); !ok {
				t.Fatalf("%+v: disabled limiter refused a request", cfg)
			}
		}
	}
}

func TestLimiterForgetsIdleClientsAndBoundsMemory(t *testing.T) {
	c := newClock()
	l := NewLimiter(RateConfig{PerMinute: 60, Burst: 2}, c.now)
	l.maxKeys = 50
	for i := 0; i < 40; i++ {
		l.Allow(fmt.Sprintf("client-%d", i))
	}
	if l.Size() != 40 {
		t.Fatalf("tracking %d clients", l.Size())
	}
	c.advance(10 * time.Minute) // everyone is idle and fully refilled
	l.Allow("fresh")
	if l.Size() != 1 {
		t.Errorf("%d buckets kept after the sweep; idle clients carry no information", l.Size())
	}

	// A flood of distinct addresses (e.g. a spoofed X-Forwarded-For) must not grow memory without bound...
	for i := 0; i < 500; i++ {
		l.Allow(fmt.Sprintf("flood-%d", i))
	}
	if l.Size() > 51 {
		t.Errorf("table grew to %d despite the cap of 50", l.Size())
	}
	// ...and the overflow clients share one bucket, so the flood itself gets limited.
	refused := 0
	for i := 500; i < 600; i++ {
		if ok, _, _ := l.Allow(fmt.Sprintf("flood-%d", i)); !ok {
			refused++
		}
	}
	if refused == 0 {
		t.Error("overflow clients were never limited")
	}
}

func TestClientKeyTrustsForwardedForOnlyWhenTold(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:51234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	if got := ClientKey(r, false); got != "10.0.0.5" {
		t.Errorf("untrusted: %q; a client-supplied header must be ignored", got)
	}
	if got := ClientKey(r, true); got != "203.0.113.7" {
		t.Errorf("trusted: %q", got)
	}
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientKey(r, true); got != "10.0.0.5" {
		t.Errorf("garbage header: %q, want fallback to the socket address", got)
	}
	r.RemoteAddr = "weird"
	if got := ClientKey(r, false); got != "weird" {
		t.Errorf("unparseable remote address: %q", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "[2001:db8::1]:443"
	if got := ClientKey(r2, false); got != "2001:db8::1" {
		t.Errorf("IPv6: %q", got)
	}
}
