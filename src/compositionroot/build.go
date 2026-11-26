package compositionroot

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/forwardnetworks/forward-1keyes/src/adapters/dns"
	"github.com/forwardnetworks/forward-1keyes/src/adapters/forward"
	"github.com/forwardnetworks/forward-1keyes/src/adapters/notify"
	"github.com/forwardnetworks/forward-1keyes/src/adapters/replay"
	"github.com/forwardnetworks/forward-1keyes/src/adapters/thousandeyes"
)

// secrets is what must never appear in anything the tool prints.
func (c Config) secrets() []string {
	var out []string
	for _, s := range []string{c.TEToken, c.ForwardPass, c.NotifySecret, c.NotifyURL, c.AlertToken} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// BuildOptions are the seams tests use; production leaves them zero.
type BuildOptions struct {
	BaseTransport http.RoundTripper
	DNSDial       func(ctx context.Context, network, address string) (net.Conn, error)
	Log           io.Writer // where debug lines go; stderr when nil
}

// DefaultBuild makes the real adapters for a Config.
func DefaultBuild(cfg Config) (Deps, error) { return BuildWith(cfg, BuildOptions{}) }

// BuildWith is DefaultBuild with test seams. Every client goes through a
// transport that enforces what its verb may send and logs on request.
func BuildWith(cfg Config, opts BuildOptions) (Deps, error) {
	mode := ModeForwardRead
	switch cfg.Verb {
	case "plan", "status", "report", "changes", "listen", "record":
	case "apply":
		mode = ModeForwardApply
	default:
		return Deps{}, fmt.Errorf("compositionroot: unknown verb %q", cfg.Verb)
	}
	log := opts.Log
	if log == nil {
		log = os.Stderr
	}
	secrets := cfg.secrets()
	client := func(m Mode) *http.Client {
		return &http.Client{Transport: NewTransport(TransportOptions{
			Base: opts.BaseTransport, Mode: m, Log: log, Debug: cfg.Debug, Bodies: cfg.DebugBodies, Secrets: secrets,
		})}
	}

	var deps Deps
	if cfg.Verb != "record" { // record never contacts Forward
		fwd, err := forward.New(forward.Config{BaseURL: cfg.ForwardURL, Username: cfg.ForwardUser, Password: cfg.ForwardPass, HTTPClient: client(mode)})
		if err != nil {
			return Deps{}, err
		}
		deps = Deps{Reader: fwd, Writer: fwd}
	}
	if cfg.Recording != nil {
		// A replay: no ThousandEyes client, no resolver, no token.
		source := replay.New(*cfg.Recording)
		deps.TE, deps.Resolver = source, source
	} else if cfg.NeedTE {
		te, err := thousandeyes.New(thousandeyes.Config{Token: cfg.TEToken, HTTPClient: client(ModeThousandEyes)})
		if err != nil {
			return Deps{}, err
		}
		deps.TE = te
		if cfg.Verb == "plan" {
			deps.Sources = te
		}
	}
	if cfg.Recording == nil {
		server := strings.TrimPrefix(cfg.ResolverSetting, "server:")
		if cfg.ResolverSetting == "system" || cfg.ResolverSetting == "" {
			server = ""
		}
		resolver, err := dns.New(dns.Config{Server: server, Dial: opts.DNSDial})
		if err != nil {
			return Deps{}, err
		}
		deps.Resolver = resolver
	}
	if cfg.NotifyURL != "" {
		// Its own client: one POST to one URL, with no service credential.
		hc := &http.Client{Transport: NewTransport(TransportOptions{
			Base: opts.BaseTransport, Mode: ModeNotify, NotifyURL: cfg.NotifyURL, Log: log, Debug: cfg.Debug,
		})}
		notifier, err := notify.New(notify.Config{URL: cfg.NotifyURL, Secret: cfg.NotifySecret, HTTPClient: hc})
		if err != nil {
			return Deps{}, err
		}
		deps.Notifier = notifier
	}
	return deps, nil
}
