package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// ChangesParams are the flags of changes.
type ChangesParams struct {
	Plan  PlanParams
	Since string // the older snapshot; empty means the one before the latest
	// Stateful is set with --state; Remembered is the state file read, or nil
	// when there is none yet. Since is empty then.
	Stateful   bool
	Remembered *domain.State
}

// pinned makes the plan pipeline read the same latest snapshot the comparison
// uses.
type pinned struct {
	ports.ForwardReader
	snapshot string
}

func (p pinned) LatestSnapshot(context.Context, string) (string, error) { return p.snapshot, nil }

// InternetConnections passes the optional read through.
func (p pinned) InternetConnections(ctx context.Context, networkID string) (int, error) {
	if r, ok := p.ForwardReader.(ports.InternetNodeReader); ok {
		return r.InternetConnections(ctx, networkID)
	}
	return 0, errors.New("the internet node cannot be read")
}

// Changes compares the managed checks on two processed snapshots, names the
// tests behind each difference and counts what plan would now do. It only
// reads, and any read that fails ends it.
func Changes(ctx context.Context, deps PlanDeps, p ChangesParams) (domain.Changes, error) {
	lister, ok := deps.Forward.(ports.SnapshotLister)
	if !ok {
		return domain.Changes{}, errors.New("changes: the Forward client cannot list snapshots")
	}
	results, ok := deps.Forward.(ports.CheckResultReader)
	if !ok {
		return domain.Changes{}, errors.New("changes: the Forward client cannot read check results")
	}
	network, err := deps.Forward.NetworkByName(ctx, p.Plan.NetworkName)
	if err != nil {
		return domain.Changes{}, fmt.Errorf("finding Forward network %q: %w", p.Plan.NetworkName, err)
	}
	accountGroupID := ""
	if p.Stateful {
		groups, err := deps.TE.AccountGroups(ctx)
		if err != nil {
			return domain.Changes{}, fmt.Errorf("reading ThousandEyes account groups: %w", err)
		}
		group, err := domain.ResolveAccountGroup(groups, p.Plan.AccountGroup)
		if err != nil {
			return domain.Changes{}, err
		}
		accountGroupID = group.AID
		if p.Remembered != nil {
			if err := p.Remembered.Bind(network, accountGroupID); err != nil {
				return domain.Changes{}, err
			}
		}
	}
	snapshots, err := lister.ProcessedSnapshots(ctx, network)
	if err != nil {
		return domain.Changes{}, fmt.Errorf("listing snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		return domain.Changes{}, errors.New("the network has no processed snapshot")
	}
	latest := len(snapshots) - 1
	out := domain.Changes{Network: p.Plan.NetworkName, To: snapshots[latest], Changes: []domain.CheckChange{}, NetworkID: network, AccountGroupID: accountGroupID}
	switch {
	case p.Remembered != nil:
		from, nothingNew, err := p.Remembered.ChooseFrom(snapshots)
		if err != nil {
			return domain.Changes{}, err
		}
		if nothingNew {
			out.NothingNew = true
			return out, nil
		}
		out.From = from
	case p.Since != "":
		i := slices.Index(snapshots, p.Since)
		if i < 0 || i >= latest {
			return domain.Changes{}, fmt.Errorf("--since %s is not a processed snapshot of this network older than %s", p.Since, out.To)
		}
		out.From = p.Since
	case latest > 0:
		out.From = snapshots[latest-1]
	default:
		out.NothingToCompare = true
	}

	deps.Forward = pinned{ForwardReader: deps.Forward, snapshot: out.To}
	body, err := Plan(ctx, deps, p.Plan)
	if err != nil {
		return domain.Changes{}, err
	}
	c := body.Counts
	out.Plan = domain.PendingWork{Create: c.Create, Update: c.Update, Stale: c.Stale, Collision: c.Collision, NotImportable: c.NotImportable}
	out.NotOnline = body.NotOnline
	if out.NothingToCompare {
		return out, nil
	}

	before, err := results.CheckResults(ctx, out.From)
	if err != nil {
		return domain.Changes{}, fmt.Errorf("reading check results on snapshot %s: %w", out.From, err)
	}
	after, err := results.CheckResults(ctx, out.To)
	if err != nil {
		return domain.Changes{}, fmt.Errorf("reading check results on snapshot %s: %w", out.To, err)
	}
	tests := map[string][]string{} // check ID -> the tests plan ties to it
	for _, row := range body.Rows {
		if row.CheckID != "" && len(row.TestIDs) > 0 {
			tests[row.CheckID] = row.TestIDs
		}
	}
	out.Changes = domain.CompareSnapshots("te-aid-"+body.TE.AID, before, after)
	for i, change := range out.Changes {
		if ids, ok := tests[change.CheckID]; ok {
			out.Changes[i].TestIDs = ids
		}
	}
	return out, nil
}

// Notify sends one message about c when there is something to tell, and
// nothing otherwise.
func Notify(ctx context.Context, n ports.Notifier, c domain.Changes) error {
	if !c.Reportable() {
		return nil
	}
	body, err := json.Marshal(struct {
		Text    string               `json:"text"`
		From    string               `json:"from"`
		To      string               `json:"to"`
		Changes []domain.CheckChange `json:"changes"`
		Plan    domain.PendingWork   `json:"plan"`
		// Recording is present only for a replay, so a receiver can tell without
		// reading the text.
		Recording *domain.RecordingMark `json:"recording,omitempty"`
		// NotOnline is present only when a named agent is not online.
		NotOnline []domain.NotOnlineAgent `json:"agents_not_online,omitempty"`
	}{c.Summary(), c.From, c.To, c.Changes, c.Plan, c.Recording, c.NotOnline})
	if err != nil {
		return err
	}
	return n.Notify(ctx, body)
}
