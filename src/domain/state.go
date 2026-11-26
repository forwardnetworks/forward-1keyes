package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// MaxStateBytes is the largest state file that is read.
const MaxStateBytes = 64 * 1024

// State is what changes --state remembers: the last snapshot a run finished
// with, for one Forward network and one ThousandEyes account group. It holds
// nothing secret.
type State struct {
	NetworkID      string `json:"network_id"`
	AccountGroupID string `json:"account_group_id"`
	SnapshotID     string `json:"snapshot_id"`
	WrittenAt      string `json:"written_at"`
	ToolVersion    string `json:"tool_version"`
}

// NewState is the record a run that finished with snapshot leaves.
func NewState(networkID, accountGroupID, snapshot, toolVersion string, now time.Time) State {
	return State{NetworkID: networkID, AccountGroupID: accountGroupID, SnapshotID: snapshot,
		WrittenAt: now.UTC().Format(time.RFC3339), ToolVersion: toolVersion}
}

// Marshal is the bytes of the state file.
func (s State) Marshal() ([]byte, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ParseState reads a state file. A file that is not valid, has an unknown
// field, has data after the record, lacks a field or is too large is refused;
// none of these is "no state".
func ParseState(raw []byte) (State, error) {
	if len(raw) > MaxStateBytes {
		return State{}, fmt.Errorf("the file is larger than %d bytes", MaxStateBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s State
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return State{}, errors.New("the file is empty")
		}
		return State{}, fmt.Errorf("it is not a state record: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return State{}, errors.New("there is data after the record")
	}
	for name, v := range map[string]string{"network_id": s.NetworkID, "account_group_id": s.AccountGroupID,
		"snapshot_id": s.SnapshotID, "written_at": s.WrittenAt, "tool_version": s.ToolVersion} {
		if v == "" {
			return State{}, fmt.Errorf("the field %s is missing or empty", name)
		}
	}
	if _, err := time.Parse(time.RFC3339, s.WrittenAt); err != nil {
		return State{}, fmt.Errorf("written_at is not a time: %w", err)
	}
	return s, nil
}

// Bind refuses a state file made for another network or account group.
func (s State) Bind(networkID, accountGroupID string) error {
	if s.NetworkID != networkID {
		return fmt.Errorf("the state file is for network %q, not %q", s.NetworkID, networkID)
	}
	if s.AccountGroupID != accountGroupID {
		return fmt.Errorf("the state file is for account group %q, not %q", s.AccountGroupID, accountGroupID)
	}
	return nil
}

// ChooseFrom picks the older snapshot of the comparison from the processed
// snapshots, oldest first. When the remembered snapshot is the latest, nothing
// is new. A remembered snapshot that is not processed is an error: no other
// start is guessed.
func (s State) ChooseFrom(processed []string) (from string, nothingNew bool, err error) {
	i := slices.Index(processed, s.SnapshotID)
	if i < 0 {
		return "", false, fmt.Errorf("the snapshot %s in the state file is not a processed snapshot of this network (it was deleted, or never processed); delete the state file to start over", s.SnapshotID)
	}
	if i == len(processed)-1 {
		return "", true, nil
	}
	return s.SnapshotID, false, nil
}
