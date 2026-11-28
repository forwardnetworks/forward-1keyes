package domain

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// NetworkRecord is one network-layer result of a test: one agent, one
// destination address, one round.
type NetworkRecord struct {
	AgentID    string
	ServerIP   string
	MeasuredAt time.Time
	Loss       *float64 // nil when the record carries no loss figure
	Error      string   // the record's error details, empty when it has none
}

// CheckResult is what Forward says about one check on a snapshot.
type CheckResult struct {
	Status      string
	Outdated    bool
	Destination string // the destination of the check as Forward holds it
	Name        string
	Tags        []string
}

// How one record reads, and how the records of a check combine.
const (
	readingWorking  = "working"
	readingFailing  = "failing"
	readingDegraded = "degraded"
	readingUnknown  = "unknown"

	measuredMixed  = "mixed"
	measuredNoData = "no data"
)

// Verdicts of a compared check.
const (
	verdictHealthy        = "healthy"
	verdictBothBlocked    = "both-blocked"
	verdictNetworkAllows  = "network-allows"
	verdictModelDisagrees = "model-disagrees"
	verdictInconclusive   = "inconclusive"
)

// read says what one record shows. An error means failing whatever the loss
// says, and a missing loss is never read as no loss.
func read(r NetworkRecord) string {
	switch {
	case r.Error != "" || r.Loss != nil && *r.Loss >= 100:
		return readingFailing
	case r.Loss == nil:
		return readingUnknown
	case *r.Loss == 0:
		return readingWorking
	}
	return readingDegraded
}

func combine(readings []string) string {
	count := map[string]int{}
	for _, r := range readings {
		count[r]++
	}
	switch n := len(readings); {
	case count[readingUnknown] == n:
		return measuredNoData
	case count[readingWorking] == n:
		return readingWorking
	case count[readingFailing] == n:
		return readingFailing
	}
	return measuredMixed
}

// judge compares what was measured with the status of the check, and says in
// one line what to look at first.
func judge(measured string, result CheckResult) (string, string) {
	switch {
	case result.Outdated:
		return verdictInconclusive, "Forward's result is for an earlier definition of the check; wait for it to be evaluated again"
	case result.Status != "PASS" && result.Status != "FAIL":
		return verdictInconclusive, "Forward has no pass or fail for this check; look at the check in Forward"
	case measured == measuredNoData:
		return verdictInconclusive, "nothing was measured; check that the test is running, or use a longer --window"
	case measured == measuredMixed:
		return verdictInconclusive, "the measurements disagree or show partial loss; look at each record"
	case measured == readingWorking && result.Status == "PASS":
		return verdictHealthy, "nothing to do"
	case measured == readingFailing && result.Status == "FAIL":
		return verdictBothBlocked, "start with the path in Forward: the network model does not allow this flow"
	case measured == readingFailing:
		return verdictNetworkAllows, "the model allows this flow; look at the destination service, the circuit, live device state or an old snapshot"
	}
	return verdictModelDisagrees, "traffic works but the model blocks it; check the source address first, then collect the network again"
}

// ReportRecord is one measurement listed in a report.
type ReportRecord struct {
	TestID     string    `json:"test_id"`
	AgentID    string    `json:"agent_id"`
	ServerIP   string    `json:"server_ip"`
	MeasuredAt time.Time `json:"measured_at"`
	Reading    string    `json:"reading"`
}

// ReportRow is one plan row in a report, compared or not.
type ReportRow struct {
	CheckID             string         `json:"check_id"`
	Name                string         `json:"name"`
	PlanOutcome         Outcome        `json:"plan_outcome"`
	Compared            bool           `json:"compared"`
	Reason              string         `json:"reason"`
	DiffersFromPlan     bool           `json:"differs_from_plan"`
	TestIDs             []string       `json:"test_ids"`
	AgentIDs            []string       `json:"agent_ids"`
	Measured            string         `json:"measured"`
	ForwardStatus       string         `json:"forward_status"`
	Outdated            bool           `json:"outdated"`
	Verdict             string         `json:"verdict"`
	Guidance            string         `json:"guidance"`
	SnapshotProcessedAt string         `json:"snapshot_processed_at"`
	OlderThanSnapshot   bool           `json:"older_than_snapshot"`
	Records             []ReportRecord `json:"records"`
}

