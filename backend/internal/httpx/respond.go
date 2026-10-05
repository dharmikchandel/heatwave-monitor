// Package httpx holds the HTTP plumbing every service shares: JSON responses
// with a uniform error shape, request IDs, structured logs, health/metrics
// endpoints and graceful shutdown.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// RequestIDHeader carries the request ID between services.
const RequestIDHeader = "X-Request-ID"

// RequestID returns the request ID stored in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Logger returns the request-scoped logger (carrying request_id), or the
// default logger when ctx has none.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// NewLogger returns a JSON logger tagged with the service name. LOG_LEVEL
// (debug|info|warn|error) selects the level; default info.
func NewLogger(service string) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("service", service)
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

// ErrorBody is the uniform error response shape.
type ErrorBody struct {
	Error     ErrorDetail `json:"error"`
	RequestID string      `json:"request_id,omitempty"`
}

// ErrorDetail describes what went wrong. Code is a stable machine-readable slug.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError writes a uniform JSON error including the request ID.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{
		Error:     ErrorDetail{Code: code, Message: message},
		RequestID: RequestID(r.Context()),
	})
}

const maxBodyBytes = 1 << 20

// DecodeJSON reads a single JSON object from the request body into v, rejecting
// unknown fields, trailing data and bodies over 1 MiB. On failure it writes the
// 400 response itself and returns the error; callers just return.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, extra := dec.Token(); !errors.Is(extra, io.EOF) {
			err = errors.New("unexpected data after JSON body")
		}
	}
	if err != nil {
		WriteError(w, r, http.StatusBadRequest, "invalid_json", fmt.Sprintf("invalid request body: %v", err))
		return err
	}
	return nil
}

// PathID parses a positive integer path parameter (e.g. "id" in "/x/{id}"). On
// failure it writes the 400 response itself and returns ok=false.
func PathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, r, http.StatusBadRequest, "invalid_id", name+" must be a positive integer")
		return 0, false
	}
	return id, true
}
