package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// ReportParams are the flags of report.
type ReportParams struct {
	Plan   PlanParams
	Window time.Duration
}

// Report puts what ThousandEyes measured beside what Forward concluded, for
// each check the plan pipeline can tie to its tests. It only reads. Any read
// that fails ends the report: a failed read is never reported as no data.
func Report(ctx context.Context, deps PlanDeps, p ReportParams) (domain.Report, error) {
	results, ok := deps.TE.(ports.ResultsReader)
	if !ok {
		return domain.Report{}, errors.New("report: the ThousandEyes client cannot read results")
	}
	forward, ok := deps.Forward.(ports.CheckResultReader)
	if !ok {
		return domain.Report{}, errors.New("report: the Forward client cannot read check results")
	}
	body, err := Plan(ctx, deps, p.Plan)
	if err != nil {
		return domain.Report{}, err
	}
	snapshot := body.Forward.SnapshotID
	checks, err := forward.CheckResults(ctx, snapshot)
	if err != nil {
		return domain.Report{}, fmt.Errorf("reading check results on snapshot %s: %w", snapshot, err)
	}
	processed, err := forward.SnapshotProcessedAt(ctx, body.Forward.NetworkID, snapshot)
	if err != nil {
		return domain.Report{}, fmt.Errorf("reading when snapshot %s was processed: %w", snapshot, err)
	}
	types := map[string]string{}
	for _, t := range body.Tests {
		types[t.ID] = t.Type
	}

	out := domain.Report{SnapshotID: snapshot, Rows: []domain.ReportRow{}}
	records := map[string][]domain.NetworkRecord{} // test ID -> records, read once
	for _, row := range body.Rows {
		if reason := domain.NotComparedReason(row, types); reason != "" {
			out.Rows = append(out.Rows, domain.ReportRow{CheckID: row.CheckID, Name: row.Name, PlanOutcome: row.Outcome,
				Reason: reason, TestIDs: row.TestIDs, AgentIDs: row.AgentIDs, Records: []domain.ReportRecord{}})
			continue
		}
		for _, id := range row.TestIDs {
			if _, done := records[id]; done {
				continue
			}
			got, err := results.NetworkResults(ctx, body.TE.AID, id, p.Window)
			if err != nil {
				return domain.Report{}, fmt.Errorf("reading the results of test %s: %w", id, err)
			}
			records[id] = got
		}
		result, ok := checks[row.CheckID]
		if !ok {
			return domain.Report{}, fmt.Errorf("check %s has no result on snapshot %s", row.CheckID, snapshot)
		}
		compared, err := domain.Compare(row, records, result, processed)
		if err != nil {
			return domain.Report{}, err
		}
		out.Rows = append(out.Rows, compared)
	}
	return out, nil
}
