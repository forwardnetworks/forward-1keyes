package notify_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/usecases"
)

type captureNotifier struct{ body []byte }

func (c *captureNotifier) Notify(_ context.Context, b []byte) error { c.body = b; return nil }

// A replay never looks live: the notification names the recording where a
// reader sees it first, and in a member a receiver can read without parsing text.
func TestNotifyOfARecordingSaysSoFirstAndInAMember(t *testing.T) {
	mark := &domain.RecordingMark{RecordedAt: "2026-11-26T10:00:00Z", SHA256: "ab12", Note: "recorded"}
	c := domain.Changes{Network: "N", From: "1", To: "2", Recording: mark,
		Changes: []domain.CheckChange{{Kind: "broke", Name: "c1"}}}
	n := &captureNotifier{}
	if err := usecases.Notify(context.Background(), n, c); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Text      string `json:"text"`
		Recording *struct {
			RecordedAt string `json:"recorded_at"`
		} `json:"recording"`
	}
	if err := json.Unmarshal(n.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Recording == nil || got.Recording.RecordedAt != mark.RecordedAt {
		t.Errorf("no recording member: %s", n.body)
	}
	if first := strings.SplitN(got.Text, "\n", 2)[0]; !strings.Contains(first, "2026-11-26T10:00:00Z") {
		t.Errorf("the first line of the text does not name the recording: %q", got.Text)
	}
}

func TestNotifyOfLiveDataHasNoRecordingMember(t *testing.T) {
	c := domain.Changes{Network: "N", From: "1", To: "2", Changes: []domain.CheckChange{{Kind: "broke", Name: "c1"}}}
	n := &captureNotifier{}
	if err := usecases.Notify(context.Background(), n, c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(n.body), "recording") {
		t.Errorf("a live notification mentions a recording: %s", n.body)
	}
}
