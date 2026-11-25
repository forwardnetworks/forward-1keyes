package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrNoInternetEdge is returned for a check whose source is the internet edge:
// how Forward expresses that source is not yet verified against a live network.
var ErrNoInternetEdge = errors.New("domain: no internet-edge source form is known yet")

// ErrMultiplePrefixes is returned when a source or destination has more than
// one prefix: Forward rejects a list in one location filter, so such
// a flow needs one check per prefix.
var ErrMultiplePrefixes = errors.New("domain: a location filter takes one prefix")

// CheckSpec is the check the tool would write to Forward.
type CheckSpec struct {
	Name       string         `json:"name"`
	Note       string         `json:"note"`
	Tags       []string       `json:"tags"`
	Definition map[string]any `json:"definition"`
}

// DefinitionHash is the hex sha256 of the canonical JSON of the definition.
func (c CheckSpec) DefinitionHash() string {
	return definitionHash(c.Definition)
}

// ContentHash covers everything the tool writes: name, note, tags (as a set)
// and definition. A spec and the check Forward returns for it have the same
// ContentHash exactly when nothing differs.
func (c CheckSpec) ContentHash() string {
	return contentHash(c.Name, c.Note, c.Tags, c.DefinitionHash())
}

// DefinitionHashJSON is DefinitionHash for a definition as Forward returns it:
// the same content in any key order hashes the same.
func DefinitionHashJSON(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", fmt.Errorf("domain: definition is not JSON: %w", err)
	}
	b, err := CanonicalJSON(generic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func definitionHash(def map[string]any) string {
	b, err := CanonicalJSON(def)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func contentHash(name, note string, tags []string, defHash string) string {
	sorted := slices.Clone(tags)
	slices.Sort(sorted)
	b, err := CanonicalJSON(map[string]any{"name": name, "note": note, "tags": sorted, "definition_hash": defHash})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ObservedRecord is a Forward check as the plan and apply read it.
type ObservedRecord struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Note           string   `json:"note"`
	Tags           []string `json:"tags"`
	DefinitionHash string   `json:"definition_hash"`
	Active         bool     `json:"active"`
}

// ContentHash is comparable with CheckSpec.ContentHash.
func (o ObservedRecord) ContentHash() string {
	return contentHash(o.Name, o.Note, o.Tags, o.DefinitionHash)
}

const (
	namePrefix = "1keyes: "
	noteHeader = "Managed by forward-1keyes (contract 1). Edits are reported by the next plan, which proposes an update back to the generated name, note and definition."
)

var ipProtocols = map[string]string{"icmp": "1", "tcp": "6", "udp": "17"}

// BuildCheck builds the Existential check for a planned check, for a
// ThousandEyes account group id aid. Names are at most nameLimit characters.
// The definition has the shape of the Existential check the forward-dynatrace
// integration creates on fwd.app: a single address is written bare, any other
// prefix in CIDR form.
func BuildCheck(p PlannedCheck, aid string, nameLimit int) (CheckSpec, error) {
	if p.Source.InternetEdge {
		return CheckSpec{}, ErrNoInternetEdge
	}
	proto, ok := ipProtocols[p.Protocol]
	if !ok {
		return CheckSpec{}, fmt.Errorf("domain: no IP protocol number for %q", p.Protocol)
	}
	hex, ok := strings.CutPrefix(p.SourceKey, "sha256:")
	if !ok || len(hex) < 12 {
		return CheckSpec{}, fmt.Errorf("domain: source key %q is not sha256:<hex>", p.SourceKey)
	}
	if len(p.Contributors) == 0 {
		return CheckSpec{}, errors.New("domain: a planned check needs a contributing test")
	}
	srcs, dsts := sortedPrefixes(p.Source.Prefixes), sortedPrefixes(p.DestPrefixes)
	if len(srcs) == 0 || len(dsts) == 0 {
		return CheckSpec{}, errors.New("domain: a planned check needs source and destination prefixes")
	}
	if len(srcs) > 1 || len(dsts) > 1 {
		return CheckSpec{}, ErrMultiplePrefixes
	}

	headers := []any{packetFilter("ip_proto", proto)}
	if p.Port != nil && (p.Protocol == "tcp" || p.Protocol == "udp") {
		headers = append(headers, packetFilter("tp_dst", strconv.Itoa(*p.Port)))
	}
	def := map[string]any{
		"checkType": "Existential",
		"filters": map[string]any{
			"from":      map[string]any{"location": subnetFilter(p.Source.Prefixes), "headers": headers},
			"to":        map[string]any{"location": subnetFilter(p.DestPrefixes)},
			"flowTypes": []any{"VALID"},
		},
		"noiseTypes":               []any{},
		"headerFieldsWithDefaults": []any{"url"},
		"returnPath":               "ANY",
	}

	name, err := checkName(p, hex[:12], nameLimit)
	if err != nil {
		return CheckSpec{}, err
	}
	note := noteHeader + "\nContributors:"
	for _, c := range p.Contributors {
		note += fmt.Sprintf("\n- test %s %q agents %s", c.TestID, c.TestName, strings.Join(c.AgentIDs, ","))
	}
	return CheckSpec{Name: name, Note: note, Tags: EncodeTags(aid, p.SourceKey), Definition: def}, nil
}

// checkName is "1keyes: <test name> [<test ID>] <slot label> #<key12>", with
// the test name of the first contributor cut so the whole fits the limit.
func checkName(p PlannedCheck, key12 string, limit int) (string, error) {
	first := p.Contributors[0]
	src := sortedPrefixes(p.Source.Prefixes)
	label := src[0].String()
	if len(src) > 1 {
		label += "+" + strconv.Itoa(len(src)-1)
	}
	head := namePrefix
	tail := " [" + first.TestID + "] " + label + " #" + key12
	avail := limit - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if avail < 0 {
		return "", fmt.Errorf("domain: a name limit of %d is too short for %q", limit, head+tail)
	}
	test := first.TestName
	if utf8.RuneCountInString(test) > avail {
		runes := []rune(test)
		switch {
		case avail >= 3:
			test = string(runes[:avail-3]) + "..."
		default:
			test = string(runes[:avail])
		}
	}
	return head + test + tail, nil
}

func packetFilter(field, value string) map[string]any {
	return map[string]any{"type": "PacketFilter", "values": map[string]any{field: []any{value}}}
}

func subnetFilter(prefixes []netip.Prefix) map[string]any {
	parts := make([]string, 0, len(prefixes))
	for _, p := range sortedPrefixes(prefixes) {
		if p.IsSingleIP() {
			parts = append(parts, p.Addr().String())
		} else {
			parts = append(parts, p.String())
		}
	}
	return map[string]any{"type": "SubnetLocationFilter", "value": strings.Join(parts, ",")}
}
