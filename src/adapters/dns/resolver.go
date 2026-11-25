// Package dns is the DNS adapter: it looks a hostname up with the system
// resolver, or with one named server. It imports the ports only.
package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Config configures the resolver. Server is "ip:port", or empty for the system
// resolver. Dial is a test seam for the named server's connection.
type Config struct {
	Server string
	Dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// Resolver implements ports.Resolver.
type Resolver struct{ res *net.Resolver }

var _ ports.Resolver = (*Resolver)(nil)

// New builds the resolver. A named server must be an ip:port; every query then
// goes to that server and never to the system resolver.
func New(cfg Config) (*Resolver, error) {
	if cfg.Server == "" {
		return &Resolver{res: net.DefaultResolver}, nil
	}
	server, err := netip.ParseAddrPort(cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("dns adapter: the resolver must be ip:port, got %q", cfg.Server)
	}
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	return &Resolver{res: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, server.String())
		},
	}}, nil
}

// Lookup returns every address of name, IPv4 and IPv6.
func (r *Resolver) Lookup(ctx context.Context, name string) ([]netip.Addr, error) {
	return r.res.LookupNetIP(ctx, "ip", name)
}
