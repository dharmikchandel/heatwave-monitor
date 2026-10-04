package weather

import (
	"context"
	"fmt"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/contracts"
)

// Source produces a raw weather snapshot for a location.
type Source interface {
	// Name is recorded with each observation ("openmeteo", "simulated").
	Name() string
	// Fetch returns the current conditions plus hourly and daily series. The
	// snapshot must pass contracts.RawSnapshot.Validate. Null values are kept as
	// nil; cleaning them is the processing service's job.
	Fetch(ctx context.Context, loc contracts.Location) (contracts.RawSnapshot, error)
}

// SourceError is returned by a Source when the upstream rejects or fails a
// request, carrying the HTTP status (0 for transport errors).
type SourceError struct {
	Status int
	Msg    string
}

func (e *SourceError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("weather source: %s (HTTP %d)", e.Msg, e.Status)
	}
	return "weather source: " + e.Msg
}
