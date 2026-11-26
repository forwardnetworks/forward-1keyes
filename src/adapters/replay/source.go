// Package replay is the replay adapter: it serves the ThousandEyes and the
// resolver ports from a recording, and nothing else. It makes no request, reads
// no credential and looks no name up. It imports the ports only.
package replay

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Source implements ports.TeClient, ports.ResultsReader and ports.Resolver
// from one recording.
type Source struct{ rec ports.Recording }

var (
	_ ports.TeClient      = (*Source)(nil)
	_ ports.ResultsReader = (*Source)(nil)
	_ ports.Resolver      = (*Source)(nil)
)

// New serves a recording that has been checked by the domain.
func New(rec ports.Recording) *Source { return &Source{rec: rec} }

func (s *Source) forGroup(aid string) error {
	if aid != s.rec.AccountGroup.AID {
		return fmt.Errorf("replay adapter: the recording holds account group %s, not aid %q", s.rec.AccountGroup.Name, aid)
	}
	return nil
}

// AccountGroups returns the one account group of the recording.
func (s *Source) AccountGroups(context.Context) ([]ports.AccountGroup, error) {
	return []ports.AccountGroup{s.rec.Group()}, nil
}

// ListTests returns the recorded tests.
func (s *Source) ListTests(_ context.Context, aid string) ([]ports.TestSummary, error) {
	if err := s.forGroup(aid); err != nil {
		return nil, err
	}
	return s.rec.TestSummaries(), nil
}

// GetTest returns the recorded configuration of a test.
func (s *Source) GetTest(_ context.Context, aid string, summary ports.TestSummary) (ports.TestConfig, error) {
	if err := s.forGroup(aid); err != nil {
		return ports.TestConfig{}, err
	}
	cfg, ok := s.rec.TestConfig(summary.ID)
	if !ok || cfg.Type != summary.Type {
		return ports.TestConfig{}, fmt.Errorf("replay adapter: the recording holds no %s test %s", summary.Type, summary.ID)
	}
	return cfg, nil
}

// ListAgents returns the recorded agents.
func (s *Source) ListAgents(_ context.Context, aid string) ([]ports.AgentInfo, error) {
	if err := s.forGroup(aid); err != nil {
		return nil, err
	}
	return s.rec.AgentInfos(), nil
}

// NetworkResults returns the recorded results of a test no older than window
// before the time of the recording.
func (s *Source) NetworkResults(_ context.Context, aid, testID string, window time.Duration) ([]ports.NetworkRecord, error) {
	if err := s.forGroup(aid); err != nil {
		return nil, err
	}
	out, err := s.rec.NetworkRecords(testID, window)
	if err != nil {
		return nil, fmt.Errorf("replay adapter: %w", err)
	}
	return out, nil
}

// Lookup returns the recorded answer for a name, never a live one.
func (s *Source) Lookup(_ context.Context, name string) ([]netip.Addr, error) {
	return s.rec.Lookup(name)
}
