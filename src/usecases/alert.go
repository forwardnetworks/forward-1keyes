package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// AnswerAlert is report for one test. The whole selection is planned, so that
// a check shared by several tests keeps its key; then only the alerted test's
// rows are kept and each is judged on that test's measurements alone. It only
// reads, and any read that fails ends it.
func AnswerAlert(ctx context.Context, deps PlanDeps, p ReportParams, alert domain.Alert, received time.Time) (domain.AlertAnswer, error) {
	none := domain.AlertAnswer{}
	results, ok := deps.TE.(ports.ResultsReader)
	if !ok {
		return none, errors.New("listen: the ThousandEyes client cannot read results")
	}
	forward, ok := deps.Forward.(ports.CheckResultReader)
	if !ok {
		return none, errors.New("listen: the Forward client cannot read check results")
	}
	body, err := Plan(ctx, deps, p.Plan)
	if err != nil {
		return none, err
	}
	snapshot := body.Forward.SnapshotID
	processed, err := forward.SnapshotProcessedAt(ctx, body.Forward.NetworkID, snapshot)
	if err != nil {
		return none, fmt.Errorf("reading when snapshot %s was processed: %w", snapshot, err)
	}
	types := map[string]string{}
	for _, t := range body.Tests {
		types[t.ID] = t.Type
	}

	var rows []domain.AlertRow
	var checks map[string]domain.CheckResult
	var records []domain.NetworkRecord
	read := false
	for _, row := range body.Rows {
		if !row.Lists(alert.TestID) {
			continue
		}
		narrowed, others := domain.AlertTestRow(row, alert.TestID)
		// The reason comes from the whole row: a sharing test decides, for
		// example, that a check is agent-to-agent.
		if reason := domain.NotComparedReason(row, types); reason != "" {
			rows = append(rows, domain.AlertRow{SharedWithTestIDs: others, ReportRow: domain.ReportRow{CheckID: row.CheckID, Name: row.Name,
				PlanOutcome: row.Outcome, Reason: reason, TestIDs: narrowed.TestIDs, AgentIDs: row.AgentIDs, Records: []domain.ReportRecord{}}})
			continue
		}
		if !read {
			if checks, err = forward.CheckResults(ctx, snapshot); err != nil {
				return none, fmt.Errorf("reading check results on snapshot %s: %w", snapshot, err)
			}
			if records, err = results.NetworkResults(ctx, body.TE.AID, alert.TestID, p.Window); err != nil {
				return none, fmt.Errorf("reading the results of test %s: %w", alert.TestID, err)
			}
			read = true
		}
		result, ok := checks[row.CheckID]
		if !ok {
			return none, fmt.Errorf("check %s has no result on snapshot %s", row.CheckID, snapshot)
		}
		compared, err := domain.Compare(narrowed, map[string][]domain.NetworkRecord{alert.TestID: records}, result, processed)
		if err != nil {
			return none, err
		}
		rows = append(rows, domain.AlertRow{ReportRow: compared, SharedWithTestIDs: others})
	}
	return domain.NewAlertAnswer(alert, received, snapshot, processed, rows), nil
}
