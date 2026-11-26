// Package usecases holds the tool's use cases: plan, apply and status. It
// imports the domain and the ports, never an adapter.
package usecases

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Row statuses in an apply report.
const (
	statusCreated = "created"
	statusUpdated = "updated"
	statusSkipped = "skipped"
	statusFailed  = "failed"
)

// ApplyOptions are the flags of apply.
type ApplyOptions struct {
	Digest      string // --digest
	ToolVersion string // the running tool's version
	BatchSize   int    // default 25
	// Replay is the recording --te-replay named, or nil.
	Replay *domain.RecordingMark
}

// Difference is one way the world no longer matches the plan.
type Difference struct {
	Kind    string // "snapshot", "check"
	ID      string
	Field   string
	Planned string
	Now     string
}

// RowResult is what apply did with one row.
type RowResult struct {
	SourceKey string
	Name      string
	Status    string
	Reason    string
	CheckIDs  []string
}

// ApplyReport is the outcome of apply.
type ApplyReport struct {
	Results     []RowResult
	Differences []Difference
	Warnings    []string              // what changed in ThousandEyes that does not stop the apply
	Duplicates  map[string][]string   // source key -> check IDs, when a key ends up held twice
	Recording   *domain.RecordingMark `json:"recording,omitempty"` // set when the checks come from a recording
	Refusal     string                // why nothing was written, when ExitCode is 3
	ExitCode    int                   // 0 all rows done, 1 any failed or skipped, 3 refused
}

// ApplyDeps are the ports apply runs on. There is no resolver: apply uses the
// addresses the plan pinned and never looks a name up.
type ApplyDeps struct {
	Forward ports.ForwardClient
	TE      ports.TeClient
}

// ApplyWithTE is Apply that also re-reads ThousandEyes before it writes.
func ApplyWithTE(ctx context.Context, deps ApplyDeps, raw []byte, opts ApplyOptions) ApplyReport {
	return apply(ctx, deps.Forward, deps.TE, raw, opts)
}

const defaultBatchSize = 25

// Apply writes an approved plan: verify the file and digest, check the plan's
// contents, re-read Forward, write in batches, then check each written key.
// Nothing is sent to Forward until the digest, the plan's contents and the
// re-read of Forward all agree with the plan.
func Apply(ctx context.Context, fwd ports.ForwardClient, raw []byte, opts ApplyOptions) ApplyReport {
	return apply(ctx, fwd, nil, raw, opts)
}

// apply is Apply with an optional ThousandEyes re-read.
func apply(ctx context.Context, fwd ports.ForwardClient, te ports.TeClient, raw []byte, opts ApplyOptions) ApplyReport {
	file, err := domain.VerifyPlan(raw, opts.Digest)
	if err != nil {
		return refuse(err.Error())
	}
	body := file.Body
	if body.ContractVersion != domain.CurrentContractVersion {
		return refuse(fmt.Sprintf("plan contract version %d, this tool writes %d", body.ContractVersion, domain.CurrentContractVersion))
	}
	if body.ToolVersion != opts.ToolVersion {
		return refuse(fmt.Sprintf("plan was made by tool version %q, this is %q", body.ToolVersion, opts.ToolVersion))
	}

	if why := sourceRefusal(body.Source, opts.Replay); why != "" {
		return refuse(why)
	}
	rep := writePlan(ctx, fwd, te, body, opts)
	rep.Recording = body.Source
	return rep
}

// sourceRefusal says why a plan and the recording given to apply do not go
// together: a plan made from a recording is applied only beside that file, and
// a plan made from live data never beside one.
func sourceRefusal(source, replay *domain.RecordingMark) string {
	switch {
	case source == nil && replay == nil:
		return ""
	case source == nil:
		return "the plan was made from live ThousandEyes data: --te-replay is not allowed"
	case replay == nil:
		return "the plan was made from a recording (file sha256 " + source.SHA256 + "): apply needs --te-replay naming that file"
	case source.SHA256 != replay.SHA256:
		return "the plan was made from the recording with sha256 " + source.SHA256 + ", not the one given (" + replay.SHA256 + ")"
	}
	return ""
}

