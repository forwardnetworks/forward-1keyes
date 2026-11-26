package domain

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// SourceWindow is how far back observed source addresses are read.
const SourceWindow = time.Hour

// maxSourceTests is the most tests whose observed sources are read in one run.
const maxSourceTests = 20

// SourceAgent is an agent that gives at least one test two or more distinct
// source prefixes. Tests are those tests, in test ID order; Prefixes are the
// source prefixes of the agent's flows in them.
type SourceAgent struct {
	ID       string
	Cluster  bool
	Tests    []string
	Prefixes []netip.Prefix
}

// ObservedSource is one result that says where a test was sent from. Direction
// is empty when the result names none. Address is the zero value when the
// result holds no usable IP address.
type ObservedSource struct {
	AgentID   string
	Direction string
	Address   netip.Addr
}

// SourceRead is what was read of one test. Complete is false when the read
// failed or stopped before the last page; Sources then holds what came first.
type SourceRead struct {
	Sources  []ObservedSource
	Complete bool
}

// MultiSourceAgents says which agents give one test more than one source,
// judged over the to-target flows alone: in any other direction the agent is
// not the source. The agents come in ID order.
func MultiSourceAgents(flows []Flow, agents map[string]AgentInfo) []SourceAgent {
	type pair struct{ agent, test string }
	sources := map[pair][]netip.Prefix{}
	for _, f := range flows {
		if f.Direction != "to-target" {
			continue
		}
		k := pair{f.AgentID, f.TestID}
		for _, p := range f.Source.Prefixes {
			if !slices.Contains(sources[k], p) {
				sources[k] = append(sources[k], p)
			}
		}
	}
	found := map[string]*SourceAgent{}
	for k, prefixes := range sources {
		if len(prefixes) < 2 {
			continue
		}
		a := found[k.agent]
		if a == nil {
			a = &SourceAgent{ID: k.agent, Cluster: agents[k.agent].Kind == AgentCluster}
			found[k.agent] = a
		}
		a.Tests = append(a.Tests, k.test)
		a.Prefixes = append(a.Prefixes, prefixes...)
	}
	out := make([]SourceAgent, 0, len(found))
	for _, a := range found {
		slices.SortFunc(a.Tests, compareIDs)
		a.Prefixes = sortedPrefixes(a.Prefixes)
		out = append(out, *a)
	}
	slices.SortFunc(out, func(a, b SourceAgent) int { return compareIDs(a.ID, b.ID) })
	return out
}

// SourceTests are the tests to read for these agents, each once and in test ID
// order, and the number left unread because of the limit on one run.
func SourceTests(agents []SourceAgent) (read []string, unread int) {
	for _, a := range agents {
		read = append(read, a.Tests...)
	}
	slices.SortFunc(read, compareIDs)
	read = slices.Compact(read)
	if len(read) > maxSourceTests {
		return read[:maxSourceTests], len(read) - maxSourceTests
	}
	return read, 0
}

// SourceReport is what the warning about multiple sources is written from.
type SourceReport struct {
	Agents []SourceAgent
	// Used holds every agent with a planned flow, at either end, with the
	// addresses left after the ignored subnets.
	Used map[string][]netip.Addr
	// ReturnTo holds, by agent, the prefixes of that agent which a planned
	// check goes to: the destinations of its from-target flows.
	ReturnTo map[string][]netip.Prefix
	// Recording is set when the plan was made from a recording, which holds no
	// observed sources: nothing was read.
	Recording bool
	// Reads is by test ID; a test with no entry was not read.
	Reads  map[string]SourceRead
	Unread int
}

// Warning is the text of the warning, or "" when no agent meets the condition.
// The only values in it that come from observed sources are parsed IP
// addresses. It suggests the ignore flag and changes nothing.
func (r SourceReport) Warning() string {
	if len(r.Agents) == 0 {
		return ""
	}
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("warning: an agent gives a test more than one source prefix, and each one gets its own check.")
	if r.Recording {
		line("This plan uses a recording. Which address a test used is not in a recording, so nothing was looked up and no flag is suggested.")
	} else {
		line("Source addresses in ThousandEyes path visualization results of the last 1 hour:")
	}
	for _, a := range r.Agents {
		line("agent %s (tests %s):", a.ID, strings.Join(a.Tests, ", "))
		if r.Recording {
			for _, p := range a.Prefixes {
				line("  source %s", p)
			}
			continue
		}
		r.observed(a, line)
	}
	if r.Unread > 0 {
		line("Tests not read: %d (at most %d are read in one run).", r.Unread, maxSourceTests)
	}
	return b.String()
}

