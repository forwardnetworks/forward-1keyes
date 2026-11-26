package domain_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// A test URL can carry a user, a password or a token in its query. A reason
// that a plan, a report or a notification prints must not repeat them, and
// must read the same for live data and for a recording.
func TestNotImportableReasonKeepsNoCredentialFromATestURL(t *testing.T) {
	for _, target := range []string{
		"https://u:PW-SENTINEL@h.example:99999/p?token=Q-SENTINEL",
		"ftp://u:PW-SENTINEL@h.example/p?token=Q-SENTINEL",
		"://u:PW-SENTINEL@h.example/p?token=Q-SENTINEL",
	} {
		res := domain.MapFlows(domain.TestConfig{ID: "1", Type: "http-server", Enabled: true, Target: target, Protocol: "tcp"}, nil)
		if res.NotImportable == nil {
			t.Fatalf("%s: expected a not-importable result, got %+v", target, res)
		}
		out := fmt.Sprintf("%s %s", res.NotImportable.Kind, res.NotImportable.Reason)
		for _, s := range []string{"PW-SENTINEL", "Q-SENTINEL"} {
			if strings.Contains(out, s) {
				t.Errorf("%s: the result repeats %q: %s", target, s, out)
			}
		}
	}
}
