package domain

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// ErrBadForwardURL is returned for a Forward URL the tool refuses to use.
var ErrBadForwardURL = errors.New("domain: FWD_URL must be an https URL with a host and no credentials")

// Redacted is what Redact leaves in place of a secret.
const Redacted = "[redacted]"

// MinSecretLength is the shortest secret the tool accepts: shorter ones could
// not be told from ordinary text when redacting.
const MinSecretLength = 8

var (
	authHeaderLine = regexp.MustCompile(`(?im)^([ \t]*authorization[ \t]*:[ \t]*).*$`)
	authJSONField  = regexp.MustCompile(`(?i)("authorization"[ \t]*:[ \t]*")[^"]*(")`)
)

// Redact removes the value of any Authorization header, as a header line or a
// JSON field, and every occurrence of each configured secret, replacing them
// with Redacted. A longer secret is removed before a shorter one it contains.
func Redact(s string, secrets []string) string {
	s = authHeaderLine.ReplaceAllString(s, "${1}"+Redacted)
	s = authJSONField.ReplaceAllString(s, "${1}"+Redacted+"${2}")
	ordered := slices.Clone(secrets)
	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range ordered {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, Redacted)
		}
	}
	return s
}

// errBadNotifyURL is returned for a NOTIFY_URL that cannot be used. It never
// carries the value.
var errBadNotifyURL = errors.New("domain: it must be an https URL with a host and no user information")

// ValidateNotifyURL checks NOTIFY_URL and returns it as it will be requested.
func ValidateNotifyURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errBadNotifyURL
	}
	return u.String(), nil
}

// ValidateForwardURL checks FWD_URL: https, a host, no user information, no
// query or fragment, and no path beyond "/". It returns the URL without a
// trailing slash.
func ValidateForwardURL(raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") {
		return "", ErrBadForwardURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return "", ErrBadForwardURL
	}
	return "https://" + u.Host, nil
}

// canonicalHost is the host and port a URL points at, lower-cased, with the
// default https port made explicit.
func canonicalHost(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = "443"
	}
	return host + ":" + port
}

// HostAllowed says whether a client whose base URL is base may follow or send
// to target: only its own host and port, over https, with no credentials in
// the URL. A relative target resolves against base; an empty one is refused.
func HostAllowed(base, target string) bool {
	if target == "" {
		return false
	}
	b, err := url.Parse(base)
	if err != nil || b.Scheme != "https" || b.Host == "" {
		return false
	}
	t, err := b.Parse(target)
	if err != nil || t.Scheme != "https" || t.User != nil || t.Host == "" {
		return false
	}
	return canonicalHost(t) == canonicalHost(b)
}

// RequestPathAllowed says whether a Forward request path may be sent: it must
// be /api or start with /api/, with no dot segments, empty segments (a trailing
// slash is fine), backslashes or percent-encoded dots and slashes.
func RequestPathAllowed(path string) bool {
	if path != "/api" && !strings.HasPrefix(path, "/api/") {
		return false
	}
	if strings.Contains(path, "\\") {
		return false
	}
	lower := strings.ToLower(path)
	for _, enc := range []string{"%2e", "%2f", "%5c"} {
		if strings.Contains(lower, enc) {
			return false
		}
	}
	segments := strings.Split(path[1:], "/")
	for i, s := range segments {
		if s == "." || s == ".." || (s == "" && i != len(segments)-1) {
			return false
		}
	}
	return true
}
