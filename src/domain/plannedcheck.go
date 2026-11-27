package domain

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Source is where a flow starts: the internet edge, or one or more prefixes.
type Source struct {
	InternetEdge bool
	Prefixes     []netip.Prefix
}

// Flow is one mapped, located and resolved flow from one agent of one test.
type Flow struct {
	TestID       string
	TestName     string
	Direction    string // "to-target", "from-target"; a2a tests add "fwd" or "rev" later
	AgentID      string
	Source       Source
	DestPrefixes []netip.Prefix
	Protocol     string
	Port         *int
	// KeyByDest makes the source key carry the destination although there is
	// only one: the destination had several before some were ignored, and a
	// check that stays must keep its key.
	KeyByDest bool
}

// Contributor is one test behind a planned check, with its agents.
type Contributor struct {
	TestID   string
	TestName string
	AgentIDs []string
}

// PlannedCheck is one desired check: the merged flows that mean the same.
type PlannedCheck struct {
	SourceKey    string // "sha256:<hex>": the smallest key among the merged flows
	Slot         string
	Source       Source
	DestPrefixes []netip.Prefix
	Protocol     string
	Port         *int
	Contributors []Contributor // ordered by test ID
}

// Slot is "direction|canonical source": the literal internet-edge, or the
// sorted source prefixes joined by commas.
func Slot(direction string, src Source) string {
	if src.InternetEdge {
		return direction + "|internet-edge"
	}
	return direction + "|" + joinPrefixes(sortedPrefixes(src.Prefixes), ",")
}

// compareIDs orders IDs by length and then lexically, which is numeric order
// for decimal IDs.
func compareIDs(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func comparePrefixes(a, b netip.Prefix) int {
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c
	}
	return a.Bits() - b.Bits()
}

func sortedPrefixes(in []netip.Prefix) []netip.Prefix {
	out := slices.Clone(in)
	slices.SortFunc(out, comparePrefixes)
	return slices.CompactFunc(out, func(a, b netip.Prefix) bool { return a == b })
}

func joinPrefixes(ps []netip.Prefix, sep string) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.String()
	}
	return strings.Join(parts, sep)
}

