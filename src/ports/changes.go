package ports

import "context"

// SnapshotLister reads which snapshots can be compared. A Forward reader offers
// it beside ForwardReader; changes needs it and plan does not.
type SnapshotLister interface {
	// ProcessedSnapshots returns the IDs of the network's processed, collected
	// snapshots, oldest first. Predictions and drafts are left out.
	ProcessedSnapshots(ctx context.Context, networkID string) ([]string, error)
}

// Notifier sends one message to the place the owner chose. A delivery that is
// not accepted is an error, and it is not retried.
type Notifier interface {
	Notify(ctx context.Context, body []byte) error
}