// writePlan is what apply does once the plan and its source are accepted.
func writePlan(ctx context.Context, fwd ports.ForwardClient, te ports.TeClient, body domain.PlanBody, opts ApplyOptions) ApplyReport {
	var warnings []string
	if te != nil {
		diffs, warns, err := teDrift(ctx, te, body)
		if err != nil {
			return ApplyReport{ExitCode: 1, Refusal: "cannot re-read ThousandEyes: " + err.Error()}
		}
		if len(diffs) > 0 {
			return ApplyReport{ExitCode: 3, Refusal: "ThousandEyes no longer matches the plan", Differences: diffs, Warnings: warns}
		}
		warnings = warns
	}

	snapshot := body.Forward.SnapshotID
	instance := "te-aid-" + body.TE.AID
	latest, err := fwd.LatestSnapshot(ctx, body.Forward.NetworkID)
	if err != nil {
		return ApplyReport{ExitCode: 1, Refusal: "cannot read the latest snapshot: " + err.Error()}
	}
	current, err := fwd.ListChecks(ctx, snapshot)
	if err != nil {
		return ApplyReport{ExitCode: 1, Refusal: "cannot list checks: " + err.Error()}
	}
	if diffs := drift(body, instance, latest, current); len(diffs) > 0 {
		return ApplyReport{ExitCode: 3, Refusal: "the world no longer matches the plan", Differences: diffs}
	}

	var rows []domain.Row
	for _, r := range body.Rows {
		if r.Outcome == domain.OutcomeCreate || r.Outcome == domain.OutcomeUpdate {
			rows = append(rows, r)
		}
	}
	known := map[string]bool{}
	for _, o := range body.Observed {
		known[o.ID] = true
	}
	observed := map[string]domain.ObservedRecord{}
	for _, o := range body.Observed {
		observed[o.ID] = o
	}
	size := opts.BatchSize
	if size < 1 {
		size = defaultBatchSize
	}

	rep := ApplyReport{Warnings: warnings}
	skipFrom := -1
	for start := 0; start < len(rows) && skipFrom < 0; start += size {
		end := min(start+size, len(rows))
		now, err := fwd.ListChecks(ctx, snapshot)
		if err != nil {
			skipFrom = start
			rep.Refusal = "cannot list checks before a batch: " + err.Error()
			break
		}
		if i := concurrentChange(rows[start:end], now, known, instance); i >= 0 {
			skipFrom = start + i
			break
		}
		for _, r := range rows[start:end] {
			res := writeRow(ctx, fwd, snapshot, body, r, observed)
			if len(res.CheckIDs) > 0 && res.Status == statusCreated {
				known[res.CheckIDs[0]] = true // made by this run: never a concurrent change
			}
			rep.Results = append(rep.Results, res)
		}
	}
	if skipFrom >= 0 {
		for _, r := range rows[len(rep.Results):] {
			rep.Results = append(rep.Results, RowResult{SourceKey: r.SourceKey, Name: r.Name, Status: statusSkipped, Reason: "concurrent change"})
		}
	}

	rep.Duplicates = duplicates(ctx, fwd, snapshot, instance, rep.Results)
	rep.ExitCode = 0
	for _, res := range rep.Results {
		if res.Status != statusCreated && res.Status != statusUpdated {
			rep.ExitCode = 1
		}
	}
	if len(rep.Duplicates) > 0 {
		rep.ExitCode = 1
	}
	return rep
}

func refuse(why string) ApplyReport { return ApplyReport{ExitCode: 3, Refusal: why} }

// drift lists every way Forward no longer matches what the plan observed.
func drift(body domain.PlanBody, instance, latest string, current []domain.ObservedRecord) []Difference {
	var diffs []Difference
	if latest != body.Forward.SnapshotID {
		diffs = append(diffs, Difference{Kind: "snapshot", ID: body.Forward.NetworkID, Field: "snapshot_id", Planned: body.Forward.SnapshotID, Now: latest})
	}
	byID := map[string]domain.ObservedRecord{}
	for _, c := range current {
		byID[c.ID] = c
	}
	planned := map[string]bool{}
	for _, o := range body.Observed {
		planned[o.ID] = true
		now, ok := byID[o.ID]
		if !ok {
			diffs = append(diffs, Difference{Kind: "check", ID: o.ID, Field: "missing", Planned: "present", Now: "absent"})
			continue
		}
		add := func(field, was, is string) {
			if was != is {
				diffs = append(diffs, Difference{Kind: "check", ID: o.ID, Field: field, Planned: was, Now: is})
			}
		}
		add("name", o.Name, now.Name)
		add("note", o.Note, now.Note)
		add("tags", joinSorted(o.Tags), joinSorted(now.Tags))
		add("definition_hash", o.DefinitionHash, now.DefinitionHash)
		add("active", fmt.Sprint(o.Active), fmt.Sprint(now.Active))
	}
	names := map[string]bool{}
	for _, r := range body.Rows {
		if r.Outcome == domain.OutcomeCreate || r.Outcome == domain.OutcomeUpdate || r.Outcome == domain.OutcomeUnchanged {
			names[r.Name] = true
		}
	}
	for _, c := range current {
		if planned[c.ID] || !c.Active {
			continue
		}
		dec := domain.DecodeTags(c.Tags)
		if names[c.Name] || (dec.Ownership == domain.Ours && dec.SourceInstance == instance) {
			diffs = append(diffs, Difference{Kind: "check", ID: c.ID, Field: "new", Planned: "absent", Now: "present"})
		}
	}
	return diffs
}