func portString(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

// candidate is the flows of one test that share a slot, protocol, port and
// destination: they become one check.
type candidate struct {
	key      string
	slot     string
	source   Source
	dest     []netip.Prefix
	protocol string
	port     *int
	testID   string
	testName string
	agents   []string
	keyDest  bool // the key carries the destination
}

// splitFlow is a flow with one source prefix and one destination prefix. A
// location filter in Forward takes one value, so a flow with several of either
// is several flows. keyDest is set here only when the destination had several
// prefixes; separateKeys adds it later where two checks would share a key.
type splitFlow struct {
	flow    Flow
	keyDest string
}

func splitFlows(flows []Flow) []splitFlow {
	var out []splitFlow
	for _, f := range flows {
		srcs := [][]netip.Prefix{nil}
		if !f.Source.InternetEdge {
			srcs = nil
			for _, p := range sortedPrefixes(f.Source.Prefixes) {
				srcs = append(srcs, []netip.Prefix{p})
			}
		}
		dests := sortedPrefixes(f.DestPrefixes)
		for _, s := range srcs {
			for _, d := range dests {
				one := f
				one.Source = Source{InternetEdge: f.Source.InternetEdge, Prefixes: s}
				one.DestPrefixes = []netip.Prefix{d}
				sf := splitFlow{flow: one}
				if len(dests) > 1 || f.KeyByDest {
					sf.keyDest = destMember(d)
				}
				out = append(out, sf)
			}
		}
	}
	return out
}

// destMember is how a key writes a destination: a single address bare, any
// other prefix in CIDR form.
func destMember(d netip.Prefix) string {
	if d.IsSingleIP() {
		return d.Addr().String()
	}
	return d.String()
}

// meaningGroup is the per-test checks that mean the same: they become one check.
type meaningGroup struct{ members []*candidate }

// owner is the member whose key the merged check keeps: the smallest.
func (g *meaningGroup) owner() *candidate {
	owner := g.members[0]
	for _, c := range g.members[1:] {
		if c.key < owner.key {
			owner = c
		}
	}
	return owner
}

// separateKeys makes the checks' keys distinct. A key leaves the destination
// out when a flow has only one, so two checks of one test can share a key: in
// the from-target direction every flow has the same source, the target. Only
// when two merged checks would end up with one key does each per-test check
// that gave it, and does not yet carry its destination in the key, start to; a
// plan whose keys are already distinct keeps every one of them. It repeats
// because a merged check then takes its smallest key afresh. All colliding
// checks change together, so no order of the input decides who keeps a key.
func separateKeys(groups []*meaningGroup) {
	for {
		byKey := map[string][]*candidate{}
		for _, g := range groups {
			o := g.owner()
			byKey[o.key] = append(byKey[o.key], o)
		}
		changed := false
		for _, owners := range byKey {
			if len(owners) < 2 {
				continue
			}
			for _, c := range owners {
				if !c.keyDest {
					c.keyDest, changed = true, true
					c.key = SourceKey(KeyInput{TestID: c.testID, Slot: c.slot, Protocol: c.protocol, Port: c.port, Dest: destMember(c.dest[0])})
				}
			}
		}
		if !changed {
			return
		}
	}
}

// BuildPlannedChecks turns flows into desired checks. Agents of one test that
// share a slot give one check; checks of different tests with the same
// source, destination, protocol and port merge into one that keeps the
// lexically smallest source key and lists every contributing test. Where two
// merged checks would share a key, their keys carry the destination (see
// separateKeys). The result is ordered by source key.
func BuildPlannedChecks(flows []Flow) []PlannedCheck {
	// 1. one candidate per (test, slot, protocol, port, destination)
	var cands []*candidate
	index := map[string]*candidate{}
	for _, one := range splitFlows(flows) {
		f := one.flow
		slot := Slot(f.Direction, f.Source)
		dest := sortedPrefixes(f.DestPrefixes)
		id := strings.Join([]string{f.TestID, slot, f.Protocol, portString(f.Port), joinPrefixes(dest, ",")}, "\x00")
		c, ok := index[id]
		if !ok {
			c = &candidate{
				key:  SourceKey(KeyInput{TestID: f.TestID, Slot: slot, Protocol: f.Protocol, Port: f.Port, Dest: one.keyDest}),
				slot: slot, source: Source{InternetEdge: f.Source.InternetEdge, Prefixes: sortedPrefixes(f.Source.Prefixes)},
				dest: dest, protocol: f.Protocol, port: f.Port, testID: f.TestID, testName: f.TestName,
				keyDest: one.keyDest != "",
			}
			index[id] = c
			cands = append(cands, c)
		}
		if !slices.Contains(c.agents, f.AgentID) {
			c.agents = append(c.agents, f.AgentID)
		}
	}

	// 2. merge candidates that mean the same: from, to, protocol, port
	groups := map[string]*meaningGroup{}
	var order []*meaningGroup
	for _, c := range cands {
		from := "edge"
		if !c.source.InternetEdge {
			from = joinPrefixes(c.source.Prefixes, ",")
		}
		m := strings.Join([]string{from, joinPrefixes(c.dest, ","), c.protocol, portString(c.port)}, "\x00")
		g, ok := groups[m]
		if !ok {
			g = &meaningGroup{}
			groups[m] = g
			order = append(order, g)
		}
		g.members = append(g.members, c)
	}
	separateKeys(order)

	out := make([]PlannedCheck, 0, len(order))
	for _, g := range order {
		members, owner := g.members, g.owner()
		pc := PlannedCheck{
			SourceKey: owner.key, Slot: owner.slot, Source: owner.source,
			DestPrefixes: owner.dest, Protocol: owner.protocol, Port: owner.port,
		}
		byTest := map[string]*Contributor{}
		for _, c := range members {
			ct, ok := byTest[c.testID]
			if !ok {
				ct = &Contributor{TestID: c.testID, TestName: c.testName}
				byTest[c.testID] = ct
			}
			for _, a := range c.agents {
				if !slices.Contains(ct.AgentIDs, a) {
					ct.AgentIDs = append(ct.AgentIDs, a)
				}
			}
		}
		for _, ct := range byTest {
			slices.SortFunc(ct.AgentIDs, compareIDs)
			pc.Contributors = append(pc.Contributors, *ct)
		}
		slices.SortFunc(pc.Contributors, func(a, b Contributor) int { return compareIDs(a.TestID, b.TestID) })
		out = append(out, pc)
	}
	slices.SortFunc(out, func(a, b PlannedCheck) int { return strings.Compare(a.SourceKey, b.SourceKey) })
	return out
}
