package domain

import (
	"net/netip"
	"net/url"
	"strconv"
)

// Candidate is one flow a test describes, from one assigned agent.
type Candidate struct {
	TestID      string
	AgentID     string
	Destination string // an IP literal or a hostname
	DestAddrs   []netip.Addr
	Protocol    string // "tcp", "udp" or "icmp"
	Port        *int   // nil for ICMP
}

// NotImportable says why a test yields no candidate.
type NotImportable struct {
	Kind   string // for example "missing port", "proxy configured", "unsupported type"
	Reason string // human text; names the type for an unsupported one
}

// FlowResult is either candidates or a reason, never both.
type FlowResult struct {
	Candidates    []Candidate
	NotImportable *NotImportable
}

// Kinds of NotImportable the flow mapping produces.
const (
	kindMissingPort     = "missing port"
	kindProxyConfigured = "proxy configured"
	kindUnsupportedType = "unsupported type"
	kindUnresolved      = "unresolved"
	kindInvalidTarget   = "invalid target"
	typeAgentToServer   = "agent-to-server"
	typeHTTPServer      = "http-server"
)

// MapFlows maps one test to candidates, one per assigned agent, in the order
// the test lists its agents. pins holds the sorted addresses each hostname
// resolved to.
//
// agent-to-agent tests need the target agent's location and are mapped by a
// later step; here they are reported as unsupported.
func MapFlows(t TestConfig, pins map[string][]netip.Addr) FlowResult {
	switch t.Type {
	case typeAgentToServer:
		return mapAgentToServer(t, pins)
	case typeHTTPServer:
		return mapHTTPServer(t, pins)
	default:
		return notImportable(kindUnsupportedType, "unsupported type "+t.Type)
	}
}

func mapAgentToServer(t TestConfig, pins map[string][]netip.Addr) FlowResult {
	var port *int
	switch t.Protocol {
	case "icmp":
		// ICMP has no port, whatever the test carries.
	case "tcp", "udp":
		if t.Port == nil {
			return notImportable(kindMissingPort, t.Protocol+" test has no port")
		}
		p := *t.Port
		port = &p
	default:
		return notImportable(kindUnsupportedType, "unsupported protocol "+t.Protocol+" in test type "+t.Type)
	}
	return candidates(t, t.Target, t.Protocol, port, pins)
}

func mapHTTPServer(t TestConfig, pins map[string][]netip.Addr) FlowResult {
	if t.UsesProxy {
		return notImportable(kindProxyConfigured, "the test goes through a proxy")
	}
	u, err := url.Parse(t.Target)
	shown := recordedTarget(t.Type, t.Target) // a reason is printed: no user, query or fragment
	if err != nil || u.Hostname() == "" {
		return notImportable(kindInvalidTarget, "cannot read a host from "+shown)
	}
	var port int
	switch {
	case u.Port() != "":
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return notImportable(kindInvalidTarget, "invalid port in "+shown)
		}
		port = p
	case u.Scheme == "https":
		port = 443
	case u.Scheme == "http":
		port = 80
	default:
		return notImportable(kindInvalidTarget, "unsupported scheme in "+shown)
	}
	return candidates(t, u.Hostname(), "tcp", &port, pins)
}

// candidates builds one candidate per assigned agent for a destination that
// is an IP literal or a pinned hostname.
func candidates(t TestConfig, dest, protocol string, port *int, pins map[string][]netip.Addr) FlowResult {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(dest); err == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs = pins[dest]
		if len(addrs) == 0 {
			return notImportable(kindUnresolved, "no address pinned for "+dest)
		}
	}
	out := make([]Candidate, 0, len(t.AgentIDs))
	for _, agent := range t.AgentIDs {
		c := Candidate{
			TestID:      t.ID,
			AgentID:     agent,
			Destination: dest,
			DestAddrs:   append([]netip.Addr(nil), addrs...),
			Protocol:    protocol,
		}
		if port != nil {
			p := *port
			c.Port = &p
		}
		out = append(out, c)
	}
	return FlowResult{Candidates: out}
}

func notImportable(kind, reason string) FlowResult {
	return FlowResult{NotImportable: &NotImportable{Kind: kind, Reason: reason}}
}