func joinSorted(tags []string) string {
	s := slices.Clone(tags)
	slices.Sort(s)
	return strings.Join(s, ",")
}

// concurrentChange returns the index in batch of the first row whose name or
// key is now held by an active check that neither the plan nor this run made,
// or -1.
func concurrentChange(batch []domain.Row, now []domain.ObservedRecord, known map[string]bool, instance string) int {
	for i, r := range batch {
		for _, c := range now {
			if !c.Active || known[c.ID] {
				continue
			}
			dec := domain.DecodeTags(c.Tags)
			if c.Name == r.Name || (dec.Ownership == domain.Ours && dec.SourceInstance == instance && dec.SourceKey == r.SourceKey) {
				return i
			}
		}
	}
	return -1
}

func writeRow(ctx context.Context, fwd ports.ForwardClient, snapshot string, body domain.PlanBody, r domain.Row, observed map[string]domain.ObservedRecord) RowResult {
	res := RowResult{SourceKey: r.SourceKey, Name: r.Name}
	fail := func(why string) RowResult {
		res.Status, res.Reason = statusFailed, why
		return res
	}
	if r.Spec == nil {
		return fail("the plan row carries no check to write")
	}
	switch r.Outcome {
	case domain.OutcomeCreate:
		id, err := fwd.CreateCheck(ctx, snapshot, *r.Spec, body.Persistent)
		if err != nil {
			return fail("create: " + err.Error())
		}
		res.CheckIDs = []string{id}
		if why := verify(ctx, fwd, snapshot, id, *r.Spec); why != "" {
			return fail(why)
		}
		res.Status = statusCreated
	case domain.OutcomeUpdate:
		if body.UpdateStrategy != "U1" {
			res.Status, res.Reason = statusSkipped, "update not supported"
			return res
		}
		res.CheckIDs = []string{r.CheckID}
		before, err := fwd.GetCheck(ctx, snapshot, r.CheckID)
		if err != nil {
			return fail("re-read: " + err.Error())
		}
		if want, ok := observed[r.CheckID]; !ok || before.ContentHash() != want.ContentHash() || before.Active != want.Active {
			res.Status, res.Reason = statusSkipped, "changed since plan"
			return res
		}
		if err := fwd.UpdateCheck(ctx, snapshot, r.CheckID, *r.Spec); err != nil {
			return fail("update: " + err.Error())
		}
		if why := verify(ctx, fwd, snapshot, r.CheckID, *r.Spec); why != "" {
			return fail(why)
		}
		res.Status = statusUpdated
	}
	return res
}

// verify reads a check back and compares name, tags, note and definition hash
// with what was written.
func verify(ctx context.Context, fwd ports.ForwardClient, snapshot, id string, spec domain.CheckSpec) string {
	got, err := fwd.GetCheck(ctx, snapshot, id)
	if err != nil {
		return "read-back: " + err.Error()
	}
	switch {
	case got.Name != spec.Name:
		return "read-back differs: name"
	case got.Note != spec.Note:
		return "read-back differs: note"
	case joinSorted(got.Tags) != joinSorted(spec.Tags):
		return "read-back differs: tags"
	case got.DefinitionHash != spec.DefinitionHash():
		return "read-back differs: definition"
	}
	return ""
}

