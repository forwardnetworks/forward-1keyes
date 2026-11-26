package forward

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdkports "github.com/forwardnetworks/forward-go-sdk/ports"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

var _ ports.CheckResultReader = (*Client)(nil)

// CheckResults reads the status of every check on a snapshot, with the
// destination of each check as Forward holds it.
func (c *Client) CheckResults(ctx context.Context, snapshotID string) (map[string]ports.CheckResult, error) {
	checks, _, err := c.sdk.Checks.List(ctx, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("forward adapter: listing the checks of snapshot %s: %w", snapshotID, err)
	}
	out := make(map[string]ports.CheckResult, len(checks))
	for _, ck := range checks {
		destination, err := destinationOf(ck.Definition)
		if err != nil {
			return nil, fmt.Errorf("forward adapter: check %s: %w", ck.ID, err)
		}
		out[string(ck.ID)] = ports.CheckResult{Status: ck.Status, Outdated: ck.Outdated != nil && *ck.Outdated, Destination: destination,
			Name: ck.Name, Tags: ck.Tags}
	}
	return out, nil
}

// destinationOf reads filters.to.location.value; a check without one has none.
func destinationOf(definition any) (string, error) {
	raw, err := json.Marshal(definition)
	if err != nil {
		return "", fmt.Errorf("definition: %w", err)
	}
	var d struct {
		Filters struct {
			To struct {
				Location struct {
					Value any `json:"value"`
				} `json:"location"`
			} `json:"to"`
		} `json:"filters"`
	}
	// Checks of other kinds have other shapes; they simply have no destination.
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", nil
	}
	value, _ := d.Filters.To.Location.Value.(string)
	return value, nil
}

// SnapshotProcessedAt says when a snapshot was processed.
func (c *Client) SnapshotProcessedAt(ctx context.Context, networkID, snapshotID string) (time.Time, error) {
	snapshots, _, err := c.sdk.Snapshots.List(ctx, networkID, sdkports.SnapshotListOptions{})
	if err != nil {
		return time.Time{}, fmt.Errorf("forward adapter: listing the snapshots of network %s: %w", networkID, err)
	}
	for _, s := range snapshots {
		if string(s.ID) == snapshotID {
			at, err := time.Parse(time.RFC3339, s.ProcessedAt)
			if err != nil {
				return time.Time{}, fmt.Errorf("forward adapter: snapshot %s: processed time: %w", snapshotID, err)
			}
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("forward adapter: snapshot %s is not listed", snapshotID)
}
