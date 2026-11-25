package usecases

import (
	"context"
	"fmt"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Problems found in a managed check's tuple.
const (
	problemMalformed = "malformed tuple"
	problemNewer     = "newer contract"
	problemDuplicate = "duplicate key"
)

// StatusCheck is one check that carries our managed-by tag.
type StatusCheck struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Active         bool   `json:"active"`
	SourceInstance string `json:"source_instance"`
	SourceKey      string `json:"source_key"`
	Problem        string `json:"problem"` // "", problemMalformed, problemNewer or problemDuplicate
}

// StatusReport is what `status` shows.
type StatusReport struct {
	Network  string        `json:"network"`
	Snapshot string        `json:"snapshot"`
	Checks   []StatusCheck `json:"checks"`
}

// Problems counts the flagged checks.
func (r StatusReport) Problems() int {
	n := 0
	for _, c := range r.Checks {
		if c.Problem != "" {
			n++
		}
	}
	return n
}

// Status lists the managed checks of a network's snapshot (the latest when
// snapshot is empty) and flags bad tuples: a malformed tuple, a newer contract
// version, and a source key held by two or more active checks. It reads only
// Forward and needs no ThousandEyes.
func Status(ctx context.Context, fwd ports.ForwardReader, network, snapshot string) (StatusReport, error) {
	id, err := fwd.NetworkByName(ctx, network)
	if err != nil {
		return StatusReport{}, fmt.Errorf("finding Forward network %q: %w", network, err)
	}
	if snapshot == "" {
		if snapshot, err = fwd.LatestSnapshot(ctx, id); err != nil {
			return StatusReport{}, fmt.Errorf("finding the latest snapshot: %w", err)
		}
	}
	checks, err := fwd.ListChecks(ctx, snapshot)
	if err != nil {
		return StatusReport{}, fmt.Errorf("listing checks on snapshot %s: %w", snapshot, err)
	}

	rep := StatusReport{Network: network, Snapshot: snapshot, Checks: []StatusCheck{}}
	holders := map[string][]int{} // instance|key -> indexes of active checks
	for _, c := range checks {
		dec := domain.DecodeTags(c.Tags)
		sc := StatusCheck{ID: c.ID, Name: c.Name, Active: c.Active}
		switch dec.Ownership {
		case domain.NotOurs:
			continue
		case domain.MalformedTuple:
			sc.Problem = problemMalformed
		case domain.NewerContract:
			sc.Problem = problemNewer
		case domain.Ours:
			sc.SourceInstance, sc.SourceKey = dec.SourceInstance, dec.SourceKey
			if c.Active {
				k := dec.SourceInstance + "|" + dec.SourceKey
				holders[k] = append(holders[k], len(rep.Checks))
			}
		}
		rep.Checks = append(rep.Checks, sc)
	}
	for _, idx := range holders {
		if len(idx) > 1 {
			for _, i := range idx {
				rep.Checks[i].Problem = problemDuplicate
			}
		}
	}
	return rep, nil
}