// Report is the measured results beside the modelled ones.
type Report struct {
	SnapshotID string      `json:"snapshot_id"`
	Rows       []ReportRow `json:"rows"`
	// Recording is set when the ThousandEyes data came from a recording.
	Recording *RecordingMark `json:"recording,omitempty"`
	// NotOnline is present only when a named agent is not online.
	NotOnline []NotOnlineAgent `json:"agents_not_online,omitempty"`
}

// ExitCode is 0 only when at least one check was compared, every compared
// check is healthy and every agent of a selected test is online; otherwise 2.
func (r Report) ExitCode() int {
	if len(SelectedIDs(r.NotOnline)) > 0 {
		return 2 // something that should have been compared was not
	}
	compared := 0
	for _, row := range r.Rows {
		if !row.Compared {
			continue
		}
		compared++
		if row.Verdict != verdictHealthy {
			return 2
		}
	}
	if compared == 0 {
		return 2
	}
	return 0
}

// NotComparedReason says why a plan row gets no verdict, or "" when it is
// compared. testTypes maps a test ID to its type.
func NotComparedReason(row Row, testTypes map[string]string) string {
	switch row.Outcome {
	case OutcomeUnchanged, OutcomeUpdate:
		if row.CheckID == "" || len(row.TestIDs) == 0 {
			return "no test is known to be behind this check"
		}
		if slices.ContainsFunc(row.TestIDs, func(id string) bool { return testTypes[id] == "agent-to-agent" }) {
			return "agent-to-agent checks are not compared yet"
		}
		return ""
	case OutcomeCreate:
		// A create or stale row has a reason of its own only when an agent that
		// is not online makes the usual one untrue.
		return cmp.Or(row.Reason, "there is no check in Forward yet")
	case OutcomeStale:
		return cmp.Or(row.Reason, "no test is behind this check any more")
	}
	return strings.TrimSpace(string(row.Outcome) + ": " + row.Reason)
}

// Compare builds the row of a compared check from the records of its tests (by
// test ID), the result Forward holds and the time its snapshot was processed.
// A record counts only when it is from one of the check's agents and for the
// check's destination; of those, the latest per test, agent and destination.
func Compare(row Row, byTest map[string][]NetworkRecord, result CheckResult, processed time.Time) (ReportRow, error) {
	out := ReportRow{
		CheckID: row.CheckID, Name: row.Name, PlanOutcome: row.Outcome, Compared: true,
		DiffersFromPlan: row.Outcome == OutcomeUpdate, TestIDs: row.TestIDs, AgentIDs: row.AgentIDs,
		ForwardStatus: result.Status, Outdated: result.Outdated,
		SnapshotProcessedAt: processed.UTC().Format(time.RFC3339), Records: []ReportRecord{},
	}
	destination, err := destinationAddr(result.Destination)
	if err != nil {
		return ReportRow{}, fmt.Errorf("domain: check %s: %w", row.CheckID, err)
	}
	var readings []string
	for _, test := range row.TestIDs {
		latest := map[string]NetworkRecord{}
		for _, r := range byTest[test] {
			ip, err := netip.ParseAddr(r.ServerIP)
			if err != nil || ip != destination || !slices.Contains(row.AgentIDs, r.AgentID) {
				continue
			}
			if held, ok := latest[r.AgentID]; !ok || r.MeasuredAt.After(held.MeasuredAt) {
				latest[r.AgentID] = r
			}
		}
		for agent, r := range latest {
			reading := read(r)
			readings = append(readings, reading)
			out.Records = append(out.Records, ReportRecord{TestID: test, AgentID: agent, ServerIP: r.ServerIP, MeasuredAt: r.MeasuredAt.UTC(), Reading: reading})
			out.OlderThanSnapshot = out.OlderThanSnapshot || r.MeasuredAt.Before(processed)
		}
	}
	slices.SortFunc(out.Records, func(a, b ReportRecord) int {
		return cmp.Or(compareIDs(a.TestID, b.TestID), compareIDs(a.AgentID, b.AgentID))
	})
	out.Measured = combine(readings)
	out.Verdict, out.Guidance = judge(out.Measured, result)
	return out, nil
}

// destinationAddr reads a check's destination, which for a compared check is
// one address, written bare or as a host prefix.
func destinationAddr(s string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil && p.IsSingleIP() {
		return p.Addr(), nil
	}
	return netip.Addr{}, fmt.Errorf("its destination %q is not one address", s)
}
