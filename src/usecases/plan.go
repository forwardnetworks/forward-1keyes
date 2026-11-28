package usecases

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// PlanDeps are the read-only ports plan uses.
type PlanDeps struct {
	TE       ports.TeClient
	Forward  ports.ForwardReader
	Resolver ports.Resolver
}

// PlanParams are the flags of plan.
type PlanParams struct {
	AccountGroup    string
	NetworkName     string
	Selection       domain.Selection
	MaxFlows        int
	Persistent      bool
	UpdateStrategy  string // "U1" or "U3", as found against a live Forward
	ToolVersion     string
	ResolverSetting string // "system" or "server:<ip:port>", recorded in the plan
	// IgnoreAgentSubnets: an agent address inside one of these is not used to
	// locate the agent. Test destinations are never filtered.
	IgnoreAgentSubnets []netip.Prefix
	// Source is set when the ThousandEyes data is a recording: the plan then
	// carries it inside what its digest covers.
	Source *domain.RecordingMark
}

const nameLimit = 255

// Plan reads ThousandEyes and Forward and builds a plan. It never writes: the
// ports it is given have no write method.
func Plan(ctx context.Context, deps PlanDeps, p PlanParams) (domain.PlanBody, error) {
	body, _, err := PlanWithSources(ctx, deps, p)
	return body, err
}

// PlanWithSources is Plan that also says which agents give one test more than
// one source, judged on the flows the plan was built from. It reads no more
// than Plan does; SourceWarning does the looking.
func PlanWithSources(ctx context.Context, deps PlanDeps, p PlanParams) (domain.PlanBody, domain.SourceReport, error) {
	var found domain.SourceReport
	body, flows, agents, used, err := buildPlan(ctx, deps, p)
	if err != nil {
		return domain.PlanBody{}, found, err
	}
	found = domain.SourceReport{Agents: domain.MultiSourceAgents(flows, agents), Used: map[string][]netip.Addr{},
		ReturnTo: domain.ReturnDestinations(flows), Recording: p.Source != nil, NotOnline: domain.SelectedIDs(body.NotOnline)}
	for id := range used {
		found.Used[id] = agents[id].Addresses
	}
	return body, found, nil
}

