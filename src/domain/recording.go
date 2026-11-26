package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// A recording is what the tool decoded from ThousandEyes, and the answers of
// the hostname lookups, saved to a file so a later run can use it in place of
// ThousandEyes and the resolver.

// Recording constants.
const (
	// recordingFormat is the format name written to every recording.
	recordingFormat = "forward-1keyes-recording"
	// recordingVersion is the format version this tool writes and reads.
	recordingVersion = 1
	// MaxRecordingBytes is the largest recording the tool reads.
	MaxRecordingBytes = 64 << 20
)

// Recording errors.
var (
	// errBadRecording wraps every reason a recording is refused.
	errBadRecording = errors.New("domain: not a usable recording")
	// errNotRecorded says a recording holds no answer to what was asked.
	errNotRecorded = errors.New("domain: the recording holds no answer to that")
)

// RecordedGroup is the account group a recording was made for.
type RecordedGroup struct {
	Name string `json:"name"`
	AID  string `json:"aid"`
}

// RecordedTest is a test as the client decoded it.
type RecordedTest struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Enabled   bool     `json:"enabled"`
	Labels    []string `json:"labels"`
	Target    string   `json:"target"`
	Protocol  string   `json:"protocol"`
	Port      *int     `json:"port"`
	Direction *string  `json:"direction"`
	UsesProxy bool     `json:"uses_proxy"`
	AgentIDs  []string `json:"agent_ids"`
}

// RecordedAgent is an agent as the client decoded it.
type RecordedAgent struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Addresses []netip.Addr `json:"addresses"`
}

// RecordedRecord is one network result as the client decoded it.
type RecordedRecord struct {
	AgentID    string    `json:"agent_id"`
	ServerIP   string    `json:"server_ip"`
	MeasuredAt time.Time `json:"measured_at"`
	Loss       *float64  `json:"loss"`
	Error      string    `json:"error"`
}

// RecordedResults are the network results of one test.
type RecordedResults struct {
	TestID  string           `json:"test_id"`
	Records []RecordedRecord `json:"records"`
}

// RecordedLookup is the answer of one hostname lookup: its addresses, or the
// error it gave.
type RecordedLookup struct {
	Name      string       `json:"name"`
	Addresses []netip.Addr `json:"addresses"`
	Error     string       `json:"error"`
}

// Recording is the file. Window is how far back the results reach from
// RecordedAt, as a Go duration.
type Recording struct {
	Format        string            `json:"format"`
	FormatVersion int               `json:"format_version"`
	ToolVersion   string            `json:"tool_version"`
	RecordedAt    time.Time         `json:"recorded_at"`
	Window        string            `json:"window"`
	AccountGroup  RecordedGroup     `json:"account_group"`
	Tests         []RecordedTest    `json:"tests"`
	Agents        []RecordedAgent   `json:"agents"`
	Results       []RecordedResults `json:"results"`
	Lookups       []RecordedLookup  `json:"lookups"`
}

// TestResults are the network records of one test, in the order they were read.
type TestResults struct {
	TestID  string
	Records []NetworkRecord
}

// RecordingInput is what NewRecording needs.
type RecordingInput struct {
	ToolVersion string
	RecordedAt  time.Time
	Window      time.Duration
	Group       AccountGroup
	Tests       []TestConfig
	Agents      []AgentInfo
	Results     []TestResults
	Lookups     []Resolution
}

// NewRecording builds a recording and checks it the way a reader will, so a
// file the tool writes is a file the tool reads. The time is cut to whole
// seconds, downwards.
func NewRecording(in RecordingInput) (Recording, error) {
	rec := Recording{
		Format: recordingFormat, FormatVersion: recordingVersion, ToolVersion: in.ToolVersion,
		RecordedAt: in.RecordedAt.UTC().Truncate(time.Second), Window: in.Window.String(),
		AccountGroup: RecordedGroup{Name: in.Group.Name, AID: in.Group.AID},
		Tests:        []RecordedTest{}, Agents: []RecordedAgent{}, Results: []RecordedResults{}, Lookups: []RecordedLookup{},
	}
	for _, t := range in.Tests {
		rec.Tests = append(rec.Tests, RecordedTest{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled, Labels: t.Labels,
			Target: recordedTarget(t.Type, t.Target), Protocol: t.Protocol, Port: t.Port, Direction: t.Direction, UsesProxy: t.UsesProxy, AgentIDs: t.AgentIDs})
	}
	for _, a := range in.Agents {
		rec.Agents = append(rec.Agents, RecordedAgent{ID: a.ID, Kind: string(a.Kind), Addresses: a.Addresses})
	}
	for _, r := range in.Results {
		out := RecordedResults{TestID: r.TestID, Records: []RecordedRecord{}}
		for _, n := range r.Records {
			out.Records = append(out.Records, RecordedRecord{AgentID: n.AgentID, ServerIP: n.ServerIP, MeasuredAt: n.MeasuredAt.UTC(), Loss: n.Loss, Error: n.Error})
		}
		rec.Results = append(rec.Results, out)
	}
	for _, l := range in.Lookups {
		out := RecordedLookup{Name: l.Name, Error: l.Error, Addresses: []netip.Addr{}}
		for _, s := range l.Addresses {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return Recording{}, fmt.Errorf("%w: lookup %q: %v", errBadRecording, l.Name, err)
			}
			out.Addresses = append(out.Addresses, a)
		}
		rec.Lookups = append(rec.Lookups, out)
	}
	if err := rec.validate(); err != nil {
		return Recording{}, err
	}
	return rec, nil
}

