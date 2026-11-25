package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// ObservedTest is a test as the plan saw it: enough to say later whether it
// changed.
type ObservedTest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

// ObservedAgent is an agent a written row depends on, as the plan saw it.
type ObservedAgent struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func hashOf(v any) string {
	b, err := CanonicalJSON(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestFingerprint is the sha256 of the canonical JSON of the decoded fields of a
// test: a field the client does not decode never enters it.
func TestFingerprint(t TestConfig) string {
	labels := slices.Clone(t.Labels)
	slices.Sort(labels)
	agents := slices.Clone(t.AgentIDs)
	slices.Sort(agents)
	return hashOf(map[string]any{
		"id": t.ID, "name": t.Name, "type": t.Type, "enabled": t.Enabled, "labels": labels,
		"target": t.Target, "protocol": t.Protocol, "port": t.Port, "direction": t.Direction,
		"uses_proxy": t.UsesProxy, "agent_ids": agents,
	})
}

// AgentFingerprint is the sha256 of the canonical JSON of {id, kind, addresses}.
// A cloud agent is located by its ID, never by address, so its addresses are
// always null and a routine address change does not read as drift.
func AgentFingerprint(a AgentInfo) string {
	var addrs any
	if a.Kind != AgentCloud {
		list := make([]string, 0, len(a.Addresses))
		for _, x := range a.Addresses {
			list = append(list, x.String())
		}
		slices.Sort(list)
		addrs = list
	}
	return hashOf(map[string]any{"id": a.ID, "kind": string(a.Kind), "addresses": addrs})
}