// duplicates lists the written keys that more than one active check of ours
// now holds.
func duplicates(ctx context.Context, fwd ports.ForwardClient, snapshot, instance string, results []RowResult) map[string][]string {
	written := map[string]bool{}
	for _, r := range results {
		if r.Status == statusCreated || r.Status == statusUpdated {
			written[r.SourceKey] = true
		}
	}
	out := map[string][]string{}
	if len(written) == 0 {
		return out
	}
	now, err := fwd.ListChecks(ctx, snapshot)
	if err != nil {
		return out
	}
	holders := map[string][]string{}
	for _, c := range now {
		dec := domain.DecodeTags(c.Tags)
		if c.Active && dec.Ownership == domain.Ours && dec.SourceInstance == instance && written[dec.SourceKey] {
			holders[dec.SourceKey] = append(holders[dec.SourceKey], c.ID)
		}
	}
	for key, ids := range holders {
		if len(ids) > 1 {
			out[key] = ids
		}
	}
	return out
}

// teDrift re-reads ThousandEyes and compares it with what the plan observed. A
// contributing test or agent that changed, or went missing, is a difference
// that stops apply. A change to a test that contributes to no written row only
// warns. Names are never resolved: the addresses are the ones the plan pinned.
func teDrift(ctx context.Context, te ports.TeClient, body domain.PlanBody) ([]Difference, []string, error) {
	aid := body.TE.AID
	contribTests, contribAgents := map[string]bool{}, map[string]bool{}
	for _, r := range body.Rows {
		if r.Outcome == domain.OutcomeCreate || r.Outcome == domain.OutcomeUpdate {
			for _, id := range r.TestIDs {
				contribTests[id] = true
			}
			for _, id := range r.AgentIDs {
				contribAgents[id] = true
			}
		}
	}
	summaries, err := te.ListTests(ctx, aid)
	if err != nil {
		return nil, nil, err
	}
	now := map[string]domain.TestSummary{}
	for _, s := range summaries {
		now[s.ID] = s
	}
	planned := map[string]domain.ObservedTest{}
	for _, t := range body.Tests {
		planned[t.ID] = t
	}

	var diffs []Difference
	var warnings []string
	for _, id := range sortedKeys(contribTests) {
		ot, recorded := planned[id]
		summary, present := now[id]
		switch {
		case !recorded:
			diffs = append(diffs, Difference{Kind: "test", ID: id, Field: "recorded", Planned: "absent", Now: "present"})
		case !present:
			diffs = append(diffs, Difference{Kind: "test", ID: id, Field: "missing", Planned: "present", Now: "absent"})
		default:
			cfg, err := te.GetTest(ctx, aid, domain.TestSummary{ID: id, Name: summary.Name, Type: ot.Type, Enabled: summary.Enabled})
			if err != nil {
				return nil, nil, err
			}
			if fp := domain.TestFingerprint(cfg); fp != ot.Fingerprint {
				diffs = append(diffs, Difference{Kind: "test", ID: id, Field: "fingerprint", Planned: ot.Fingerprint, Now: fp})
			}
		}
	}
	if len(contribAgents) > 0 {
		list, err := te.ListAgents(ctx, aid)
		if err != nil {
			return nil, nil, err
		}
		agents := map[string]domain.AgentInfo{}
		for _, a := range list {
			agents[a.ID] = a
		}
		recorded := map[string]string{}
		for _, a := range body.Agents {
			recorded[a.ID] = a.Fingerprint
		}
		for _, id := range sortedKeys(contribAgents) {
			a, present := agents[id]
			switch {
			case !present:
				diffs = append(diffs, Difference{Kind: "agent", ID: id, Field: "missing", Planned: "present", Now: "absent"})
			case domain.AgentFingerprint(a) != recorded[id]:
				diffs = append(diffs, Difference{Kind: "agent", ID: id, Field: "fingerprint", Planned: recorded[id], Now: domain.AgentFingerprint(a)})
			}
		}
	}
	for _, ot := range body.Tests {
		if contribTests[ot.ID] {
			continue
		}
		switch s, present := now[ot.ID]; {
		case !present:
			warnings = append(warnings, fmt.Sprintf("test %s (%s) is no longer in ThousandEyes; it contributed to no written check", ot.ID, ot.Name))
		case s.Name != ot.Name || s.Type != ot.Type || s.Enabled != ot.Enabled:
			warnings = append(warnings, fmt.Sprintf("test %s changed in ThousandEyes (name, type or enabled); it contributed to no written check", ot.ID))
		}
	}
	return diffs, warnings, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
