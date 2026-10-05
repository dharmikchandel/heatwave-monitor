package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewIDUniqueAndSortable(t *testing.T) {
	seen := map[string]bool{}
	var prev string
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 28 || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
		if prefix := id[:12]; prev != "" && prefix < prev {
			t.Fatalf("timestamp prefix went backwards: %s < %s", prefix, prev)
		}
		prev = id[:12]
	}
}

func TestValidate(t *testing.T) {
	ok := Event{ID: "1", Type: "t", Payload: json.RawMessage(`{}`)}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid event rejected: %v", err)
	}
	for field, ev := range map[string]Event{
		"id":      {Type: "t", Payload: json.RawMessage(`{}`)},
		"type":    {ID: "1", Payload: json.RawMessage(`{}`)},
		"payload": {ID: "1", Type: "t"},
	} {
		if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("missing %s: err = %v", field, err)
		}
	}
}

func TestDecode(t *testing.T) {
	ev := Event{Type: "t", Payload: json.RawMessage(`{"n":3}`)}
	var v struct{ N int }
	if err := ev.Decode(&v); err != nil || v.N != 3 {
		t.Errorf("Decode = %+v, %v", v, err)
	}
	if err := (Event{Type: "t", Payload: json.RawMessage(`[`)}).Decode(&v); err == nil {
		t.Error("expected decode error")
	}
}
