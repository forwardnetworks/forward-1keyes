// Package ports holds the interfaces the use cases need from the outside
// world, and re-exports the domain types they speak in, so adapters import
// this package and never the domain.
package ports

import (
	"context"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Domain types the Forward port speaks in.
type (
	CheckSpec      = domain.CheckSpec
	ObservedRecord = domain.ObservedRecord
)

// ForwardClient is what the tool needs from Forward. Every method acts on one
// pinned snapshot, never on "latest".
type ForwardClient interface {
	// LatestSnapshot returns the ID of the network's newest processed snapshot.
	LatestSnapshot(ctx context.Context, networkID string) (string, error)
	// ListChecks returns every check on the snapshot, active or not.
	ListChecks(ctx context.Context, snapshotID string) ([]ObservedRecord, error)
	// CreateCheck writes one check and returns its ID.
	CreateCheck(ctx context.Context, snapshotID string, spec CheckSpec, persistent bool) (string, error)
	// GetCheck reads one check back.
	GetCheck(ctx context.Context, snapshotID, checkID string) (ObservedRecord, error)
	// UpdateCheck rewrites one check in place.
	UpdateCheck(ctx context.Context, snapshotID, checkID string, spec CheckSpec) error
}
