package gateway

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)} }

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	c := newClock()
	b := &Breaker{Threshold: 3, Cooldown: 10 * time.Second, Now: c.now}
	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatal("closed breaker refused a request")
		}
		b.Record(false)
	}
	if b.State() != StateClosed {
		t.Fatal("opened before the threshold")
	}
	b.Allow()
	b.Record(true) // a success resets the streak
	for i := 0; i < 2; i++ {
		b.Allow()
		b.Record(false)
	}
	if b.State() != StateClosed {
		t.Fatal("failures separated by a success must not add up")
	}
	b.Allow()
	b.Record(false)
	if b.State() != StateOpen || b.Allow() {
		t.Fatal("breaker should be open and refusing requests")
	}
}

func TestBreakerHalfOpenProbeClosesOrReopens(t *testing.T) {
	c := newClock()
	b := &Breaker{Threshold: 1, Cooldown: 10 * time.Second, Now: c.now}
	b.Allow()
	b.Record(false)

	c.advance(9 * time.Second)
	if b.Allow() {
		t.Fatal("allowed a request before the cooldown elapsed")
	}
	c.advance(2 * time.Second)
	if !b.Allow() {
		t.Fatal("no probe allowed after the cooldown")
	}
	if b.State() != StateHalfOpen || b.Allow() {
		t.Fatal("exactly one probe may be in flight")
	}
	b.Record(false) // probe failed
	if b.State() != StateOpen || b.Allow() {
		t.Fatal("a failed probe must reopen the breaker for a fresh cooldown")
	}

	c.advance(11 * time.Second)
	if !b.Allow() {
		t.Fatal("no second probe")
	}
	b.Record(true)
	if b.State() != StateClosed || !b.Allow() {
		t.Fatal("a successful probe must close the breaker")
	}
}

func TestBreakerDefaultsAndZeroValue(t *testing.T) {
	var b Breaker
	if b.State() != StateClosed || !b.Allow() {
		t.Fatal("the zero value must be a working closed breaker")
	}
	b.Record(true)
	for i := 0; i < 4; i++ {
		b.Allow()
		b.Record(false)
	}
	if b.State() != StateClosed {
		t.Fatal("default threshold is 5")
	}
	b.Allow()
	b.Record(false)
	if b.State() != StateOpen {
		t.Fatal("did not open at the default threshold")
	}
}

func TestBreakerIsSafeUnderConcurrency(t *testing.T) {
	b := &Breaker{Threshold: 50, Cooldown: time.Millisecond}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if b.Allow() {
					b.Record((i+j)%3 != 0)
				}
				b.State()
			}
		}(i)
	}
	wg.Wait()
}