// buildPlan builds the plan, and returns with it the located flows, the agents
// with the ignored addresses removed, and the IDs of the agents at either end
// of a flow.
func buildPlan(ctx context.Context, deps PlanDeps, p PlanParams) (domain.PlanBody, []domain.Flow, map[string]domain.AgentInfo, map[string]bool, error) {
	fail := func(err error) (domain.PlanBody, []domain.Flow, map[string]domain.AgentInfo, map[string]bool, error) {
		return domain.PlanBody{}, nil, nil, nil, err
	}
	groups, err := deps.TE.AccountGroups(ctx)
	if err != nil {
		return fail(fmt.Errorf("reading ThousandEyes account groups: %w", err))
	}
	group, err := domain.ResolveAccountGroup(groups, p.AccountGroup)
	if err != nil {
		return fail(err)
	}

	configs, err := readTests(ctx, deps.TE, group.AID)
	if err != nil {
		return fail(err)
	}
	selected, err := domain.Select(configs, p.Selection)
	if err != nil {
		return fail(err)
	}
	agentList, err := deps.TE.ListAgents(ctx, group.AID)
	if err != nil {
		return fail(fmt.Errorf("reading ThousandEyes agents: %w", err))
	}
	agents := map[string]domain.AgentInfo{}
	for _, a := range agentList {
		agents[a.ID] = a
	}

	notOnline := domain.NotOnlineAgents(configs, selected, agents)

	// Locating uses the filtered agents; the plan fingerprints the agents as
	// ThousandEyes reports them, so apply compares like with like.
	reachable, emptied := ignoreAgentAddresses(agents, p.IgnoreAgentSubnets)

	pins, resolutions := resolveHostnames(ctx, deps.Resolver, selected)

	networkID, err := deps.Forward.NetworkByName(ctx, p.NetworkName)
	if err != nil {
		return fail(fmt.Errorf("finding Forward network %q: %w", p.NetworkName, err))
	}
	snapshot, err := deps.Forward.LatestSnapshot(ctx, networkID)
	if err != nil {
		return fail(fmt.Errorf("finding the latest snapshot: %w", err))
	}
	inv, err := deps.Forward.Inventory(ctx, snapshot)
	if err != nil {
		return fail(fmt.Errorf("reading the inventory of snapshot %s: %w", snapshot, err))
	}
	checks, err := deps.Forward.ListChecks(ctx, snapshot)
	if err != nil {
		return fail(fmt.Errorf("listing checks on snapshot %s: %w", snapshot, err))
	}

	var rows []domain.Row
	var flows []domain.Flow
	targets := map[string]bool{}
	for _, t := range selected {
		if !t.Enabled {
			rows = append(rows, notImportableRow(t.Name, "disabled", "the test is disabled in ThousandEyes", t.ID))
			continue
		}
		if t.Type == "agent-to-agent" {
			fs, why := agentToAgentFlows(inv, agents, reachable, emptied, t)
			if why != "" {
				rows = append(rows, notImportableRow(t.Name, "agent-to-agent", why, t.ID))
				continue
			}
			flows = append(flows, fs...)
			if len(fs) > 0 {
				targets[t.Target] = true
			}
			continue
		}
		res := domain.MapFlows(t, pins)
		if res.NotImportable != nil {
			rows = append(rows, notImportableRow(t.Name, res.NotImportable.Kind, res.NotImportable.Reason, t.ID))
			continue
		}
		for _, c := range res.Candidates {
			f, why := locateFlow(inv, reachable, emptied, t, c)
			if why != "" {
				rows = append(rows, notImportableRow(t.Name, "not located", why, t.ID))
				continue
			}
			flows = append(flows, f)
		}
	}

	instance := "te-aid-" + group.AID
	var desired []domain.DesiredRow
	specs := map[string]domain.CheckSpec{}
	behind := map[string]contributors{} // source key -> the tests and agents behind it
	usedAgents := map[string]bool{}
	for _, pc := range domain.BuildPlannedChecks(flows) {
		spec, err := domain.BuildCheck(pc, group.AID, nameLimit)
		if err != nil {
			var behindIt []string
			for _, ct := range pc.Contributors {
				behindIt = append(behindIt, ct.TestID)
			}
			rows = append(rows, notImportableRow(pc.Contributors[0].TestName, "cannot build the check", buildFailure(err, internetReason(ctx, deps.Forward, networkID)), behindIt...))
			continue
		}
		specs[pc.SourceKey] = spec
		c := contributors{}
		for _, ct := range pc.Contributors {
			c.tests = append(c.tests, ct.TestID)
			for _, a := range ct.AgentIDs {
				c.agents = append(c.agents, a)
				usedAgents[a] = true
			}
		}
		behind[pc.SourceKey] = c
		desired = append(desired, domain.DesiredRow{SourceKey: pc.SourceKey, Name: spec.Name, ContentHash: spec.ContentHash()})
	}

	byID := map[string]domain.ObservedRecord{}
	observed := make([]domain.ObservedCheck, 0, len(checks))
	for _, c := range checks {
		byID[c.ID] = c
		observed = append(observed, domain.ObservedCheck{ID: c.ID, Name: c.Name, Tags: c.Tags, Active: c.Active, ContentHash: c.ContentHash()})
	}
	diff := domain.Diff(domain.DiffOptions{SourceInstance: instance}, desired, observed)
	for _, r := range diff.Rows {
		if c, ok := behind[r.SourceKey]; ok {
			r.TestIDs, r.AgentIDs = uniqueSorted(c.tests), uniqueSorted(c.agents)
		}
		if spec, ok := specs[r.SourceKey]; ok && (r.Outcome == domain.OutcomeCreate || r.Outcome == domain.OutcomeUpdate) {
			s := spec
			r.Spec = &s
			if r.Outcome == domain.OutcomeUpdate {
				r.Reason = "changed: " + strings.Join(changedFields(byID[r.CheckID], spec), ", ")
			}
		}
		rows = append(rows, r)
	}

	linkReplacements(rows, byID)
	domain.WarnRows(rows, notOnline)

	setting := p.ResolverSetting
	if setting == "" {
		setting = "system"
	}
	ends := targets
	for _, f := range flows {
		ends[f.AgentID] = true
	}
	body, err := domain.NewPlan(domain.PlanInput{
		ToolVersion: p.ToolVersion, TE: domain.TEInfo{AccountGroupName: group.Name, AID: group.AID},
		Forward:    domain.ForwardInfo{NetworkID: networkID, NetworkName: p.NetworkName, SnapshotID: snapshot},
		Persistent: p.Persistent, UpdateStrategy: p.UpdateStrategy, MaxFlows: p.MaxFlows, Rows: rows,
		Observed: observedForPlan(checks, instance, desired), Resolver: setting, Resolutions: resolutions,
		Tests: observedTests(selected), Agents: observedAgents(agents, usedAgents),
		IgnoredAgentSubnets: domain.CanonicalPrefixes(p.IgnoreAgentSubnets), Source: p.Source, NotOnline: notOnline,
	})
	return body, flows, reachable, ends, err
}

