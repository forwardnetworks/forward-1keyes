package domain_test

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Tests for the recording file: it is untrusted input and is refused when it
// holds a value the live reader would reject, and no credential reaches it.

func goodRecording(t *testing.T, target string) []byte {
	t.Helper()
	rec, err := domain.NewRecording(domain.RecordingInput{
		ToolVersion: "0.0.0", RecordedAt: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), Window: time.Hour,
		Group:   domain.AccountGroup{Name: "Lab", AID: "1"},
		Tests:   []domain.TestConfig{{ID: "1", Name: "t", Type: "http-server", Enabled: true, Target: target, Protocol: "tcp", AgentIDs: []string{"a1"}}},
		Agents:  []domain.AgentInfo{{ID: "a1", Kind: domain.AgentKind("enterprise"), Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}},
		Lookups: []domain.Resolution{{Name: "h.example", Addresses: []string{"10.0.0.2"}}},
	})
	if err != nil {
		t.Fatalf("a valid recording was refused: %v", err)
	}
	raw, err := rec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRecordingRefusesWhatLiveReaderWouldReject(t *testing.T) {
	base := goodRecording(t, "https://h.example/")
	if _, _, err := domain.ParseRecording(base); err != nil {
		t.Fatalf("the unmodified recording is refused: %v", err)
	}
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(base, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, _ := json.Marshal(m)
		return out
	}
	cases := map[string][]byte{
		"empty agent address":                     mutate(func(m map[string]any) { m["agents"].([]any)[0].(map[string]any)["addresses"] = []any{""} }),
		"null agent address":                      mutate(func(m map[string]any) { m["agents"].([]any)[0].(map[string]any)["addresses"] = []any{nil} }),
		"empty lookup address":                    mutate(func(m map[string]any) { m["lookups"].([]any)[0].(map[string]any)["addresses"] = []any{""} }),
		"null lookup address":                     mutate(func(m map[string]any) { m["lookups"].([]any)[0].(map[string]any)["addresses"] = []any{nil} }),
		"lookup with both an error and addresses": mutate(func(m map[string]any) { m["lookups"].([]any)[0].(map[string]any)["error"] = "no such host" }),
	}
	for name, raw := range cases {
		if _, _, err := domain.ParseRecording(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRecordingKeepsACleanTestURLUnchanged(t *testing.T) {
	const clean = "https://h.example:8443"
	if !strings.Contains(string(goodRecording(t, clean)), clean) {
		t.Errorf("a URL with nothing to remove was changed")
	}
}

func TestRecordingKeepsNoCredentialFromAnUnparseableTestURL(t *testing.T) {
	raw := goodRecording(t, "https://u:PW-SENTINEL@h.example/a%zz?token=Q-SENTINEL")
	for _, s := range []string{"PW-SENTINEL", "Q-SENTINEL"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("the recording holds %q", s)
		}
	}
}

func TestRecordingKeepsNoPathFromATestURL(t *testing.T) {
	raw := goodRecording(t, "https://h.example:8443/hook/PATH-SENTINEL/x")
	if strings.Contains(string(raw), "PATH-SENTINEL") {
		t.Errorf("the recording holds the URL path")
	}
	if !strings.Contains(string(raw), "https://h.example:8443") {
		t.Errorf("the scheme, host and port of the test URL were lost")
	}
}

func TestRecordingKeepsNoCredentialFromATestURL(t *testing.T) {
	raw := goodRecording(t, "https://user:PASSWORD-SENTINEL@h.example/path?token=QUERY-SENTINEL#frag")
	for _, s := range []string{"PASSWORD-SENTINEL", "QUERY-SENTINEL", "user:"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("the recording holds %q", s)
		}
	}
	if !strings.Contains(string(raw), "h.example") {
		t.Errorf("the host of the test URL was lost")
	}
}
