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
	"github.com/forwardnetworks/forward-1keyes/src/adapters/thousandeyes"
)

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
	case "plan", "status", "report":
	case "apply":
		mode = ModeForwardApply
	default:
		return Deps{}, fmt.Errorf("compositionroot: unknown verb %q", cfg.Verb)
	}
	log := opts.Log
	if log == nil {
		log = os.Stderr
	}
	var secrets []string
	for _, s := range []string{cfg.TEToken, cfg.ForwardPass} {
		if s != "" {
			secrets = append(secrets, s)
		}
	}
	client := func(m Mode) *http.Client {
		return &http.Client{Transport: NewTransport(TransportOptions{
			Base: opts.BaseTransport, Mode: m, Log: log, Debug: cfg.Debug, Bodies: cfg.DebugBodies, Secrets: secrets,
		})}
	}

	fwd, err := forward.New(forward.Config{BaseURL: cfg.ForwardURL, Username: cfg.ForwardUser, Password: cfg.ForwardPass, HTTPClient: client(mode)})
	if err != nil {
		return Deps{}, err
	}
	deps := Deps{Reader: fwd, Writer: fwd}
	if cfg.NeedTE {
		te, err := thousandeyes.New(thousandeyes.Config{Token: cfg.TEToken, HTTPClient: client(ModeThousandEyes)})
		if err != nil {
			return Deps{}, err
		}
		deps.TE = te
	}
	server := strings.TrimPrefix(cfg.ResolverSetting, "server:")
	if cfg.ResolverSetting == "system" || cfg.ResolverSetting == "" {
		server = ""
	}
	resolver, err := dns.New(dns.Config{Server: server, Dial: opts.DNSDial})
	if err != nil {
		return Deps{}, err
	}
	deps.Resolver = resolver
	return deps, nil
}
