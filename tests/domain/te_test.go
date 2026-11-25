package domain_test

import (
	"errors"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Test: an account group is chosen by exact name.
func TestResolveAccountGroup(t *testing.T) {
	groups := []domain.AccountGroup{{Name: "Acme", AID: "1234"}, {Name: "Other", AID: "99"}, {Name: "Twin", AID: "1"}, {Name: "Twin", AID: "2"}}
	if g, err := domain.ResolveAccountGroup(groups, "Acme"); err != nil || g.AID != "1234" {
		t.Fatalf("Acme = %+v, %v; want aid 1234", g, err)
	}
	if _, err := domain.ResolveAccountGroup(groups, "Nope"); !errors.Is(err, domain.ErrUnknownAccountGroup) {
		t.Fatalf("Nope: err = %v, want ErrUnknownAccountGroup", err)
	}
	if _, err := domain.ResolveAccountGroup(groups, "acme"); !errors.Is(err, domain.ErrUnknownAccountGroup) {
		t.Fatalf("a different case must not match: err = %v", err)
	}
	if _, err := domain.ResolveAccountGroup(groups, "Twin"); !errors.Is(err, domain.ErrAmbiguousAccountGroup) {
		t.Fatalf("Twin: err = %v, want ErrAmbiguousAccountGroup", err)
	}
}

// Test: the host a test points at.
func TestTargetHost(t *testing.T) {
	cases := []struct {
		t    domain.TestConfig
		want string
		ok   bool
	}{
		{domain.TestConfig{Type: "agent-to-server", Target: "app.example.com"}, "app.example.com", true},
		{domain.TestConfig{Type: "agent-to-server", Target: "10.0.0.5"}, "10.0.0.5", true},
		{domain.TestConfig{Type: "http-server", Target: "https://app.example.com:8443/x"}, "app.example.com", true},
		{domain.TestConfig{Type: "http-server", Target: "not a url"}, "", false},
		{domain.TestConfig{Type: "dns-server", Target: "x"}, "", false},
		{domain.TestConfig{Type: "agent-to-server", Target: ""}, "", false},
	}
	for _, c := range cases {
		if got, ok := domain.TargetHost(c.t); got != c.want || ok != c.ok {
			t.Fatalf("TargetHost(%+v) = %q, %v; want %q, %v", c.t, got, ok, c.want, c.ok)
		}
	}
}
