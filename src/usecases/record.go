package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// RecordDeps are the ports record reads: ThousandEyes and the resolver. It has
// no Forward port.
type RecordDeps struct {
	TE       ports.TeClient
	Resolver ports.Resolver
}

// RecordParams are the flags of record.
type RecordParams struct {
	AccountGroup string
	Selection    domain.Selection
	Window       time.Duration
	ToolVersion  string
	RecordedAt   time.Time // taken before the first read
}

// Record reads what plan and report read from ThousandEyes, and the lookups of
// the selected tests' hostnames, and returns them as a recording. Any read that
// fails ends it: a recording is never partial.
func Record(ctx context.Context, deps RecordDeps, p RecordParams) (domain.Recording, error) {
	reader, ok := deps.TE.(ports.ResultsReader)
	if !ok {
		return domain.Recording{}, errors.New("record: the ThousandEyes client cannot read results")
	}
	groups, err := deps.TE.AccountGroups(ctx)
	if err != nil {
		return domain.Recording{}, fmt.Errorf("reading ThousandEyes account groups: %w", err)
	}
	group, err := domain.ResolveAccountGroup(groups, p.AccountGroup)
	if err != nil {
		return domain.Recording{}, err
	}
	configs, err := readTests(ctx, deps.TE, group.AID)
	if err != nil {
		return domain.Recording{}, err
	}
	selected, err := domain.Select(configs, p.Selection)
	if err != nil {
		return domain.Recording{}, err
	}
	agents, err := deps.TE.ListAgents(ctx, group.AID)
	if err != nil {
		return domain.Recording{}, fmt.Errorf("reading ThousandEyes agents: %w", err)
	}
	var results []domain.TestResults
	for _, t := range selected {
		// A disabled test is never compared, so its results are not read.
		if !t.Enabled || (t.Type != "agent-to-server" && t.Type != "http-server") {
			continue
		}
		got, err := reader.NetworkResults(ctx, group.AID, t.ID, p.Window)
		if err != nil {
			return domain.Recording{}, fmt.Errorf("reading the results of test %s: %w", t.ID, err)
		}
		results = append(results, domain.TestResults{TestID: t.ID, Records: got})
	}
	_, lookups := resolveHostnames(ctx, deps.Resolver, selected)
	// Only the selected tests are saved, so an agent that is not online and is
	// named by another test is marked: a replay still has to warn about it.
	held := map[string]bool{}
	for _, t := range selected {
		held[t.ID] = true
	}
	var left []domain.TestConfig
	for _, t := range configs {
		if !held[t.ID] {
			left = append(left, t)
		}
	}
	return domain.NewRecording(domain.RecordingInput{
		ToolVersion: p.ToolVersion, RecordedAt: p.RecordedAt, Window: p.Window, Group: group,
		Tests: selected, Agents: agents, NamedOutside: domain.NamedAgents(left), Results: results, Lookups: lookups,
	})
}