// recordedTarget keeps a test's target without anything in it that could be a
// credential: a web test's URL loses its user, query and fragment (only the
// scheme, host and port are used), so the file holds no secret a test URL carried.
func recordedTarget(testType, target string) string {
	if testType != "http-server" {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		// Unparseable: it cannot be mapped to a flow anyway, so keep no query,
		// fragment or user.
		if i := strings.IndexAny(target, "?#"); i >= 0 {
			target = target[:i]
		}
		if s := strings.Index(target, "//"); s >= 0 {
			if at := strings.LastIndex(target[s+2:], "@"); at >= 0 && !strings.Contains(target[s+2:s+2+at], "/") {
				target = target[:s+2] + target[s+2+at+1:]
			}
		}
		return target
	}
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery {
		return target
	}
	u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
	return u.String()
}

// Marshal writes the recording as JSON, with a final newline.
func (r Recording) Marshal() ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRecording, fmt.Sprintf(format, args...))
}

// validate holds what the live reader would hold: ids that name something,
// times that parse, addresses that are addresses, and no two entries for one
// thing.
func (r Recording) validate() error {
	switch {
	case r.Format != recordingFormat:
		return bad("format %q is not %q", r.Format, recordingFormat)
	case r.FormatVersion != recordingVersion:
		return bad("format version %d is not %d", r.FormatVersion, recordingVersion)
	case r.ToolVersion == "":
		return bad("no tool version")
	case r.RecordedAt.IsZero():
		return bad("no recording time")
	case r.AccountGroup.Name == "" || r.AccountGroup.AID == "":
		return bad("no account group")
	}
	if w, err := time.ParseDuration(r.Window); err != nil || w < time.Second {
		return bad("window %q is not a duration of at least one second", r.Window)
	}
	tests := map[string]bool{}
	for _, t := range r.Tests {
		switch {
		case t.ID == "" || t.Type == "":
			return bad("a test has no id or no type")
		case tests[t.ID]:
			return bad("test %s appears twice", t.ID)
		case t.Port != nil && (*t.Port < 1 || *t.Port > 65535):
			return bad("test %s has port %d", t.ID, *t.Port)
		case slices.Contains(t.AgentIDs, ""):
			return bad("test %s has an agent with no id", t.ID)
		}
		tests[t.ID] = true
	}
	agents := map[string]bool{}
	for _, a := range r.Agents {
		if a.ID == "" || a.Kind == "" || agents[a.ID] {
			return bad("an agent has no id or kind, or appears twice")
		}
		if slices.ContainsFunc(a.Addresses, func(ad netip.Addr) bool { return !ad.IsValid() }) {
			return bad("agent %s has an address that is not one", a.ID)
		}
		agents[a.ID] = true
	}
	held := map[string]bool{}
	for _, res := range r.Results {
		if !tests[res.TestID] || held[res.TestID] {
			return bad("results for test %q: no such test, or listed twice", res.TestID)
		}
		held[res.TestID] = true
		for _, n := range res.Records {
			if n.MeasuredAt.IsZero() {
				return bad("a result of test %s has no time", res.TestID)
			}
		}
	}
	names := map[string]bool{}
	for _, l := range r.Lookups {
		if l.Name == "" || names[l.Name] {
			return bad("a lookup has no name, or appears twice")
		}
		names[l.Name] = true
		if slices.ContainsFunc(l.Addresses, func(ad netip.Addr) bool { return !ad.IsValid() }) {
			return bad("lookup %q has an address that is not one", l.Name)
		}
		if l.Error != "" && len(l.Addresses) > 0 {
			return bad("lookup %q has both an error and addresses", l.Name)
		}
	}
	return nil
}

