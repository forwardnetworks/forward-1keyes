package domain_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Only a state that is given and is not "online" counts, only documented states
// keep their own word, and no other text is carried.
func TestConditionOfAnAgent(t *testing.T) {
	const at, utc = "2026-11-27T14:36:02+01:00", "2026-11-27T13:36:02Z"
	for _, c := range []struct {
		state, seen string
		want        domain.AgentCondition
	}{
		{"", at, domain.AgentCondition{}},
		{"online", at, domain.AgentCondition{}},
		{"offline", at, domain.AgentCondition{State: "offline", LastSeen: utc}},
		{"disabled", "", domain.AgentCondition{State: "disabled"}},
		{"Online", "yesterday", domain.AgentCondition{State: "unknown"}},
		{"<script>", at, domain.AgentCondition{State: "unknown", LastSeen: utc}},
	} {
		if got := domain.ConditionOf(c.state, c.seen); got != c.want {
			t.Errorf("ConditionOf(%q, %q) = %+v, want %+v", c.state, c.seen, got, c.want)
		}
	}
}

// wantReason requires a row's reason to give the warning and name the agent.
func wantReason(t *testing.T, what, reason, warning string) {
	t.Helper()
	if !strings.Contains(reason, warning) || !strings.Contains(reason, "agent 7") {
		t.Errorf("%s: reason %q, want %q naming agent 7", what, reason, warning)
	}
}

// Nothing is created or updated while an agent of a selected test is not
// online; with nothing to write, or only unselected agents down, a plan is as
// appliable as before.
func TestNothingIsWrittenOnAPartialView(t *testing.T) {
	down := domain.AgentInfo{ID: "7", Kind: domain.AgentEnterprise, Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.7")},
		Condition: domain.ConditionOf("offline", "")}
	tests := []domain.TestConfig{{ID: "1", Type: "agent-to-server", Enabled: true, AgentIDs: []string{"7"}}}
	agents := map[string]domain.AgentInfo{"7": down}
	for _, c := range []struct {
		name      string
		selected  []domain.TestConfig
		outcome   domain.Outcome
		appliable bool
		exit      int
	}{
		{"selected, a create", tests, domain.OutcomeCreate, false, 3},
		{"selected, an update", tests, domain.OutcomeUpdate, false, 3},
		{"selected, only stale", tests, domain.OutcomeStale, true, 0},
		{"unselected, a create", nil, domain.OutcomeCreate, true, 2},
	} {
		rows := []domain.Row{{Outcome: c.outcome, SourceKey: "k", Name: "n"}}
		notOnline := domain.NotOnlineAgents(tests, c.selected, agents)
		domain.WarnRows(rows, notOnline)
		body, err := domain.NewPlan(domain.PlanInput{UpdateStrategy: "U1", MaxFlows: 10, Rows: rows, NotOnline: notOnline})
		if err != nil {
			t.Fatal(err)
		}
		if body.Appliable != c.appliable || body.ExitCode() != c.exit {
			t.Errorf("%s: appliable %v exit %d, want %v %d", c.name, body.Appliable, body.ExitCode(), c.appliable, c.exit)
		}
		if c.outcome == domain.OutcomeStale {
			wantReason(t, c.name, domain.NotComparedReason(body.Rows[0], nil), "may only look stale")
		}
		if c.outcome == domain.OutcomeCreate && c.selected != nil {
			wantReason(t, c.name, domain.NotComparedReason(body.Rows[0], nil), "may already exist under another key")
		}
	}
}
