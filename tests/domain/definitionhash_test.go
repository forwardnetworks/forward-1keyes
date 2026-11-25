package domain_test

import (
	"testing"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Test: Forward returns a stored definition with the same
// content in a different key order, so the hash of what it returns must equal
// the hash of what was sent.
func TestDefinitionHashJSONIgnoresKeyOrder(t *testing.T) {
	sent := domain.CheckSpec{Definition: map[string]any{
		"checkType": "Existential", "returnPath": "ANY", "noiseTypes": []any{},
		"filters": map[string]any{"from": map[string]any{"location": map[string]any{"type": "SubnetLocationFilter", "value": "10.1.0.0/24"}}, "flowTypes": []any{"VALID"}},
	}}
	returned := `{"filters":{"flowTypes":["VALID"],"from":{"location":{"value":"10.1.0.0/24","type":"SubnetLocationFilter"}}},"noiseTypes":[],"returnPath":"ANY","checkType":"Existential"}`
	got, err := domain.DefinitionHashJSON([]byte(returned))
	if err != nil || got != sent.DefinitionHash() {
		t.Fatalf("DefinitionHashJSON = %q, %v; want %q", got, err, sent.DefinitionHash())
	}
	changed := `{"filters":{"flowTypes":["VALID"],"from":{"location":{"value":"10.1.0.0/25","type":"SubnetLocationFilter"}}},"noiseTypes":[],"returnPath":"ANY","checkType":"Existential"}`
	if other, _ := domain.DefinitionHashJSON([]byte(changed)); other == got {
		t.Fatal("a different definition must hash differently")
	}
	if _, err := domain.DefinitionHashJSON([]byte("not json")); err == nil {
		t.Fatal("a non-JSON definition must be an error")
	}
}
