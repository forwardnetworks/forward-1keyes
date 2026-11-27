package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Alert is the little the tool takes from a ThousandEyes alert.
type Alert struct {
	ID        string
	TestID    string
	Triggered bool // false when the alert cleared
}

// AlertRow is one kept row of an answer, judged on the alerted test alone.
type AlertRow struct {
	ReportRow
	SharedWithTestIDs []string `json:"shared_with_test_ids"`
}

// AlertAnswer is the one message about one alert.
type AlertAnswer struct {
	Text                string     `json:"text"`
	AlertID             string     `json:"alert_id"`
	TestID              string     `json:"test_id"`
	Alert               string     `json:"alert"`
	ReceivedAt          string     `json:"received_at"`
	SnapshotID          string     `json:"snapshot_id"`
	SnapshotProcessedAt string     `json:"snapshot_processed_at"`
	CouldNotAnswer      bool       `json:"could_not_answer"`
	Rows                []AlertRow `json:"rows"`
	// Recording is set when the answer was made from a recording.
	Recording *RecordingMark `json:"recording,omitempty"`
}

// The two sentences that read a measurement as being about the alert at hand.
// A replay writes neither: its measurements are from another time.
const (
	liveCaveat  = "ThousandEyes still raised an alert: the cause may be above the network layer, or the fault may have ended before the latest measurement, or began after the snapshot."
	clearedLead = "The alert cleared, but not every check of this test is healthy."
)

// replayStatement keeps a replay's measurements apart from the alert it answers.
const replayStatement = "These measurements were taken when the recording was made; they do not show the state of the network when this alert arrived."

// FromRecording is the answer as a replay sends it: the recording named in the
// first line of the text and in its own member, nothing that reads the recorded
// measurements as being about this alert, and, with rows, a sentence that says
// they are not. The rows and verdicts are untouched.
func (a AlertAnswer) FromRecording(mark RecordingMark) AlertAnswer {
	// A sentence is taken out only where the answer wrote it. It is written
	// before any row, so the first copy is then the tool's own; elsewhere the
	// same words belong to a check and stay.
	text := a.Text
	healthy, triggered := allowed(a.Rows), a.Alert == Alert{Triggered: true}.Kind()
	switch {
	case healthy && triggered:
		text = strings.Replace(text, "\n"+liveCaveat, "", 1)
	case len(a.Rows) > 0 && !healthy && !triggered:
		text = strings.Replace(text, "\n"+clearedLead, "", 1)
	}
	if len(a.Rows) > 0 {
		text += "\n" + replayStatement
	}
	a.Text = mark.Line() + "\n" + text
	a.Recording = &mark
	return a
}

// Errors of listen's input. Neither carries the value it refused.
var (
	errBadAlert         = errors.New("domain: the body is not an alert with testId, alertId and notificationType")
	errBadListenAddress = errors.New("domain: --address must be ip:port with a loopback IP address")
)

var (
	alertTestID = regexp.MustCompile(`^[0-9]{1,20}$`)
	alertID     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// ParseAlert reads the three fields of an alert body, which must be one JSON
// object with each of them a string. Names are matched exactly; every other
// field is ignored.
func ParseAlert(body []byte) (Alert, error) {
	var fields map[string]json.RawMessage
	// Unmarshal refuses anything after the one value.
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return Alert{}, errBadAlert
	}
	text := func(name string) string {
		var s string
		if json.Unmarshal(fields[name], &s) != nil {
			return ""
		}
		return s
	}
	test, id, kind := text("testId"), text("alertId"), text("notificationType")
	if !alertTestID.MatchString(test) || !alertID.MatchString(id) || (kind != "1" && kind != "2") {
		return Alert{}, errBadAlert
	}
	return Alert{ID: id, TestID: test, Triggered: kind == "2"}, nil
}

