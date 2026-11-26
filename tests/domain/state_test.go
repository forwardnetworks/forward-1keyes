package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Tests for the state file that `changes --state` keeps: it remembers the last
// snapshot a finished run ended on, for one network and one account group, holds
// nothing secret, and is refused when it is anything but exactly that.

var stateTime = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func TestStateRoundTrip(t *testing.T) {
	s := domain.NewState("net1", "ag1", "102", "0.1.0", stateTime)
	raw, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := domain.ParseState(raw)
	if err != nil {
		t.Fatalf("a state file the tool wrote was refused: %v", err)
	}
	if got != s {
		t.Fatalf("got %+v, want %+v", got, s)
	}
}

func TestStateIsRefusedWhenItIsNotExactlyOurs(t *testing.T) {
	good, _ := domain.NewState("net1", "ag1", "102", "0.1.0", stateTime).Marshal()
	cases := map[string]string{
		"unknown field":    strings.Replace(string(good), "{", `{"extra":"x",`, 1),
		"trailing data":    string(good) + "{}",
		"not json":         "snapshot 102",
		"empty":            "",
		"missing snapshot": strings.Replace(string(good), `"snapshot_id": "102"`, `"snapshot_id": ""`, 1),
		"oversized":        string(good) + strings.Repeat(" ", domain.MaxStateBytes),
	}
	if _, err := domain.ParseState(good); err != nil {
		t.Fatalf("the unmodified file is refused: %v", err)
	}
	for name, raw := range cases {
		if _, err := domain.ParseState([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStateHoldsNothingSecret(t *testing.T) {
	raw, _ := domain.NewState("net1", "ag1", "102", "0.1.0", stateTime).Marshal()
	for _, bad := range []string{"token", "pass", "secret", "http", "bearer"} {
		if strings.Contains(strings.ToLower(string(raw)), bad) {
			t.Errorf("the state file mentions %q: %s", bad, raw)
		}
	}
}

func TestStateIsBoundToOneNetworkAndAccountGroup(t *testing.T) {
	s := domain.NewState("net1", "ag1", "102", "0.1.0", stateTime)
	if err := s.Bind("net1", "ag1"); err != nil {
		t.Fatalf("its own network and account group were refused: %v", err)
	}
	if s.Bind("net2", "ag1") == nil || s.Bind("net1", "ag2") == nil {
		t.Fatal("a state file for another network or account group was accepted")
	}
}

func TestStateChoosesWhereTheComparisonStarts(t *testing.T) {
	processed := []string{"100", "101", "102", "103"}
	at := func(id string) domain.State { return domain.NewState("n", "a", id, "v", stateTime) }

	if from, nothing, err := at("101").ChooseFrom(processed); err != nil || nothing || from != "101" {
		t.Fatalf("remembered 101: from %q nothing %v err %v", from, nothing, err)
	}
	if _, nothing, err := at("103").ChooseFrom(processed); err != nil || !nothing {
		t.Fatalf("remembered the latest: nothing %v err %v, want nothing new", nothing, err)
	}
	if from, nothing, err := at("999").ChooseFrom(processed); err == nil || nothing || from != "" {
		t.Fatalf("a remembered snapshot that is not processed: from %q nothing %v err %v, want an error and no guess", from, nothing, err)
	}
	if _, _, err := at("101").ChooseFrom(nil); err == nil {
		t.Fatal("no processed snapshots at all was accepted")
	}
}
