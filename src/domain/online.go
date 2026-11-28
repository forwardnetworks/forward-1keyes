package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// An agent that ThousandEyes does not report as online is not located: what it
// last reported of its addresses may be old or incomplete.

// States kept of an agent that is not online. A state ThousandEyes gives that
// is not one of its documented ones is kept as unknown, never as its own text.
const (
	stateOnline   = "online"
	stateOffline  = "offline"
	stateDisabled = "disabled"
	stateUnknown  = "unknown"
)

// AgentCondition is what is kept of an agent that is not online. It is empty for
// an agent that is online or whose state is not given.
type AgentCondition struct {
	State    string // offline, disabled or unknown
	LastSeen string // RFC 3339 in UTC, or empty
}

// ConditionOf normalizes the state and last-seen time ThousandEyes gives.
func ConditionOf(state, lastSeen string) AgentCondition {
	switch state {
	case "", stateOnline:
		return AgentCondition{}
	case stateOffline, stateDisabled:
	default:
		state = stateUnknown
	}
	return AgentCondition{State: state, LastSeen: utcTime(lastSeen)}
}

// utcTime is s in UTC, or "" when it is not an RFC 3339 time.
func utcTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Online says whether the agent is online, or its state is not given. A cloud
// agent's state is never looked at.
func (a AgentInfo) Online() bool { return a.Kind == AgentCloud || a.Condition.State == "" }

// UnusableReason says why a flow that needs to locate this agent cannot be one.
func (a AgentInfo) UnusableReason() string {
	state := "is " + a.Condition.State
	if a.Condition.State == stateUnknown {
		state = "is in an unknown state"
	}
	seen := ""
	if a.Condition.LastSeen != "" {
		seen = ", last seen " + a.Condition.LastSeen
	}
	return fmt.Sprintf("agent %s %s in ThousandEyes%s: its reported addresses may be old or incomplete", a.ID, state, seen)
}

// NamedAgents are the agents an enabled test of a supported type names, as a
// source or as the target of an agent-to-agent test.
func NamedAgents(tests []TestConfig) map[string]bool {
	out := map[string]bool{}
	for _, t := range tests {
		if !t.Enabled || (t.Type != typeAgentToServer && t.Type != typeHTTPServer && t.Type != typeAgentToAgent) {
			continue
		}
		for _, id := range t.AgentIDs {
			out[id] = true
		}
		if t.Type == typeAgentToAgent {
			out[t.Target] = true
		}
	}
	return out
}

// NotOnlineAgent is a named agent that is not online.
type NotOnlineAgent struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	LastSeen string `json:"last_seen,omitempty"`
	Selected bool   `json:"selected"`
}

// Line is the one line of output about the agent.
func (a NotOnlineAgent) Line() string {
	line := fmt.Sprintf("agent %s not online in ThousandEyes: %s", a.ID, a.State)
	if a.LastSeen != "" {
		line += ", last seen " + a.LastSeen
	}
	if a.Selected {
		return line + " (used by a selected test)"
	}
	return line + " (used only by tests that are not selected)"
}

// NotOnlineAgents lists, in ID order, the agents that are not online and are
// named: by one of tests, or, in a recording, by a test it does not hold. An
// agent is selected when one of the selected tests names it.
func NotOnlineAgents(tests, selected []TestConfig, agents map[string]AgentInfo) []NotOnlineAgent {
	named, chosen := NamedAgents(tests), NamedAgents(selected)
	var out []NotOnlineAgent
	for id, a := range agents {
		if a.Online() || !named[id] && !a.NamedOutside {
			continue
		}
		out = append(out, NotOnlineAgent{ID: id, State: a.Condition.State, LastSeen: a.Condition.LastSeen, Selected: chosen[id]})
	}
	slices.SortFunc(out, func(a, b NotOnlineAgent) int { return compareIDs(a.ID, b.ID) })
	return out
}

// SelectedIDs are the IDs of the selected agents among these.
func SelectedIDs(agents []NotOnlineAgent) []string {
	var out []string
	for _, a := range agents {
		if a.Selected {
			out = append(out, a.ID)
		}
	}
	return out
}

// NotOnlineLine names these agents in one line, or is "" when there is none.
func NotOnlineLine(agents []NotOnlineAgent) string {
	if len(agents) == 0 {
		return ""
	}
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.ID
	}
	return "not online in ThousandEyes: agent " + strings.Join(ids, ", agent ")
}

// WarnRows gives the rows that may be wrong because of these agents their
// reason. Which rows an agent affects is not known and not guessed: any stale
// check may only look stale, and while a selected agent is not online any
// check to be created may exist already.
func WarnRows(rows []Row, agents []NotOnlineAgent) {
	var all []string
	for _, a := range agents {
		all = append(all, a.ID)
	}
	selected := SelectedIDs(agents)
	list := func(ids []string) string { return "agent " + strings.Join(ids, ", agent ") }
	for i := range rows {
		switch {
		case rows[i].Outcome == OutcomeStale && len(all) > 0:
			rows[i].Reason = "may only look stale: ThousandEyes does not report as online " + list(all)
		case rows[i].Outcome == OutcomeCreate && len(selected) > 0:
			rows[i].Reason = "the check may already exist under another key: ThousandEyes does not report as online " + list(selected)
		}
	}
}
