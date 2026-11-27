package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// In a replay the alert arrives now and every measurement is from the past, so
// the message must name the recording first and must not read the measurements
// as being about this alert.
func TestAlertAnswerFromARecordingNeverLooksLive(t *testing.T) {
	healthy := []domain.AlertRow{{ReportRow: domain.ReportRow{Name: "c", Compared: true, Verdict: "healthy"}}}
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	mark := domain.RecordingMark{RecordedAt: "2026-11-01T09:00:00Z", SHA256: "ab12", Note: "recorded"}
	for _, triggered := range []bool{true, false} {
		alert := domain.Alert{ID: "a1", TestID: "7", Triggered: triggered}
		live := domain.NewAlertAnswer(alert, at, "5", at, healthy)
		got := live.FromRecording(mark)
		first, _, _ := strings.Cut(got.Text, "\n")
		if first != mark.Line() || got.Recording == nil || got.Recording.RecordedAt != mark.RecordedAt {
			t.Fatalf("the recording is not named first and in its member: %q %+v", first, got.Recording)
		}
		if !strings.Contains(got.Text, "do not show the state of the network when this alert arrived") {
			t.Errorf("the message does not separate the measurements from the alert:\n%s", got.Text)
		}
		for _, tie := range []string{"ThousandEyes still raised an alert", "The alert cleared, but"} {
			if strings.Contains(got.Text, tie) {
				t.Errorf("a replay ties the recording to the alert with %q:\n%s", tie, got.Text)
			}
		}
		if live.Recording != nil || strings.Contains(live.Text, "recording") {
			t.Errorf("the live answer changed:\n%s", live.Text)
		}
	}
	unanswered := domain.UnansweredAlert(domain.Alert{ID: "a1", TestID: "7", Triggered: true}, at).FromRecording(mark)
	if !strings.HasPrefix(unanswered.Text, mark.Line()+"\n") || strings.Contains(unanswered.Text, "when the recording was made") {
		t.Errorf("an unanswered alert is not marked, or speaks of measurements it has none of:\n%s", unanswered.Text)
	}
}

// Only the sentence the tool wrote itself is left out: a check name that happens
// to hold the same line is shown as it is.
func TestAlertAnswerFromARecordingKeepsARowsOwnText(t *testing.T) {
	const caveat = "ThousandEyes still raised an alert: the cause may be above the network layer, or the fault may have ended before the latest measurement, or began after the snapshot."
	const lead = "The alert cleared, but not every check of this test is healthy."
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	mark := domain.RecordingMark{RecordedAt: "2026-11-01T09:00:00Z", SHA256: "ab12", Note: "recorded"}
	for _, c := range []struct {
		triggered bool
		verdict   string
		odd       string
	}{{false, "healthy", caveat}, {true, "both-blocked", lead}} {
		rows := []domain.AlertRow{{ReportRow: domain.ReportRow{Name: "c\n" + c.odd, Compared: true, Verdict: c.verdict}, SharedWithTestIDs: []string{"9"}}}
		live := domain.NewAlertAnswer(domain.Alert{ID: "a1", TestID: "7", Triggered: c.triggered}, at, "5", at, rows)
		got := live.FromRecording(mark)
		if want, have := strings.Count(live.Text, "\n"+c.odd), strings.Count(got.Text, "\n"+c.odd); want == 0 || have != want {
			t.Errorf("a row's own text was removed: %d of %d left:\n%s", have, want, got.Text)
		}
	}
}
