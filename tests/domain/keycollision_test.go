package domain_test

import (
	"net/netip"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// In the from-target direction every check starts at the target, so two senders
// in different subnets must not share a source key, whichever comes first. A
// lone sender keeps the key without a destination.
func TestReverseSendersGetDistinctKeys(t *testing.T) {
	port := 443
	flow := func(agent, dest string) domain.Flow {
		return domain.Flow{TestID: "100", TestName: "t", Direction: "from-target", AgentID: agent,
			Source:       domain.Source{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.10/32")}},
			DestPrefixes: []netip.Prefix{netip.MustParsePrefix(dest)}, Protocol: "tcp", Port: &port}
	}
	key := func(dest string) string {
		return domain.SourceKey(domain.KeyInput{TestID: "100", Protocol: "tcp", Port: &port, Dest: dest,
			Slot: domain.Slot("from-target", domain.Source{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.10/32")}})})
	}
	a, b := flow("1", "10.1.0.5/32"), flow("2", "10.2.0.0/24")

	alone := domain.BuildPlannedChecks([]domain.Flow{a})
	if len(alone) != 1 || alone[0].SourceKey != key("") {
		t.Fatalf("one sender: %+v", alone)
	}
	want := map[string]string{"10.1.0.5/32": key("10.1.0.5"), "10.2.0.0/24": key("10.2.0.0/24")}
	for _, in := range [][]domain.Flow{{a, b}, {b, a}} {
		got := domain.BuildPlannedChecks(in)
		if len(got) != 2 {
			t.Fatalf("two senders: %d checks", len(got))
		}
		for _, c := range got {
			if d := c.DestPrefixes[0].String(); c.SourceKey != want[d] {
				t.Errorf("check to %s: key %s, want %s", d, c.SourceKey, want[d])
			}
		}
	}
}
