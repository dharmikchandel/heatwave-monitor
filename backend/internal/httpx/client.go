package httpx

import (
	"net/http"
	"time"
)

// NewClient returns an HTTP client with the given timeout that forwards the
// caller's request ID (from the request context) on every outgoing call, so a
// single user request can be traced across services.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: requestIDTransport{http.DefaultTransport}}
}

type requestIDTransport struct{ next http.RoundTripper }

func (t requestIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if id := RequestID(req.Context()); id != "" && req.Header.Get(RequestIDHeader) == "" {
		req = req.Clone(req.Context())
		req.Header.Set(RequestIDHeader, id)
	}
	return t.next.RoundTrip(req)
}