// RecordingMark says that ThousandEyes data is a recording, and which: it is
// carried by every output made from one, and inside a plan made from one.
type RecordingMark struct {
	RecordedAt string `json:"recorded_at"`
	SHA256     string `json:"sha256"`
	Note       string `json:"note"`
}

// Line is the one line of text that tells a reader the data is a recording.
func (m RecordingMark) Line() string {
	return fmt.Sprintf("ThousandEyes data: a recording made at %s (file sha256 %s), not live data", m.RecordedAt, m.SHA256)
}

// ParseRecording reads a recording file. It refuses a file over
// MaxRecordingBytes, an unknown format, version or field, trailing data, and a
// value the live reader would not accept. The mark carries the SHA-256 of the
// file as given.
func ParseRecording(raw []byte) (Recording, RecordingMark, error) {
	if len(raw) > MaxRecordingBytes {
		return Recording{}, RecordingMark{}, bad("the file is larger than %d MiB", MaxRecordingBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec Recording
	if err := dec.Decode(&rec); err != nil {
		return Recording{}, RecordingMark{}, bad("%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Recording{}, RecordingMark{}, bad("data follows the recording")
	}
	if err := rec.validate(); err != nil {
		return Recording{}, RecordingMark{}, err
	}
	sum := sha256.Sum256(raw)
	return rec, RecordingMark{
		RecordedAt: rec.RecordedAt.UTC().Format(time.RFC3339), SHA256: hex.EncodeToString(sum[:]),
		Note: "the ThousandEyes data is a recording, not live",
	}, nil
}

// CheckGroup refuses a recording made for another account group.
func (r Recording) CheckGroup(name string) error {
	if r.AccountGroup.Name != name {
		return fmt.Errorf("the recording was made for account group %q, not %q", r.AccountGroup.Name, name)
	}
	return nil
}

// WindowDuration is the window the recording was made with.
func (r Recording) WindowDuration() time.Duration {
	d, _ := time.ParseDuration(r.Window)
	return d
}

// Group is the account group of the recording.
func (r Recording) Group() AccountGroup {
	return AccountGroup{Name: r.AccountGroup.Name, AID: r.AccountGroup.AID}
}

// TestSummaries lists the recorded tests as the test list does.
func (r Recording) TestSummaries() []TestSummary {
	out := make([]TestSummary, 0, len(r.Tests))
	for _, t := range r.Tests {
		out = append(out, TestSummary{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled})
	}
	return out
}

// TestConfig returns a recorded test as the client decoded it.
func (r Recording) TestConfig(id string) (TestConfig, bool) {
	for _, t := range r.Tests {
		if t.ID == id {
			return TestConfig{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled, Labels: slices.Clone(t.Labels), Target: t.Target,
				Protocol: t.Protocol, Port: t.Port, Direction: t.Direction, UsesProxy: t.UsesProxy, AgentIDs: slices.Clone(t.AgentIDs)}, true
		}
	}
	return TestConfig{}, false
}

// AgentInfos lists the recorded agents.
func (r Recording) AgentInfos() []AgentInfo {
	out := make([]AgentInfo, 0, len(r.Agents))
	for _, a := range r.Agents {
		out = append(out, AgentInfo{ID: a.ID, Kind: AgentKind(a.Kind), Addresses: slices.Clone(a.Addresses)})
	}
	return out
}

// NetworkRecords returns the recorded results of a test no older than window
// before the time of the recording. A window longer than the recorded one, and
// a test with no recorded results, are errors: neither is read as no data.
func (r Recording) NetworkRecords(testID string, window time.Duration) ([]NetworkRecord, error) {
	if window > r.WindowDuration() {
		return nil, fmt.Errorf("window %s is longer than the %s the recording holds", window, r.WindowDuration())
	}
	cut := r.RecordedAt.Add(-window)
	for _, res := range r.Results {
		if res.TestID != testID {
			continue
		}
		out := []NetworkRecord{}
		for _, n := range res.Records {
			if !n.MeasuredAt.Before(cut) {
				out = append(out, NetworkRecord{AgentID: n.AgentID, ServerIP: n.ServerIP, MeasuredAt: n.MeasuredAt, Loss: n.Loss, Error: n.Error})
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: results of test %s", errNotRecorded, testID)
}

// Lookup returns the recorded answer of a hostname lookup: its addresses, or
// the error the lookup gave. A name that was not looked up is an error too.
func (r Recording) Lookup(name string) ([]netip.Addr, error) {
	for _, l := range r.Lookups {
		if l.Name == name {
			if l.Error != "" {
				return nil, errors.New(l.Error)
			}
			return slices.Clone(l.Addresses), nil
		}
	}
	return nil, fmt.Errorf("%w: lookup of %q", errNotRecorded, strings.TrimSpace(name))
}
