package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalJSON encodes v as compact JSON with object keys sorted bytewise at
// every depth, numbers exactly as encoded (an integer stays an integer), null
// kept, and no HTML escaping. It is the one form every digest and key in this
// tool is computed over.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("domain: canonical json: %w", err)
	}
	// Decoding into any turns every object, including struct fields, into a
	// map, and encoding a map sorts its keys bytewise.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("domain: canonical json: %w", err)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, fmt.Errorf("domain: canonical json: %w", err)
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
