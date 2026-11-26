package domain_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// When the evidence about an agent is incomplete, a subnet that was not seen
// must not read as if everything had been looked at.
func TestSourceWarningQualifiesNotSeenWhenEvidenceIsIncomplete(t *testing.T) {
	lan, mgmt := netip.MustParsePrefix("10.20.1.0/24"), netip.MustParsePrefix("172.28.28.0/24")
	seen := domain.ObservedSource{AgentID: "7", Address: netip.MustParseAddr("10.20.1.10")}
	warning := func(reads map[string]domain.SourceRead) string {
		return domain.SourceReport{
			Agents: []domain.SourceAgent{{ID: "7", Tests: []string{"1", "2"}, Prefixes: []netip.Prefix{lan, mgmt}}},
			Reads:  reads,
		}.Warning()
	}
	line := func(w string) string {
		for _, l := range strings.Split(w, "\n") {
			if strings.Contains(l, mgmt.String()+": ") {
				return strings.TrimSpace(l)
			}
		}
		t.Fatalf("no line for %s:\n%s", mgmt, w)
		return ""
	}
	good := domain.SourceRead{Sources: []domain.ObservedSource{seen}, Complete: true}

	if got := line(warning(map[string]domain.SourceRead{"1": good, "2": good})); got != "172.28.28.0/24: not seen" {
		t.Errorf("complete evidence: %q", got)
	}
	for name, reads := range map[string]map[string]domain.SourceRead{
		"nothing could be read": {"1": {}, "2": {}},
		"one read failed":       {"1": good, "2": {}},
		"one test not read":     {"1": good},
		"one test says nothing": {"1": good, "2": {Complete: true}},
	} {
		if got := line(warning(reads)); got != "172.28.28.0/24: not seen in the results read" {
			t.Errorf("%s: %q", name, got)
		}
	}
}
