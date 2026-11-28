package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Account group errors.
var (
	ErrUnknownAccountGroup   = errors.New("domain: unknown ThousandEyes account group")
	ErrAmbiguousAccountGroup = errors.New("domain: more than one ThousandEyes account group has that name")
)

// AccountGroup is a ThousandEyes account group.
type AccountGroup struct {
	Name string
	AID  string
}

// ResolveAccountGroup finds the group with exactly this name.
func ResolveAccountGroup(groups []AccountGroup, name string) (AccountGroup, error) {
	var found []AccountGroup
	for _, g := range groups {
		if g.Name == name {
			found = append(found, g)
		}
	}
	switch len(found) {
	case 0:
		return AccountGroup{}, fmt.Errorf("%w: %q", ErrUnknownAccountGroup, name)
	case 1:
		return found[0], nil
	}
	return AccountGroup{}, fmt.Errorf("%w: %q", ErrAmbiguousAccountGroup, name)
}

// TestSummary is one line of the ThousandEyes test list.
type TestSummary struct {
	ID      string
	Name    string
	Type    string
	Enabled bool
}

// AgentKind says where a ThousandEyes agent runs.
type AgentKind string

const (
	AgentCloud      AgentKind = "cloud"
	AgentEnterprise AgentKind = "enterprise"
	AgentCluster    AgentKind = "enterprise-cluster"
)

// AgentInfo is a ThousandEyes agent. A cluster is one agent whose addresses
// are the union of its members'.
type AgentInfo struct {
	ID        string
	Kind      AgentKind
	Addresses []netip.Addr
	// Condition is set only for an agent that is not online.
	Condition AgentCondition
	// NamedOutside is set only by a recording: a test it does not hold names
	// this agent, which is not online.
	NamedOutside bool
}

// TargetHost is the host a test points at: the server of an agent-to-server
// test, or the host part of the URL of an http-server test. ok is false when
// the test has no host target.
func TargetHost(t TestConfig) (host string, ok bool) {
	switch t.Type {
	case typeAgentToServer:
		return strings.TrimSpace(t.Target), strings.TrimSpace(t.Target) != ""
	case typeHTTPServer:
		u, err := url.Parse(t.Target)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return u.Hostname(), true
	}
	return "", false
}
