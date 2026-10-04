// Package gateway implements the API gateway: the single public entry point. It
// proxies requests to the owning service, composes a location's climate picture
// from several services in parallel (degrading gracefully when one is down), and
// protects the system with timeouts, circuit breakers, rate limits and an admin gate.
package gateway

import (
	"sync"
	"time"
)

// Circuit breaker states.
const (
	StateClosed   = "closed"
	StateOpen     = "open"
	StateHalfOpen = "half-open"
)

// Breaker stops calling a failing upstream so requests fail fast instead of
// piling up behind timeouts. After Threshold consecutive failures it opens for
// Cooldown; then it lets a single probe through (half-open): success closes it,
// failure reopens it.
type Breaker struct {
	Threshold int
	Cooldown  time.Duration
	Now       func() time.Time // defaults to time.Now

	mu       sync.Mutex
	state    string
	failures int
	openedAt time.Time
	probing  bool
}

func (b *Breaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Breaker) threshold() int {
	if b.Threshold <= 0 {
		return 5
	}
	return b.Threshold
}

func (b *Breaker) cooldown() time.Duration {
	if b.Cooldown <= 0 {
		return 10 * time.Second
	}
	return b.Cooldown
}

// Allow reports whether a request may go to the upstream. Every allowed request
// must be followed by exactly one Record call.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case "", StateClosed:
		return true
	case StateOpen:
		if b.now().Sub(b.openedAt) < b.cooldown() {
			return false
		}
		b.state, b.probing = StateHalfOpen, true
		return true // this request is the probe
	default: // half-open: one probe at a time
		return false
	}
}

// Record reports the outcome of an allowed request.
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.state == StateHalfOpen:
		b.probing = false
		if success {
			b.state, b.failures = StateClosed, 0
		} else {
			b.state, b.openedAt = StateOpen, b.now()
		}
	case success:
		b.failures = 0
	default:
		b.failures++
		if b.failures >= b.threshold() {
			b.state, b.openedAt = StateOpen, b.now()
		}
	}
}

// Abort releases an allowed request that ended without a verdict on the
// upstream's health (the client went away). A half-open probe goes back to open,
// with its cooldown already elapsed, so the next request probes again.
func (b *Breaker) Abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == StateHalfOpen {
		b.state, b.probing = StateOpen, false
	}
}

// State returns "closed", "open" or "half-open" (an open breaker whose cooldown
// has elapsed still reads "open" until the next request probes it).
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == "" {
		return StateClosed
	}
	return b.state
}
