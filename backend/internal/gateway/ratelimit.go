package gateway

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateConfig describes a token bucket: PerMinute is the sustained rate, Burst the
// bucket size (how many requests may arrive at once).
type RateConfig struct {
	PerMinute float64
	Burst     int
}

// Limiter is an in-memory token-bucket rate limiter keyed by client.
type Limiter struct {
	cfg     RateConfig
	now     func() time.Time
	maxKeys int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// overflowKey is the shared bucket used once the table is full, so a flood of
// distinct (possibly spoofed) client addresses cannot grow memory without bound.
const overflowKey = "*overflow*"

// NewLimiter returns a limiter; a non-positive rate disables it (everything is allowed).
func NewLimiter(cfg RateConfig, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{cfg: cfg, now: now, maxKeys: 10000, buckets: map[string]*bucket{}}
}

func (l *Limiter) enabled() bool { return l.cfg.PerMinute > 0 && l.cfg.Burst > 0 }

// Allow takes a token for key. When denied it returns how long until one is available.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration, remaining int) {
	if !l.enabled() {
		return true, 0, math.MaxInt32
	}
	rate := l.cfg.PerMinute / 60 // tokens per second
	burst := float64(l.cfg.Burst)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now, rate, burst)

	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= l.maxKeys {
			key = overflowKey
			b, found = l.buckets[key]
		}
		if !found {
			b = &bucket{tokens: burst, last: now}
			l.buckets[key] = b
		}
	}
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0, int(b.tokens)
	}
	wait := time.Duration(math.Ceil((1-b.tokens)/rate*1000)) * time.Millisecond
	return false, wait, 0
}

// sweep drops buckets that have been idle long enough to be full again (they
// carry no information), at most once a minute.
func (l *Limiter) sweep(now time.Time, rate, burst float64) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	refill := time.Duration(burst / rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= refill {
			delete(l.buckets, k)
		}
	}
}

// Size returns the number of tracked clients (for tests and metrics).
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// ClientKey identifies the caller for rate limiting. Behind a trusted proxy (the
// Next.js server, a load balancer) the real address is the first X-Forwarded-For
// entry; otherwise that header is attacker-controlled and ignored.
func ClientKey(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
