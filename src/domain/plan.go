package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Plan errors.
var (
	ErrDuplicateName  = errors.New("domain: two desired rows would get the same name")
	ErrBadPlanInput   = errors.New("domain: invalid plan input")
	ErrDigestMismatch = errors.New("domain: the plan body does not match the digest")
	ErrUnappliable    = errors.New("domain: the plan is not appliable")
	ErrPlanShape      = errors.New("domain: not a plan file")
)

// CurrentContractVersion is the contract version this tool writes and reads.
const CurrentContractVersion = contractVersion

// PlanSchema is body.schema.
const PlanSchema = "forward-1keyes/plan/v1"

// TEInfo names the ThousandEyes account group a plan was made for.
type TEInfo struct {
	AccountGroupName string `json:"account_group_name"`
	AID              string `json:"aid"`
}

// ForwardInfo names the Forward network and the pinned snapshot.
type ForwardInfo struct {
	NetworkID   string `json:"network_id"`
	NetworkName string `json:"network_name"`
	SnapshotID  string `json:"snapshot_id"`
}

// Counts is the number of rows per outcome.
type Counts struct {
	Create        int `json:"create"`
	Update        int `json:"update"`
	Unchanged     int `json:"unchanged"`
	Stale         int `json:"stale"`
	Collision     int `json:"collision"`
	NotImportable int `json:"not_importable"`
}

// Resolution is the pinned answer for one hostname: its sorted addresses, or
// the error the lookup gave. It is recorded so apply never resolves again.
type Resolution struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Error     string   `json:"error"`
}

// PlanInput is what NewPlan needs.
type PlanInput struct {
	ToolVersion    string
	TE             TEInfo
	Forward        ForwardInfo
	Persistent     bool
	UpdateStrategy string // "U1" or "U3", as found against a live Forward
	MaxFlows       int    // the flow cap; must be positive
	Rows           []Row
	Observed       []ObservedRecord // every check of ours, and every check holding a desired name
	Resolver       string           // "system" or "server:<ip:port>"
	Resolutions    []Resolution
	MappingSHA256  string // "none" when no mapping file is used
	Tests          []ObservedTest
	Agents         []ObservedAgent
	// IgnoredAgentSubnets are the prefixes whose agent addresses were ignored,
	// in the form CanonicalPrefixes gives.
	IgnoredAgentSubnets []string
	// Source is set only for a plan made from a recording.
	Source *RecordingMark
}

// CanonicalPrefixes returns prefixes masked, ordered by address and then
// length, without repeats, as strings. It never returns nil.
func CanonicalPrefixes(in []netip.Prefix) []string {
	masked := make([]netip.Prefix, len(in))
	for i, p := range in {
		masked[i] = p.Masked()
	}
	out := []string{}
	for _, p := range sortedPrefixes(masked) {
		out = append(out, p.String())
	}
	return out
}

// PlanBody is what the digest covers.
type PlanBody struct {
	Schema          string           `json:"schema"`
	ToolVersion     string           `json:"tool_version"`
	ContractVersion int              `json:"contract_version"`
	TE              TEInfo           `json:"te"`
	Forward         ForwardInfo      `json:"forward"`
	Persistent      bool             `json:"persistent"`
	UpdateStrategy  string           `json:"update_strategy"`
	MaxFlows        int              `json:"max_flows"`
	Appliable       bool             `json:"appliable"`
	CapExceeded     bool             `json:"cap_exceeded"`
	Rows            []Row            `json:"rows"`
	Counts          Counts           `json:"counts"`
	Observed        []ObservedRecord `json:"observed"`
	Resolver        string           `json:"resolver"`
	Resolutions     []Resolution     `json:"resolutions"`
	MappingSHA256   string           `json:"mapping_sha256"`
	Tests           []ObservedTest   `json:"observed_tests"`
	Agents          []ObservedAgent  `json:"observed_agents"`
	// IgnoredAgentSubnets are recorded so the digest covers them.
	IgnoredAgentSubnets []string `json:"ignored_agent_subnets"`
	// Source says the plan was made from a recording, and which. A plan made
	// from live data has no such member, so its file and digest do not change.
	Source *RecordingMark `json:"source,omitempty"`
}