// readTests reads the test list, and the detail of every test of a supported
// type; other types are kept as summaries so they can be reported.
func readTests(ctx context.Context, te ports.TeClient, aid string) ([]domain.TestConfig, error) {
	summaries, err := te.ListTests(ctx, aid)
	if err != nil {
		return nil, fmt.Errorf("listing ThousandEyes tests: %w", err)
	}
	var out []domain.TestConfig
	for _, s := range summaries {
		if s.Type != "agent-to-server" && s.Type != "http-server" && s.Type != "agent-to-agent" {
			out = append(out, domain.TestConfig{ID: s.ID, Name: s.Name, Type: s.Type, Enabled: s.Enabled})
			continue
		}
		cfg, err := te.GetTest(ctx, aid, s)
		if err != nil {
			return nil, fmt.Errorf("reading ThousandEyes test %s: %w", s.ID, err)
		}
		out = append(out, cfg)
	}
	return out, nil
}

// resolveHostnames looks each distinct hostname up once, in sorted order, and
// pins the answer (sorted, deduplicated) or the error.
func resolveHostnames(ctx context.Context, r ports.Resolver, tests []domain.TestConfig) (map[string][]netip.Addr, []domain.Resolution) {
	names := map[string]bool{}
	for _, t := range tests {
		if !t.Enabled {
			continue
		}
		if host, ok := domain.TargetHost(t); ok {
			if _, err := netip.ParseAddr(host); err != nil {
				names[host] = true
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	pins := map[string][]netip.Addr{}
	var resolutions []domain.Resolution
	for _, name := range sorted {
		addrs, err := r.Lookup(ctx, name)
		res := domain.Resolution{Name: name, Addresses: []string{}}
		if err != nil {
			res.Error = err.Error()
		} else {
			slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
			addrs = slices.Compact(addrs)
			pins[name] = addrs
			for _, a := range addrs {
				res.Addresses = append(res.Addresses, a.String())
			}
		}
		resolutions = append(resolutions, res)
	}
	return pins, resolutions
}

// locateFlow places one candidate in the Forward model: where it starts, and
// the destination addresses it ends at (one check each). A reason comes back when it cannot.
func locateFlow(inv domain.Inventory, agents map[string]domain.AgentInfo, emptied map[string]bool, t domain.TestConfig, c domain.Candidate) (domain.Flow, string) {
	src, why := agentSource(inv, agents, emptied, c.AgentID)
	if why != "" {
		return domain.Flow{}, why
	}
	var dests []netip.Prefix
	for _, a := range c.DestAddrs {
		if domain.Locate(inv, []netip.Addr{a}).Status != domain.Unmapped {
			dests = append(dests, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	if len(dests) == 0 {
		return domain.Flow{}, "the destination is outside the modelled network"
	}
	return domain.Flow{TestID: t.ID, TestName: t.Name, Direction: "to-target", AgentID: c.AgentID,
		Source: src, DestPrefixes: dests, Protocol: c.Protocol, Port: c.Port}, ""
}

// agentSource says where an agent starts a flow: the internet edge for a cloud
// agent, or the one location its addresses give in the model. A reason comes
// back when it has none or more than one.
func agentSource(inv domain.Inventory, agents map[string]domain.AgentInfo, emptied map[string]bool, id string) (domain.Source, string) {
	agent, ok := agents[id]
	if !ok {
		return domain.Source{}, fmt.Sprintf("agent %s is not in the account group", id)
	}
	if agent.Kind == domain.AgentCloud {
		return domain.Source{InternetEdge: true}, ""
	}
	if !agent.Online() {
		return domain.Source{}, agent.UnusableReason()
	}
	if emptied[id] {
		return domain.Source{}, allIgnoredReason(id)
	}
	loc := domain.Locate(inv, agent.Addresses)
	switch loc.Status {
	case domain.Located:
		return domain.Source{Prefixes: sourcePrefixes(loc.Location, agent.Addresses)}, ""
	case domain.Ambiguous:
		// Several prefixes are several checks. Subnets that share one prefix in
		// different VRFs cannot be told apart by a prefix filter.
		var prefixes []netip.Prefix
		for _, c := range loc.Candidates {
			if c.Kind != domain.SubnetLocation {
				return domain.Source{}, fmt.Sprintf("agent %s maps to more than one location", id)
			}
			if !slices.Contains(prefixes, c.Prefix) {
				prefixes = append(prefixes, c.Prefix)
			}
		}
		if len(prefixes) < 2 {
			return domain.Source{}, fmt.Sprintf("agent %s maps to more than one location", id)
		}
		return domain.Source{Prefixes: prefixes}, ""
	}
	return domain.Source{}, fmt.Sprintf("agent %s is not in the modelled network", id)
}

// agentToAgentFlows maps an agent-to-agent test. The target agent is located
// like a source; a target that maps to no location, or to more than one, makes
// the whole test not importable. Direction decides which end is the source.
func agentToAgentFlows(inv domain.Inventory, reported, agents map[string]domain.AgentInfo, emptied map[string]bool, t domain.TestConfig) ([]domain.Flow, string) {
	if (t.Protocol == "tcp" || t.Protocol == "udp") && t.Port == nil {
		return nil, t.Protocol + " test has no port"
	}
	target, ok := agents[t.Target]
	if !ok {
		return nil, fmt.Sprintf("target agent %s is not in the account group", t.Target)
	}
	if target.Kind == domain.AgentCloud {
		return nil, fmt.Sprintf("target agent %s is a cloud agent: it has no location in the model", t.Target)
	}
	if !target.Online() {
		return nil, "target " + target.UnusableReason()
	}
	if emptied[t.Target] {
		return nil, "target " + allIgnoredReason(t.Target)
	}
	loc := domain.Locate(inv, target.Addresses)
	switch loc.Status {
	case domain.Ambiguous:
		return nil, fmt.Sprintf("target agent %s maps to more than one location", t.Target)
	case domain.Located:
	default:
		return nil, fmt.Sprintf("target agent %s is not in the modelled network", t.Target)
	}
	targetPrefixes := sourcePrefixes(loc.Location, target.Addresses)

	dir := "to-target"
	if t.Direction != nil && *t.Direction != "" {
		dir = *t.Direction
	}
	if dir != "to-target" && dir != "from-target" && dir != "bidirectional" {
		return nil, fmt.Sprintf("unknown direction %q", dir)
	}
	var flows []domain.Flow
	for _, id := range t.AgentIDs {
		src, why := agentSource(inv, agents, emptied, id)
		if why != "" {
			return nil, why
		}
		if dir == "to-target" || dir == "bidirectional" {
			flows = append(flows, domain.Flow{TestID: t.ID, TestName: t.Name, Direction: "to-target", AgentID: id,
				Source: src, DestPrefixes: targetPrefixes, Protocol: t.Protocol, Port: t.Port})
		}
		if dir == "from-target" || dir == "bidirectional" {
			if src.InternetEdge {
				return nil, fmt.Sprintf("agent %s is a cloud agent: it has no location in the model to send to", id)
			}
			full, fullWhy := agentSource(inv, reported, nil, id)
			flows = append(flows, domain.Flow{TestID: t.ID, TestName: t.Name, Direction: "from-target", AgentID: id,
				Source: domain.Source{Prefixes: targetPrefixes}, DestPrefixes: src.Prefixes, Protocol: t.Protocol, Port: t.Port,
				KeyByDest: fullWhy == "" && len(full.Prefixes) > 1})
		}
	}
	return flows, ""
}

// ignoreAgentAddresses returns the agents with every address inside an ignored
// subnet removed, and the IDs of the agents that had addresses and kept none.
func ignoreAgentAddresses(agents map[string]domain.AgentInfo, ignored []netip.Prefix) (map[string]domain.AgentInfo, map[string]bool) {
	out, emptied := map[string]domain.AgentInfo{}, map[string]bool{}
	for id, a := range agents {
		var keep []netip.Addr
		for _, addr := range a.Addresses {
			if !slices.ContainsFunc(ignored, func(p netip.Prefix) bool { return p.Masked().Contains(addr) }) {
				keep = append(keep, addr)
			}
		}
		if len(a.Addresses) > 0 && len(keep) == 0 {
			emptied[id] = true
		}
		a.Addresses = keep
		out[id] = a
	}
	return out, emptied
}

func allIgnoredReason(agent string) string {
	return fmt.Sprintf("agent %s has all its addresses ignored (--ignore-agent-subnet)", agent)
}

func sourcePrefixes(loc domain.Location, addrs []netip.Addr) []netip.Prefix {
	if loc.Kind == domain.SubnetLocation {
		return []netip.Prefix{loc.Prefix}
	}
	var out []netip.Prefix
	for _, a := range addrs {
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out
}

// internetReason says why a flow from the internet cannot be a check: when the
// Forward reader can say that the network's internet node has no connection,
// that is the reason; otherwise the check form is not known yet.
func internetReason(ctx context.Context, fwd ports.ForwardReader, networkID string) string {
	const unknown = "no internet-edge source form is known yet"
	r, ok := fwd.(ports.InternetNodeReader)
	if !ok {
		return unknown
	}
	n, err := r.InternetConnections(ctx, networkID)
	if err != nil || n > 0 {
		return unknown
	}
	return "the network's internet node has no connection to the model"
}

func buildFailure(err error, internet string) string {
	if err == domain.ErrNoInternetEdge {
		return internet
	}
	return err.Error()
}

func notImportableRow(name, kind, reason string, testIDs ...string) domain.Row {
	return domain.Row{Outcome: domain.OutcomeNotImportable, Name: name, Reason: kind + ": " + reason, UnimportableTestIDs: testIDs}
}

// changedFields names what differs between the check Forward holds and the
// check the plan would write.
func changedFields(have domain.ObservedRecord, want domain.CheckSpec) []string {
	var out []string
	if have.Name != want.Name {
		out = append(out, "name")
	}
	if have.Note != want.Note {
		out = append(out, "note")
	}
	if !sameTags(have.Tags, want.Tags) {
		out = append(out, "tags")
	}
	if have.DefinitionHash != want.DefinitionHash() {
		out = append(out, "definition")
	}
	return out
}

func sameTags(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// observedForPlan keeps every check of ours (active or not) and every check
// that holds a desired name: the checks apply must see unchanged.
func observedForPlan(checks []domain.ObservedRecord, instance string, desired []domain.DesiredRow) []domain.ObservedRecord {
	names := map[string]bool{}
	for _, d := range desired {
		names[d.Name] = true
	}
	var out []domain.ObservedRecord
	for _, c := range checks {
		dec := domain.DecodeTags(c.Tags)
		if names[c.Name] || (dec.Ownership == domain.Ours && dec.SourceInstance == instance) {
			out = append(out, c)
		}
	}
	return out
}

// contributors are the tests and agents behind one planned check.
type contributors struct{ tests, agents []string }

func uniqueSorted(in []string) []string {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(out)
}

// observedTests records every selected test with its fingerprint, so apply can
// say later whether it changed.
func observedTests(selected []domain.TestConfig) []domain.ObservedTest {
	out := make([]domain.ObservedTest, 0, len(selected))
	for _, t := range selected {
		out = append(out, domain.ObservedTest{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled, Fingerprint: domain.TestFingerprint(t)})
	}
	return out
}

// observedAgents records the agents behind the planned checks, with fingerprints.
func observedAgents(agents map[string]domain.AgentInfo, used map[string]bool) []domain.ObservedAgent {
	out := make([]domain.ObservedAgent, 0, len(used))
	for id := range used {
		if a, ok := agents[id]; ok {
			out = append(out, domain.ObservedAgent{ID: id, Fingerprint: domain.AgentFingerprint(a)})
		}
	}
	return out
}

// linkReplacements links each create row to the stale check that means the same
// flow: when the smallest key of a merged row changes, the old check goes stale
// and a new one is created, which must read as a pair and never as an update.
// Two checks mean the same flow when their definitions are the same, because the
// definition is built from the flow alone.
func linkReplacements(rows []domain.Row, held map[string]domain.ObservedRecord) {
	creates := map[string][]int{}
	for i, r := range rows {
		if r.Outcome == domain.OutcomeCreate && r.Spec != nil {
			h := r.Spec.DefinitionHash()
			creates[h] = append(creates[h], i)
		}
	}
	for i, r := range rows {
		if r.Outcome != domain.OutcomeStale {
			continue
		}
		for _, j := range creates[held[r.CheckID].DefinitionHash] {
			rows[j].Notes = append(rows[j].Notes, fmt.Sprintf("replaces stale %s (dedup key changed)", r.CheckID))
			rows[i].Notes = append(rows[i].Notes, fmt.Sprintf("superseded by %s", rows[j].SourceKey))
		}
	}
}
