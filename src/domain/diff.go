package domain

import "strings"

// Outcome is what a plan does with one row.
type Outcome string

const (
	OutcomeCreate    Outcome = "create"
	OutcomeUpdate    Outcome = "update"
	OutcomeUnchanged Outcome = "unchanged"
	OutcomeStale     Outcome = "stale"
	OutcomeCollision Outcome = "collision"

	// OutcomeNotImportable is a test or flow that yields no check.
	OutcomeNotImportable Outcome = "not_importable"
)

// Collision reasons.
const (
	reasonMalformedTuple   = "malformed tuple"
	reasonNewerContract    = "newer contract"
	ReasonName             = "name"
	ReasonInactiveSameName = "inactive same name"
	ReasonDuplicateKey     = "duplicate key"
)

// DesiredRow is a check the plan wants. ContentHash covers the name, note,
// tags and definition the tool would write.
type DesiredRow struct {
	SourceKey   string
	Name        string
	ContentHash string
}

// ObservedCheck is a Forward check as read. ContentHash is the same hash,
// computed from what Forward returned.
type ObservedCheck struct {
	ID          string
	Name        string
	Tags        []string
	Active      bool
	ContentHash string
}

// DiffOptions carries what the diff needs besides the two lists.
type DiffOptions struct {
	SourceInstance            string // for example "te-aid-1234"
	UniqueNamesAcrossInactive bool   // what a live Forward does
}

// Row is one line of a plan.
type Row struct {
	Outcome   Outcome `json:"outcome"`
	SourceKey string  `json:"source_key"`
	Name      string  `json:"name"`
	CheckID   string  `json:"check_id"`
	Reason    string  `json:"reason"`
	// Spec is the check a create or update row writes.
	Spec *CheckSpec `json:"spec"`
	// TestIDs and AgentIDs are the ThousandEyes tests and agents behind the row.
	TestIDs  []string `json:"test_ids"`
	AgentIDs []string `json:"agent_ids"`
	// Notes link rows that belong together, for example a create that replaces a stale check.
	Notes []string `json:"notes"`
}

// DiffResult is the rows, plus the IDs of our inactive checks, which are
// listed but never matched.
type DiffResult struct {
	Rows          []Row
	InactiveOwned []string
}

// Diff compares desired rows with observed checks. Only checks that are
// active, ours and carry opts.SourceInstance are matched by key or reported
// stale. Rows come in this order: one per desired row, then a collision row for
// each active check whose tuple is malformed or newer, then the stale checks.
func Diff(opts DiffOptions, desired []DesiredRow, observed []ObservedCheck) DiffResult {
	var res DiffResult
	byKey := map[string][]ObservedCheck{}   // active, ours, this instance
	var ownedActive []ObservedCheck         // the same checks, in input order
	var brokenRows []Row                    // malformed or newer tuples
	activeNames := map[string]bool{}        // every active check, whoever owns it
	inactiveOwnedNames := map[string]bool{} // our inactive checks of this instance
	keys := map[string]string{}             // check ID -> source key

	for _, c := range observed {
		dec := DecodeTags(c.Tags)
		if c.Active {
			activeNames[c.Name] = true
		}
		switch dec.Ownership {
		case MalformedTuple, NewerContract:
			if c.Active {
				reason := reasonMalformedTuple
				if dec.Ownership == NewerContract {
					reason = reasonNewerContract
				}
				brokenRows = append(brokenRows, Row{Outcome: OutcomeCollision, Name: c.Name, CheckID: c.ID, Reason: reason})
			}
		case Ours:
			if dec.SourceInstance != opts.SourceInstance {
				continue // another account group's check: ignored unless its name clashes
			}
			keys[c.ID] = dec.SourceKey
			if !c.Active {
				res.InactiveOwned = append(res.InactiveOwned, c.ID)
				inactiveOwnedNames[c.Name] = true
				continue
			}
			byKey[dec.SourceKey] = append(byKey[dec.SourceKey], c)
			ownedActive = append(ownedActive, c)
		}
	}

	wanted := map[string]bool{}
	for _, d := range desired {
		wanted[d.SourceKey] = true
		row := Row{SourceKey: d.SourceKey, Name: d.Name}
		matches := byKey[d.SourceKey]
		switch {
		case len(matches) > 1:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			row.Outcome, row.Reason, row.CheckID = OutcomeCollision, ReasonDuplicateKey, strings.Join(ids, ",")
		case len(matches) == 1:
			row.CheckID = matches[0].ID
			if matches[0].ContentHash == d.ContentHash {
				row.Outcome = OutcomeUnchanged
			} else {
				row.Outcome = OutcomeUpdate
			}
		case activeNames[d.Name]:
			row.Outcome, row.Reason = OutcomeCollision, ReasonName
		case opts.UniqueNamesAcrossInactive && inactiveOwnedNames[d.Name]:
			row.Outcome, row.Reason = OutcomeCollision, ReasonInactiveSameName
		default:
			row.Outcome = OutcomeCreate
		}
		res.Rows = append(res.Rows, row)
	}
	res.Rows = append(res.Rows, brokenRows...)
	for _, c := range ownedActive {
		if !wanted[keys[c.ID]] {
			res.Rows = append(res.Rows, Row{Outcome: OutcomeStale, SourceKey: keys[c.ID], Name: c.Name, CheckID: c.ID})
		}
	}
	return res
}
