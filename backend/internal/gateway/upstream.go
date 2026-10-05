package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

// maxUpstreamBody caps how much of an upstream response the gateway will buffer.
const maxUpstreamBody = 10 << 20

// Kinds of call failure.
const (
	KindTimeout     = "timeout"
	KindUnavailable = "unavailable"
	KindCircuitOpen = "circuit_open"
	KindTooLarge    = "too_large"
	KindCanceled    = "canceled" // the caller went away; not the upstream's fault
)

// CallError describes why a call to an upstream produced no usable response.
type CallError struct {
	Upstream string
	Kind     string
	Err      error
}

func (e *CallError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Upstream, e.Kind, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Upstream, e.Kind)
}

func (e *CallError) Unwrap() error { return e.Err }

// Result is an upstream's response.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// Upstream is one backend service behind the gateway, with its own timeout and
// circuit breaker.
type Upstream struct {
	Name    string
	BaseURL string
	Client  *http.Client // must not follow redirects
	Breaker *Breaker
	Timeout time.Duration

	ok       atomic.Int64
	failed   atomic.Int64
	rejected atomic.Int64 // refused by the open breaker
}

// Counts returns lifetime call counts: succeeded, failed, refused by the breaker.
func (u *Upstream) Counts() (ok, failed, rejected int64) {
	return u.ok.Load(), u.failed.Load(), u.rejected.Load()
}

// Do calls the upstream. A transport error, timeout or 5xx counts against the
// breaker; 4xx responses are the upstream working correctly and do not.
func (u *Upstream) Do(ctx context.Context, method, path, rawQuery string, body []byte, header http.Header) (Result, error) {
	if !u.Breaker.Allow() {
		u.rejected.Add(1)
		return Result{}, &CallError{Upstream: u.Name, Kind: KindCircuitOpen}
	}

	ctx, cancel := context.WithTimeout(ctx, u.Timeout)
	defer cancel()

	target := u.BaseURL + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		u.Breaker.Abort()
		return Result{}, &CallError{Upstream: u.Name, Kind: KindUnavailable, Err: err}
	}
	for _, h := range []string{"Content-Type", "Accept"} { // never forward credentials or cookies
		if v := header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := u.Client.Do(req)
	if err != nil {
		return Result{}, u.failure(ctx, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBody+1))
	if err != nil {
		return Result{}, u.failure(ctx, err)
	}
	if len(data) > maxUpstreamBody {
		u.failed.Add(1)
		u.Breaker.Record(false)
		return Result{}, &CallError{Upstream: u.Name, Kind: KindTooLarge}
	}

	if resp.StatusCode >= 500 {
		u.failed.Add(1)
		u.Breaker.Record(false)
	} else {
		u.ok.Add(1)
		u.Breaker.Record(true)
	}
	return Result{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// failure classifies a transport-level error and updates the breaker.
func (u *Upstream) failure(ctx context.Context, err error) error {
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		u.Breaker.Abort() // the caller left; says nothing about the upstream
		return &CallError{Upstream: u.Name, Kind: KindCanceled, Err: err}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded || (errors.As(err, &netErr) && netErr.Timeout()):
		u.failed.Add(1)
		u.Breaker.Record(false)
		return &CallError{Upstream: u.Name, Kind: KindTimeout, Err: err}
	default:
		u.failed.Add(1)
		u.Breaker.Record(false)
		return &CallError{Upstream: u.Name, Kind: KindUnavailable, Err: err}
	}
}

// newClient returns an HTTP client that forwards the request ID and refuses to
// follow redirects.
func newClient(timeout time.Duration) *http.Client {
	c := httpx.NewClient(timeout + time.Second) // the per-call context is the real limit
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}
