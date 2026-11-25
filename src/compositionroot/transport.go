package compositionroot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Mode says what a client may send.
type Mode int

const (
	// ModeThousandEyes allows only GET.
	ModeThousandEyes Mode = iota + 1
	// ModeForwardRead allows GET and the NQE read, which is a POST.
	ModeForwardRead
	// ModeForwardApply also allows creating and updating checks.
	ModeForwardApply
)

// TransportOptions configures NewTransport.
type TransportOptions struct {
	Base    http.RoundTripper // nil means http.DefaultTransport
	Mode    Mode
	Log     io.Writer // where debug lines go; nil means nowhere
	Debug   bool      // log method, path, status and duration
	Bodies  bool      // also log bodies, redacted
	Secrets []string  // removed from anything logged
	Now     func() time.Time
}

const maxLoggedBody = 4096

var (
	nqePath    = regexp.MustCompile(`^/api/nqe$`)
	createPath = regexp.MustCompile(`^/api/snapshots/[^/]+/checks$`)
	checkPath  = regexp.MustCompile(`^/api/snapshots/[^/]+/checks/[^/]+$`)
)

type transport struct{ opts TransportOptions }

// NewTransport wraps a RoundTripper: it refuses what the mode does not allow
// (never a DELETE, never a deactivate path, never a path outside the API), and
// logs calls without query strings, headers or, unless asked, bodies.
func NewTransport(opts TransportOptions) http.RoundTripper {
	if opts.Base == nil {
		opts.Base = http.DefaultTransport
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &transport{opts: opts}
}

func (t *transport) allow(req *http.Request) error {
	method, escaped := req.Method, req.URL.EscapedPath()
	if method == http.MethodDelete {
		return errors.New("refusing a DELETE: the tool never deletes")
	}
	if strings.Contains(strings.ToLower(escaped), "deactivate") {
		return errors.New("refusing a deactivate request: the tool never deactivates")
	}
	switch t.opts.Mode {
	case ModeThousandEyes:
		if method != http.MethodGet {
			return fmt.Errorf("refusing %s to ThousandEyes: the tool only reads it", method)
		}
		return nil
	case ModeForwardRead, ModeForwardApply:
		if !ports.RequestPathAllowed(escaped) {
			return fmt.Errorf("refusing a request outside /api: %s", escaped)
		}
		switch {
		case method == http.MethodGet:
			return nil
		case method == http.MethodPost && nqePath.MatchString(escaped):
			return nil // NQE is a read sent as a POST
		case t.opts.Mode == ModeForwardApply && method == http.MethodPost && createPath.MatchString(escaped):
			return nil
		case t.opts.Mode == ModeForwardApply && method == http.MethodPatch && checkPath.MatchString(escaped):
			return nil
		}
		return fmt.Errorf("refusing %s %s: not allowed in this mode", method, escaped)
	}
	return errors.New("transport has no mode")
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.allow(req); err != nil {
		return nil, err
	}
	logging := t.opts.Debug && t.opts.Log != nil
	var reqBody []byte
	if logging && t.opts.Bodies && req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	start := t.opts.Now()
	resp, err := t.opts.Base.RoundTrip(req)
	took := t.opts.Now().Sub(start).Round(time.Millisecond)
	if !logging {
		return resp, err
	}
	status := "error"
	if err == nil {
		status = fmt.Sprint(resp.StatusCode)
	}
	fmt.Fprintf(t.opts.Log, "%s %s %s %s\n", req.Method, req.URL.Path, status, took)
	if t.opts.Bodies {
		t.logBody("request body", reqBody)
		if err == nil && resp.Body != nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(body))
			t.logBody("response body", body)
		}
	}
	return resp, err
}

func (t *transport) logBody(label string, body []byte) {
	if len(body) == 0 {
		return
	}
	if len(body) > maxLoggedBody {
		body = body[:maxLoggedBody]
	}
	fmt.Fprintf(t.opts.Log, "  %s: %s\n", label, ports.Redact(string(body), t.opts.Secrets))
}
