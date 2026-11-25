package ports

import "github.com/forwardnetworks/forward-1keyes/src/domain"

// Redacted is what Redact leaves in place of a secret.
const Redacted = domain.Redacted

// The security rules adapters must follow, re-exported so adapters import this
// package and never the domain.

// HostAllowed says whether a client based at base may send or follow to target.
func HostAllowed(base, target string) bool { return domain.HostAllowed(base, target) }

// RequestPathAllowed says whether a Forward request path may be sent.
func RequestPathAllowed(path string) bool { return domain.RequestPathAllowed(path) }

// Redact removes Authorization values and the given secrets from s.
func Redact(s string, secrets []string) string { return domain.Redact(s, secrets) }

// ValidateForwardURL checks FWD_URL and returns it without a trailing slash.
func ValidateForwardURL(raw string) (string, error) { return domain.ValidateForwardURL(raw) }

// MinSecretLength is the shortest secret the tool accepts.
const MinSecretLength = domain.MinSecretLength

// DefinitionHashJSON hashes a check definition as Forward returns it.
func DefinitionHashJSON(raw []byte) (string, error) { return domain.DefinitionHashJSON(raw) }