// NewPlan builds a plan body: it counts the rows, applies the flow cap, and
// decides whether the plan can be applied. Two create, update or unchanged
// rows with one name are an error. Rows are put in a fixed order, so the same
// rows in any order give the same body.
func NewPlan(in PlanInput) (PlanBody, error) {
	if in.UpdateStrategy != "U1" && in.UpdateStrategy != "U3" {
		return PlanBody{}, fmt.Errorf("%w: update strategy %q is neither U1 nor U3", ErrBadPlanInput, in.UpdateStrategy)
	}
	if in.MaxFlows < 1 {
		return PlanBody{}, fmt.Errorf("%w: the flow cap must be positive, got %d", ErrBadPlanInput, in.MaxFlows)
	}
	rows := slices.Clone(in.Rows)
	if rows == nil {
		rows = []Row{}
	}
	slices.SortStableFunc(rows, func(a, b Row) int {
		if (a.SourceKey == "") != (b.SourceKey == "") {
			if a.SourceKey == "" {
				return 1
			}
			return -1
		}
		if c := strings.Compare(a.SourceKey, b.SourceKey); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.CheckID, b.CheckID)
	})

	var counts Counts
	names := map[string]bool{}
	for _, r := range rows {
		switch r.Outcome {
		case OutcomeCreate, OutcomeUpdate, OutcomeUnchanged:
			if r.Name != "" {
				if names[r.Name] {
					return PlanBody{}, fmt.Errorf("%w: %q", ErrDuplicateName, r.Name)
				}
				names[r.Name] = true
			}
		}
		switch r.Outcome {
		case OutcomeCreate:
			counts.Create++
		case OutcomeUpdate:
			counts.Update++
		case OutcomeUnchanged:
			counts.Unchanged++
		case OutcomeStale:
			counts.Stale++
		case OutcomeCollision:
			counts.Collision++
		case OutcomeNotImportable:
			counts.NotImportable++
		}
	}
	observed := slices.Clone(in.Observed)
	if observed == nil {
		observed = []ObservedRecord{}
	}
	for i := range observed {
		observed[i].Tags = slices.Clone(observed[i].Tags)
		if observed[i].Tags == nil {
			observed[i].Tags = []string{}
		}
		slices.Sort(observed[i].Tags)
	}
	slices.SortFunc(observed, func(a, b ObservedRecord) int { return strings.Compare(a.ID, b.ID) })
	tests := slices.Clone(in.Tests)
	if tests == nil {
		tests = []ObservedTest{}
	}
	slices.SortFunc(tests, func(a, b ObservedTest) int { return compareIDs(a.ID, b.ID) })
	agents := slices.Clone(in.Agents)
	if agents == nil {
		agents = []ObservedAgent{}
	}
	slices.SortFunc(agents, func(a, b ObservedAgent) int { return compareIDs(a.ID, b.ID) })
	for i := range rows {
		if rows[i].TestIDs == nil {
			rows[i].TestIDs = []string{}
		}
		if rows[i].AgentIDs == nil {
			rows[i].AgentIDs = []string{}
		}
		if rows[i].Notes == nil {
			rows[i].Notes = []string{}
		}
	}
	mapping := in.MappingSHA256
	if mapping == "" {
		mapping = "none"
	}
	resolutions := slices.Clone(in.Resolutions)
	if resolutions == nil {
		resolutions = []Resolution{}
	}
	for i := range resolutions {
		if resolutions[i].Addresses == nil {
			resolutions[i].Addresses = []string{}
		}
	}
	slices.SortFunc(resolutions, func(a, b Resolution) int { return strings.Compare(a.Name, b.Name) })
	capExceeded := counts.Create+counts.Update+counts.Unchanged+counts.Stale > in.MaxFlows
	return PlanBody{
		Schema: PlanSchema, ToolVersion: in.ToolVersion, ContractVersion: contractVersion,
		TE: in.TE, Forward: in.Forward, Persistent: in.Persistent, UpdateStrategy: in.UpdateStrategy,
		MaxFlows: in.MaxFlows, Appliable: counts.Collision == 0 && !capExceeded, CapExceeded: capExceeded,
		Rows: rows, Counts: counts, Observed: observed, Resolver: in.Resolver, Resolutions: resolutions, MappingSHA256: mapping,
		Tests: tests, Agents: agents, IgnoredAgentSubnets: append([]string{}, in.IgnoredAgentSubnets...), Source: in.Source,
	}, nil
}

// ExitCode is the exit code of `plan`: 3 when a collision is present or the
// flow cap is exceeded, 2 when a create or update is pending, otherwise 0.
func (b PlanBody) ExitCode() int {
	switch {
	case b.Counts.Collision > 0 || b.CapExceeded:
		return 3
	case b.Counts.Create > 0 || b.Counts.Update > 0:
		return 2
	}
	return 0
}
