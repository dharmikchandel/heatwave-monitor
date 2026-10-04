// Package events defines the envelope services exchange over HTTP.
//
// Delivery is at-least-once (see package outbox), so consumers must treat
// events as idempotent; Event.ID is the dedupe key (see package inbox).
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Event is the JSON envelope posted to a consumer's /internal/events endpoint.
type Event struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// NewID returns a roughly time-sortable unique ID: 12 hex chars of unix
// milliseconds followed by 16 random hex chars.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("events: crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%012x%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// Validate reports whether the envelope has the fields a consumer needs.
func (e Event) Validate() error {
	switch {
	case e.ID == "":
		return errors.New("missing id")
	case e.Type == "":
		return errors.New("missing type")
	case len(e.Payload) == 0:
		return errors.New("missing payload")
	}
	return nil
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}
