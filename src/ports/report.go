package ports

import (
	"context"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Domain types the report's reads speak in.
type (
	NetworkRecord = domain.NetworkRecord
	CheckResult   = domain.CheckResult
)

// ResultsReader reads what ThousandEyes measured. A reader offers it beside
// TeClient; report needs it and plan does not.
type ResultsReader interface {
	// NetworkResults returns every network record of a test inside the window,
	// over every page. A page that cannot be read or decoded is an error.
	NetworkResults(ctx context.Context, aid, testID string, window time.Duration) ([]NetworkRecord, error)
}

// CheckResultReader reads what Forward concluded. A reader offers it beside
// ForwardReader; report needs it and plan does not.
type CheckResultReader interface {
	// CheckResults returns the result of every check on a snapshot, by check ID.
	CheckResults(ctx context.Context, snapshotID string) (map[string]CheckResult, error)
	// SnapshotProcessedAt says when a snapshot was processed.
	SnapshotProcessedAt(ctx context.Context, networkID, snapshotID string) (time.Time, error)
}
