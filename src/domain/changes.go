package domain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Kinds of change of a check between two snapshots.
const (
	changeBroke     = "broke"
	changeRecovered = "recovered"
	changeAdded     = "added"
	changeRemoved   = "removed"
	changeChanged   = "changed"
	changeUnknown   = "unknown"
)

// CheckChange is one managed check that differs between two snapshots.
type CheckChange struct {
	CheckID    string   `json:"check_id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	FromStatus string   `json:"from_status"`
	ToStatus   string   `json:"to_status"`
	TestIDs    []string `json:"test_ids"`
}

// PendingWork is what plan would do now.
type PendingWork struct {
	Create        int `json:"create"`
	Update        int `json:"update"`
	Stale         int `json:"stale"`
	Collision     int `json:"collision"`
	NotImportable int `json:"not_importable"`
}

// waiting says whether anything is left to apply or to look at. Tests that
// cannot be imported do not count: they are a standing fact, not new work.
func (p PendingWork) waiting() bool { return p.Create+p.Update+p.Stale+p.Collision > 0 }

func (p PendingWork) String() string {
	return fmt.Sprintf("create %d  update %d  stale %d  collision %d  not importable %d", p.Create, p.Update, p.Stale, p.Collision, p.NotImportable)
}

// Changes is what differs between two snapshots, and what plan would now do.
type Changes struct {
	Network          string        `json:"network"`
	From             string        `json:"from"`
	To               string        `json:"to"`
	NothingToCompare bool          `json:"nothing_to_compare"`
	Changes          []CheckChange `json:"changes"`
	Plan             PendingWork   `json:"plan"`
	// Recording is set when the ThousandEyes data came from a recording.
	Recording *RecordingMark `json:"recording,omitempty"`
}

// Reportable says whether there is something to tell: a change or waiting work.
func (c Changes) Reportable() bool { return len(c.Changes) > 0 || c.Plan.waiting() }

// ExitCode is 0 only when two snapshots were compared and there is nothing to
// tell; otherwise 2.
func (c Changes) ExitCode() int {
	if c.NothingToCompare || c.Reportable() {
		return 2
	}
	return 0
}

const messageLines = 20

// Summary is the text of a notification: short enough for a chat message.
func (c Changes) Summary() string {
	var b strings.Builder
	if c.Recording != nil {
		fmt.Fprintf(&b, "%s\n", c.Recording.Line())
	}
	if c.NothingToCompare {
		fmt.Fprintf(&b, "forward-1keyes on %s: only snapshot %s is processed, nothing to compare.", c.Network, c.To)
	} else {
		fmt.Fprintf(&b, "forward-1keyes on %s: %d check change(s) between snapshot %s and %s.", c.Network, len(c.Changes), c.From, c.To)
	}
	for i, ch := range c.Changes {
		if i == messageLines {
			fmt.Fprintf(&b, "\n... and %d more", len(c.Changes)-messageLines)
			break
		}
		fmt.Fprintf(&b, "\n%s: %s", ch.Kind, ch.Name)
	}
	fmt.Fprintf(&b, "\nplan now: %s", c.Plan)
	return b.String()
}

// ownChecks keeps the checks that carry this account group's ownership tags.
func ownChecks(all map[string]CheckResult, instance string) map[string]CheckResult {
	out := map[string]CheckResult{}
	for id, r := range all {
		if d := DecodeTags(r.Tags); d.Ownership == Ours && d.SourceInstance == instance {
			out[id] = r
		}
	}
	return out
}

// CompareSnapshots lists the managed checks of one source instance that differ
// between two snapshots, matched by check ID, each with at most one change,
// ordered by ID.
func CompareSnapshots(instance string, from, to map[string]CheckResult) []CheckChange {
	before, after := ownChecks(from, instance), ownChecks(to, instance)
	ids := slices.Collect(maps.Keys(before))
	for id := range after {
		if _, ok := before[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, compareIDs)
	out := []CheckChange{}
	for _, id := range ids {
		old, wasThere := before[id]
		now, isThere := after[id]
		change := CheckChange{CheckID: id, Name: now.Name, FromStatus: old.Status, ToStatus: now.Status, TestIDs: []string{}}
		switch {
		case !wasThere:
			change.Kind = changeAdded
		case !isThere:
			change.Kind, change.Name = changeRemoved, old.Name
		case old.Outdated || now.Outdated:
			change.Kind = changeUnknown
		case old.Status == now.Status:
			continue
		case old.Status == "PASS" && now.Status == "FAIL":
			change.Kind = changeBroke
		case old.Status == "FAIL" && now.Status == "PASS":
			change.Kind = changeRecovered
		default:
			change.Kind = changeChanged
		}
		out = append(out, change)
	}
	return out
}
