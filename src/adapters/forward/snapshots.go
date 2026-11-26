package forward

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	sdkports "github.com/forwardnetworks/forward-go-sdk/ports"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

var _ ports.SnapshotLister = (*Client)(nil)

// ProcessedSnapshots lists the processed snapshots that were really collected,
// oldest first by the time they were created.
func (c *Client) ProcessedSnapshots(ctx context.Context, networkID string) ([]string, error) {
	snapshots, _, err := c.sdk.Snapshots.List(ctx, networkID, sdkports.SnapshotListOptions{})
	if err != nil {
		return nil, fmt.Errorf("forward adapter: listing the snapshots of network %s: %w", networkID, err)
	}
	type snap struct {
		id string
		at time.Time
	}
	var kept []snap
	for _, s := range snapshots {
		if !strings.EqualFold(s.State, "PROCESSED") || s.Predicted() || s.IsDraft {
			continue
		}
		at, err := time.Parse(time.RFC3339, s.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("forward adapter: snapshot %s: created time: %w", s.ID, err)
		}
		kept = append(kept, snap{string(s.ID), at})
	}
	slices.SortStableFunc(kept, func(a, b snap) int { return a.at.Compare(b.at) })
	ids := make([]string, len(kept))
	for i, s := range kept {
		ids[i] = s.id
	}
	return ids, nil
}
