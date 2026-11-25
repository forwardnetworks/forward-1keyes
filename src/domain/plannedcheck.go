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
}

// BuildPlannedChecks turns flows into desired checks. Agents of one test that
// share a slot give one check; checks of different tests with the same
// source, destination, protocol and port merge into one that keeps the
// lexically smallest source key and lists every contributing test. The result
// is ordered by source key.
func BuildPlannedChecks(flows []Flow) []PlannedCheck {
	// 1. one candidate per (test, slot, protocol, port, destination)
	var cands []*candidate
	index := map[string]*candidate{}
	for _, f := range flows {
		slot := Slot(f.Direction, f.Source)
		dest := sortedPrefixes(f.DestPrefixes)
		id := strings.Join([]string{f.TestID, slot, f.Protocol, portString(f.Port), joinPrefixes(dest, ",")}, "\x00")
		c, ok := index[id]
		if !ok {
			c = &candidate{
				key:  SourceKey(KeyInput{TestID: f.TestID, Slot: slot, Protocol: f.Protocol, Port: f.Port}),
				slot: slot, source: Source{InternetEdge: f.Source.InternetEdge, Prefixes: sortedPrefixes(f.Source.Prefixes)},
				dest: dest, protocol: f.Protocol, port: f.Port, testID: f.TestID, testName: f.TestName,
			}
			index[id] = c
			cands = append(cands, c)
		}
		if !slices.Contains(c.agents, f.AgentID) {
			c.agents = append(c.agents, f.AgentID)
		}
	}

	// 2. merge candidates that mean the same: from, to, protocol, port
	type group struct{ members []*candidate }
	groups := map[string]*group{}
	var order []string
	for _, c := range cands {
		from := "edge"
		if !c.source.InternetEdge {
			from = joinPrefixes(c.source.Prefixes, ",")
		}
		m := strings.Join([]string{from, joinPrefixes(c.dest, ","), c.protocol, portString(c.port)}, "\x00")
		g, ok := groups[m]
		if !ok {
			g = &group{}
			groups[m] = g
			order = append(order, m)
		}
		g.members = append(g.members, c)
	}

	out := make([]PlannedCheck, 0, len(groups))
	for _, m := range order {
		members := groups[m].members
		owner := members[0]
		for _, c := range members[1:] {
			if c.key < owner.key {
				owner = c
			}
		}
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
