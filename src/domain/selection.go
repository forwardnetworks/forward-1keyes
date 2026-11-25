package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// ErrUnknownTestID is returned when a requested test ID is not in the account
// group. Its message reads "unknown test ID <id>".
var ErrUnknownTestID = errors.New("domain: unknown test ID")

// TestConfig is the normalised form of a ThousandEyes test: only fields the
// client decodes, with an absent optional field as nil.
type TestConfig struct {
	ID        string
	Name      string
	Type      string // "agent-to-server", "http-server", "agent-to-agent", ...
	Enabled   bool
	Labels    []string
	Target    string // server, URL or target agent ID, by type
	Protocol  string // "tcp", "udp" or "icmp"; empty for http-server
	Port      *int
	Direction *string
	UsesProxy bool
	AgentIDs  []string
}

// Selection narrows the default import set (every test). Label filters match a
// label name exactly; name filters are RE2 patterns matched unanchored.
type Selection struct {
	TestIDs       []string
	IncludeLabels []string
	ExcludeLabels []string
	IncludeNames  []string
	ExcludeNames  []string
}

// Select returns the tests a Selection keeps, in input order. A test is kept
// when
//
//	(no test IDs given OR its ID is listed)
//	AND it has every include label AND its name matches every include pattern
//	AND NOT (it matches any exclude label OR any exclude name)
//
// Every include flag therefore narrows the set; a repeated include
// value never adds a test. Test IDs are alternatives, as are exclude values.
//
// An unknown test ID or an invalid pattern is an error.
func Select(all []TestConfig, sel Selection) ([]TestConfig, error) {
	known := make(map[string]bool, len(all))
	for _, t := range all {
		known[t.ID] = true
	}
	for _, id := range sel.TestIDs {
		if !known[id] {
			return nil, fmt.Errorf("%w %s", ErrUnknownTestID, id)
		}
	}
	includeNames, err := compilePatterns(sel.IncludeNames)
	if err != nil {
		return nil, err
	}
	excludeNames, err := compilePatterns(sel.ExcludeNames)
	if err != nil {
		return nil, err
	}

	out := []TestConfig{}
	for _, t := range all {
		if len(sel.TestIDs) > 0 && !slices.Contains(sel.TestIDs, t.ID) {
			continue
		}
		if !hasEveryLabel(t, sel.IncludeLabels) || !matchesEvery(t.Name, includeNames) {
			continue
		}
		if hasAnyLabel(t, sel.ExcludeLabels) || matchesAny(t.Name, excludeNames) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("domain: invalid name pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func hasEveryLabel(t TestConfig, labels []string) bool {
	for _, l := range labels {
		if !slices.Contains(t.Labels, l) {
			return false
		}
	}
	return true
}

func matchesEvery(name string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if !re.MatchString(name) {
			return false
		}
	}
	return true
}

func hasAnyLabel(t TestConfig, labels []string) bool {
	for _, l := range labels {
		if slices.Contains(t.Labels, l) {
			return true
		}
	}
	return false
}

func matchesAny(name string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}
