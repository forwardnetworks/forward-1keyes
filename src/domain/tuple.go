package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// managedByValue is the value of the managed-by tag this tool writes.
const managedByValue = "com.forward.1keyes"

const (
	tagContractVersion = "contract-version"
	tagManagedBy       = "managed-by"
	tagSourceInstance  = "source-instance"
	tagSourceKey       = "source-key"

	contractVersion = 1
)

var sourceKeyPattern = regexp.MustCompile("^sha256:[0-9a-f]{64}$")

// KeyInput is the only data a source key depends on.
type KeyInput struct {
	TestID   string
	Slot     string
	Protocol string
	Port     *int // nil for protocols without a port, such as ICMP
	// Dest is the one destination address of a flow whose destination has
	// several modelled addresses; empty for every other flow.
	Dest string
}

// SourceKey returns "sha256:<64 lowercase hex>" over the canonical JSON of
// {test_id, slot, protocol, port}, plus dest for a flow whose destination has
// several addresses.
func SourceKey(in KeyInput) string {
	var port any
	if in.Port != nil {
		port = *in.Port
	}
	member := map[string]any{
		"test_id":  in.TestID,
		"slot":     in.Slot,
		"protocol": in.Protocol,
		"port":     port,
	}
	if in.Dest != "" {
		member["dest"] = in.Dest
	}
	body, err := CanonicalJSON(member)
	if err != nil {
		// A map of strings, an int and nil always encodes.
		panic("domain: source key: " + err.Error())
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EncodeTags returns the four ownership tags in sorted order for a
// ThousandEyes account group id and a source key ("sha256:<hex>").
func EncodeTags(aid, sourceKey string) []string {
	tags := []string{
		tagContractVersion + ":" + strconv.Itoa(contractVersion),
		tagManagedBy + ":" + managedByValue,
		tagSourceInstance + ":te-aid-" + aid,
		tagSourceKey + ":" + sourceKey,
	}
	sort.Strings(tags)
	return tags
}

// Ownership says how a check's tags read.
type Ownership int

const (
	OwnershipUnknown Ownership = iota // the zero value: never a valid answer
	NotOurs                           // no managed-by:com.forward.1keyes tag
	Ours                              // a strict, well-formed, current tuple
	MalformedTuple                    // managed by us, but the tuple is broken
	NewerContract                     // managed by us, contract version above 1
)

// Decoded is the result of reading a check's tags.
type Decoded struct {
	Ownership      Ownership
	SourceInstance string // for example "te-aid-1234"; set when Ours
	SourceKey      string // "sha256:<hex>"; set when Ours
}

// DecodeTags reads a check's tags strictly. A check is ours
// only with exactly one tag of each of the four keys, managed-by equal to
// managedByValue, contract-version 1 and a source key of the form
// sha256:<64 lowercase hex>. Tags of any other key are ignored, and order does
// not matter.
func DecodeTags(tags []string) Decoded {
	values := map[string][]string{}
	ours := false
	for _, tag := range tags {
		key, value, ok := strings.Cut(tag, ":")
		if !ok {
			continue
		}
		values[key] = append(values[key], value)
		if key == tagManagedBy && value == managedByValue {
			ours = true
		}
	}
	if !ours {
		return Decoded{Ownership: NotOurs}
	}

	one := func(key string) (string, bool) {
		v := values[key]
		if len(v) != 1 {
			return "", false
		}
		return v[0], true
	}
	if _, ok := one(tagManagedBy); !ok {
		return Decoded{Ownership: MalformedTuple}
	}
	version, ok := one(tagContractVersion)
	if !ok {
		return Decoded{Ownership: MalformedTuple}
	}
	n, canonical := parseVersion(version)
	switch {
	case !canonical || n < 1:
		return Decoded{Ownership: MalformedTuple}
	case n > contractVersion:
		return Decoded{Ownership: NewerContract}
	}
	instance, ok := one(tagSourceInstance)
	if !ok || instance == "" {
		return Decoded{Ownership: MalformedTuple}
	}
	key, ok := one(tagSourceKey)
	if !ok || !sourceKeyPattern.MatchString(key) {
		return Decoded{Ownership: MalformedTuple}
	}
	return Decoded{Ownership: Ours, SourceInstance: instance, SourceKey: key}
}

// parseVersion reads a contract version written as a plain decimal integer
// without a sign or leading zeros.
func parseVersion(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