// ValidateListenAddress accepts ip:port whose ip is a loopback address
// literal, and returns it as it will be listened on. No name is resolved.
func ValidateListenAddress(address string) (string, error) {
	colon := strings.LastIndex(address, ":")
	if colon < 0 {
		return "", errBadListenAddress
	}
	host, port := address[:colon], address[colon+1:]
	if inner, bracketed := strings.CutPrefix(host, "["); bracketed {
		if host, bracketed = strings.CutSuffix(inner, "]"); !bracketed {
			return "", errBadListenAddress
		}
	} else if strings.Contains(host, ":") {
		return "", errBadListenAddress // an IPv6 address needs its brackets
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return "", errBadListenAddress
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", errBadListenAddress
	}
	return netip.AddrPortFrom(ip, uint16(number)).String(), nil
}

// Kind names what the alert reports.
func (a Alert) Kind() string {
	if a.Triggered {
		return "triggered"
	}
	return "cleared"
}

const allowedStatement = "the alerted test's network measurement works and the model allows the flow"

func answerTo(a Alert, received time.Time) AlertAnswer {
	return AlertAnswer{AlertID: a.ID, TestID: a.TestID, Alert: a.Kind(), ReceivedAt: received.UTC().Format(time.RFC3339), Rows: []AlertRow{}}
}

func heading(a Alert) string {
	return fmt.Sprintf("forward-1keyes: ThousandEyes alert %s %s for test %s.", a.ID, a.Kind(), a.TestID)
}

// allowed says whether the one reassuring statement is justified: something
// was kept, and all of it was compared and is healthy.
func allowed(rows []AlertRow) bool {
	return len(rows) > 0 && !slices.ContainsFunc(rows, troubled)
}

func troubled(r AlertRow) bool { return !r.Compared || r.Verdict != verdictHealthy }

// NewAlertAnswer builds the answer to an alert from its kept rows. Every row
// that is not compared and healthy is named with its reason.
func NewAlertAnswer(a Alert, received time.Time, snapshotID string, processed time.Time, rows []AlertRow) AlertAnswer {
	out := answerTo(a, received)
	out.SnapshotID, out.SnapshotProcessedAt = snapshotID, processed.UTC().Format(time.RFC3339)
	out.Rows = append(out.Rows, rows...)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\nForward snapshot %s, processed %s.", heading(a), snapshotID, out.SnapshotProcessedAt)
	switch {
	case len(rows) == 0:
		fmt.Fprintf(&b, "\nFor test %s, no managed check or planned flow is known.", a.TestID)
	case allowed(rows):
		fmt.Fprintf(&b, "\nIn every check of this test, %s.", allowedStatement)
		if a.Triggered {
			b.WriteString("\n" + liveCaveat)
		}
	default:
		if !a.Triggered {
			b.WriteString("\n" + clearedLead)
		}
		for _, r := range rows {
			if !troubled(r) {
				continue
			}
			if r.Compared {
				fmt.Fprintf(&b, "\n%s: %s\n    %s", r.Verdict, r.Name, r.Guidance)
			} else {
				fmt.Fprintf(&b, "\nnot compared: %s\n    %s", r.Name, r.Reason)
			}
		}
	}
	for _, r := range rows {
		if len(r.SharedWithTestIDs) > 0 {
			fmt.Fprintf(&b, "\nThe check %s is also behind tests %s; they are not part of this answer.", r.Name, strings.Join(r.SharedWithTestIDs, ", "))
		}
	}
	out.Text = b.String()
	return out
}

// UnansweredAlert is the message sent when the work failed. It says nothing
// about why: the error may hold a secret.
func UnansweredAlert(a Alert, received time.Time) AlertAnswer {
	out := answerTo(a, received)
	out.CouldNotAnswer = true
	out.Text = heading(a) + "\nThe alert could not be answered. The tool's own log says why."
	return out
}

// Lists says whether the row is one of the test's.
func (r Row) Lists(testID string) bool {
	return slices.Contains(r.TestIDs, testID) || slices.Contains(r.UnimportableTestIDs, testID)
}

// AlertTestRow narrows a shared row to the alerted test, and names the others.
func AlertTestRow(row Row, testID string) (Row, []string) {
	others := []string{}
	for _, id := range row.TestIDs {
		if id != testID {
			others = append(others, id)
		}
	}
	row.TestIDs = []string{testID}
	return row, others
}