// observed writes what was seen of one agent, and what may be suggested.
func (r SourceReport) observed(a SourceAgent, line func(string, ...any)) {
	seen := make([][]netip.Addr, len(a.Prefixes))
	var failed, silent, left []string
	conclusive, anySeen := true, false
	for _, test := range a.Tests {
		read, tried := r.Reads[test]
		if !tried {
			left = append(left, test)
			conclusive = false
			continue
		}
		reported := false
		for _, s := range read.Sources {
			if s.AgentID != a.ID || (s.Direction != "" && s.Direction != "to-target") || !s.Address.IsValid() {
				continue
			}
			reported = true
			for i, p := range a.Prefixes {
				if p.Contains(s.Address) {
					seen[i] = append(seen[i], s.Address)
					anySeen = true
				}
			}
		}
		switch {
		case !read.Complete:
			failed = append(failed, test)
			conclusive = false
		case !reported:
			silent = append(silent, test)
			conclusive = false
		}
	}

	// With a read missing or silent, "not seen" speaks only for what was read.
	notSeen := "not seen"
	if !conclusive {
		notSeen = "not seen in the results read"
	}
	for i, p := range a.Prefixes {
		if len(seen[i]) == 0 {
			line("  %s: %s", p, notSeen)
			continue
		}
		slices.SortFunc(seen[i], netip.Addr.Compare)
		var addrs []string
		for _, ad := range slices.Compact(seen[i]) {
			addrs = append(addrs, ad.String())
		}
		line("  %s: seen as %s", p, strings.Join(addrs, ", "))
	}
	if len(failed) > 0 {
		line("  The results of these tests could not be read, or were not all read: %s", strings.Join(failed, ", "))
	}
	if len(silent) > 0 {
		line("  No source address for this agent was reported in these tests: %s", strings.Join(silent, ", "))
	}
	if len(left) > 0 {
		line("  Left out because of the limit on one run, these tests: %s", strings.Join(left, ", "))
	}

	if !conclusive || !anySeen {
		return
	}
	if a.Cluster {
		line("  No flag is suggested: this agent is a cluster, and which agent ID its results carry is not known.")
		return
	}
	suggested := false
	for i, p := range a.Prefixes {
		if len(seen[i]) > 0 {
			continue
		}
		if others := r.othersIn(a.ID, p); len(others) > 0 {
			line("  No flag is suggested for %s: it would also remove addresses this plan uses of agent %s", p, strings.Join(others, ", agent "))
			continue
		}
		suggested = true
		line("  To plan without %s, add: --ignore-agent-subnet %s", p, p)
		if slices.ContainsFunc(r.ReturnTo[a.ID], p.Overlaps) {
			line("  Take care: a planned check also goes to this agent in %s (the return check of an agent-to-agent test). The flag would change it too, and its traffic was not looked up.", p)
		}
	}
	if suggested {
		line("  Not being seen in one hour does not prove a prefix unused.")
		line("  With the flag, the next plan marks checks already made from that prefix stale.")
	}
}

// othersIn names the other agents the plan uses that have an address in p: the
// ignore flag for p would take it from them too.
func (r SourceReport) othersIn(agent string, p netip.Prefix) []string {
	var out []string
	for id, addrs := range r.Used {
		if id != agent && slices.ContainsFunc(addrs, p.Contains) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, compareIDs)
	return out
}

// ReturnDestinations are, by agent, the prefixes of that agent which a check
// goes to: in a from-target flow the agent is the destination.
func ReturnDestinations(flows []Flow) map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	for _, f := range flows {
		if f.Direction == "from-target" {
			out[f.AgentID] = append(out[f.AgentID], f.DestPrefixes...)
		}
	}
	return out
}
