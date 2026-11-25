package domain_test

import (
	"net/netip"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Test: a test fingerprint changes with every decoded field
// and ignores the order of labels and agents; a cloud agent's fingerprint
// ignores its addresses, an enterprise agent's does not.
func TestFingerprints(t *testing.T) {
	port := 443
	base := domain.TestConfig{ID: "1", Name: "web", Type: "agent-to-server", Enabled: true, Labels: []string{"a", "b"}, Target: "10.0.0.5",
		Protocol: "tcp", Port: &port, AgentIDs: []string{"1", "2"}}
	fp := domain.TestFingerprint(base)
	if fp == "" || fp != domain.TestFingerprint(base) {
		t.Fatal("a fingerprint must be stable and not empty")
	}
	reordered := base
	reordered.Labels, reordered.AgentIDs = []string{"b", "a"}, []string{"2", "1"}
	if domain.TestFingerprint(reordered) != fp {
		t.Fatal("the order of labels and agents must not change the fingerprint")
	}
	other := 80
	changes := map[string]func(c *domain.TestConfig){
		"name": func(c *domain.TestConfig) { c.Name = "x" }, "enabled": func(c *domain.TestConfig) { c.Enabled = false },
		"target": func(c *domain.TestConfig) { c.Target = "10.0.0.6" }, "port": func(c *domain.TestConfig) { c.Port = &other },
		"protocol": func(c *domain.TestConfig) { c.Protocol = "udp" }, "proxy": func(c *domain.TestConfig) { c.UsesProxy = true },
		"agents": func(c *domain.TestConfig) { c.AgentIDs = []string{"1"} }, "labels": func(c *domain.TestConfig) { c.Labels = []string{"a"} },
	}
	for name, change := range changes {
		c := base
		change(&c)
		if domain.TestFingerprint(c) == fp {
			t.Fatalf("changing the %s did not change the fingerprint", name)
		}
	}
	cloudA := domain.AgentInfo{ID: "9", Kind: domain.AgentCloud, Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.4")}}
	cloudB := domain.AgentInfo{ID: "9", Kind: domain.AgentCloud, Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.99")}}
	if domain.AgentFingerprint(cloudA) != domain.AgentFingerprint(cloudB) {
		t.Fatal("a cloud agent's address change must not read as drift")
	}
	entA := domain.AgentInfo{ID: "9", Kind: domain.AgentEnterprise, Addresses: []netip.Addr{netip.MustParseAddr("10.1.0.7")}}
	entB := domain.AgentInfo{ID: "9", Kind: domain.AgentEnterprise, Addresses: []netip.Addr{netip.MustParseAddr("10.1.0.8")}}
	if domain.AgentFingerprint(entA) == domain.AgentFingerprint(entB) {
		t.Fatal("an enterprise agent's address change must read as drift")
	}
	if domain.AgentFingerprint(entA) == domain.AgentFingerprint(cloudA) {
		t.Fatal("the kind is part of the fingerprint")
	}
}

// The kind alone changes an agent's fingerprint: the same ID and the same
// addresses as an enterprise agent and as a cluster are two different agents.
func TestAgentKindIsPartOfItsFingerprint(t *testing.T) {
	addrs := []netip.Addr{netip.MustParseAddr("10.1.0.7")}
	a := domain.AgentInfo{ID: "9", Kind: domain.AgentEnterprise, Addresses: addrs}
	b := domain.AgentInfo{ID: "9", Kind: domain.AgentCluster, Addresses: addrs}
	if domain.AgentFingerprint(a) == domain.AgentFingerprint(b) {
		t.Fatal("two kinds of agent with one ID and one address list must not share a fingerprint")
	}
	c := domain.AgentInfo{ID: "10", Kind: domain.AgentEnterprise, Addresses: addrs}
	if domain.AgentFingerprint(a) == domain.AgentFingerprint(c) {
		t.Fatal("the ID is part of the fingerprint")
	}
}
