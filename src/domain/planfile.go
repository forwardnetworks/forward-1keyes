package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Advisory is what a plan file carries that the digest does not cover.
type Advisory struct {
	GeneratedAt string `json:"generated_at"`
}

// PlanFile is a parsed plan file.
type PlanFile struct {
	Body     PlanBody
	Digest   string
	Advisory Advisory
}

func digestOf(canonicalBody []byte) string {
	sum := sha256.Sum256(canonicalBody)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PlanDigest is "sha256:" plus the hex sha256 of the canonical JSON of the body.
func PlanDigest(body PlanBody) (string, error) {
	canonicalBody, err := CanonicalJSON(body)
	if err != nil {
		return "", err
	}
	return digestOf(canonicalBody), nil
}

// MarshalPlan writes the plan file: a JSON object with exactly the members
// body, digest and advisory, in canonical form. The digest is "sha256:" plus
// the hex sha256 of the canonical JSON of the body.
func MarshalPlan(body PlanBody, adv Advisory) ([]byte, error) {
	canonicalBody, err := CanonicalJSON(body)
	if err != nil {
		return nil, err
	}
	file, err := CanonicalJSON(map[string]any{
		"body":     json.RawMessage(canonicalBody),
		"digest":   digestOf(canonicalBody),
		"advisory": adv,
	})
	if err != nil {
		return nil, err
	}
	return append(file, '\n'), nil
}

// VerifyPlan reads a plan file and checks it: it must have exactly the members
// body, digest and advisory, the recomputed digest must equal both wantDigest
// and the stored digest, and the plan must be appliable.
func VerifyPlan(raw []byte, wantDigest string) (PlanFile, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return PlanFile{}, fmt.Errorf("%w: %v", ErrPlanShape, err)
	}
	if len(members) != 3 || members["body"] == nil || members["digest"] == nil || members["advisory"] == nil {
		return PlanFile{}, fmt.Errorf("%w: want exactly body, digest and advisory", ErrPlanShape)
	}
	var stored string
	if err := json.Unmarshal(members["digest"], &stored); err != nil {
		return PlanFile{}, fmt.Errorf("%w: digest: %v", ErrPlanShape, err)
	}

	dec := json.NewDecoder(bytes.NewReader(members["body"]))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return PlanFile{}, fmt.Errorf("%w: body: %v", ErrPlanShape, err)
	}
	canonicalBody, err := CanonicalJSON(generic)
	if err != nil {
		return PlanFile{}, fmt.Errorf("%w: body: %v", ErrPlanShape, err)
	}
	if got := digestOf(canonicalBody); got != wantDigest || got != stored {
		return PlanFile{}, ErrDigestMismatch
	}

	var f PlanFile
	f.Digest = stored
	strict := json.NewDecoder(bytes.NewReader(members["body"]))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&f.Body); err != nil {
		return PlanFile{}, fmt.Errorf("%w: body: %v", ErrPlanShape, err)
	}
	if f.Body.Schema != PlanSchema {
		return PlanFile{}, fmt.Errorf("%w: schema %q", ErrPlanShape, f.Body.Schema)
	}
	if err := json.Unmarshal(members["advisory"], &f.Advisory); err != nil {
		return PlanFile{}, fmt.Errorf("%w: advisory: %v", ErrPlanShape, err)
	}
	if !f.Body.Appliable {
		return f, ErrUnappliable
	}
	return f, nil
}
