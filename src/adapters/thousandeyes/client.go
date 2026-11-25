// Package thousandeyes is the ThousandEyes adapter: a small client for the v7
// API that implements ports.TeClient. It talks to one fixed host and decodes
// only the fields the tool uses. It imports the ports, never the domain.
package thousandeyes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// baseURL is the only host the adapter talks to. It is a constant on purpose:
// no flag, variable or option can change it.
const baseURL = "https://api.thousandeyes.com"

const (
	maxRetries   = 3
	maxPages     = 1000
	maxBodyBytes = 5 << 20
)

var backoff = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}

// Config configures the adapter. There is deliberately no base URL: the host is
// fixed. HTTPClient is a test seam, and Sleep and Now are time seams.
type Config struct {
	Token      string
	HTTPClient *http.Client
	Sleep      func(time.Duration)
	Now        func() time.Time
}

// Client implements ports.TeClient.
type Client struct {
	token string
	hc    *http.Client
	sleep func(time.Duration)
	now   func() time.Time
}

var _ ports.TeClient = (*Client)(nil)

// New builds the adapter. A redirect is never followed.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("thousandeyes adapter: a token is required")
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{token: cfg.Token, hc: hc, sleep: cfg.Sleep, now: cfg.Now}
	if c.sleep == nil {
		c.sleep = time.Sleep
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// get reads one URL, retrying a 429 up to maxRetries times, and returns the body.
func (c *Client) get(ctx context.Context, target string) ([]byte, error) {
	if !ports.HostAllowed(baseURL, target) {
		return nil, fmt.Errorf("thousandeyes adapter: refusing to request %s", redactHost(target))
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("thousandeyes adapter: GET %s: %w", path(target), scrub(err, c.token))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt >= maxRetries {
				return nil, fmt.Errorf("thousandeyes adapter: GET %s: rate limited after %d retries", path(target), maxRetries)
			}
			c.sleep(c.waitFor(resp.Header.Get("X-Organization-Rate-Limit-Reset"), attempt))
			continue
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return nil, fmt.Errorf("thousandeyes adapter: GET %s: %s", path(target), resp.Status)
		case readErr != nil:
			return nil, fmt.Errorf("thousandeyes adapter: GET %s: %w", path(target), readErr)
		}
		return body, nil
	}
}

// waitFor is the time to wait after a 429: until the reset time the header
// gives (epoch seconds), else 5, 10, then 20 seconds.
func (c *Client) waitFor(header string, attempt int) time.Duration {
	if reset, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil {
		if d := time.Unix(reset, 0).Sub(c.now()); d > 0 {
			return d
		}
		return time.Second
	}
	return backoff[min(attempt, len(backoff)-1)]
}

func path(target string) string {
	if u, err := url.Parse(target); err == nil {
		return u.Path
	}
	return "?"
}

func redactHost(target string) string {
	if u, err := url.Parse(target); err == nil {
		return u.Scheme + "://" + u.Host
	}
	return "an unreadable URL"
}

func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

func endpoint(p string, aid string) string {
	u := baseURL + p
	if aid != "" {
		u += "?aid=" + url.QueryEscape(aid)
	}
	return u
}

// nextLink returns the URL of the next page, or "".
func nextLink(body []byte) string {
	var l struct {
		Links struct {
			Next struct {
				Href string `json:"href"`
			} `json:"next"`
		} `json:"_links"`
	}
	_ = json.Unmarshal(body, &l)
	return l.Links.Next.Href
}

// pages reads a list endpoint and its next pages, handing each body to fn.
func (c *Client) pages(ctx context.Context, first string, fn func(body []byte) error) error {
	target := first
	for page := 0; target != ""; page++ {
		if page >= maxPages {
			return errors.New("thousandeyes adapter: too many pages")
		}
		body, err := c.get(ctx, target)
		if err != nil {
			return err
		}
		if err := fn(body); err != nil {
			return err
		}
		target = nextLink(body)
	}
	return nil
}

// AccountGroups lists the account groups the token can see.
func (c *Client) AccountGroups(ctx context.Context) ([]ports.AccountGroup, error) {
	var out []ports.AccountGroup
	err := c.pages(ctx, endpoint("/v7/account-groups", ""), func(body []byte) error {
		var r struct {
			Groups []struct {
				Name string `json:"accountGroupName"`
				AID  string `json:"aid"`
			} `json:"accountGroups"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("thousandeyes adapter: account groups: %w", err)
		}
		for _, g := range r.Groups {
			out = append(out, ports.AccountGroup{Name: g.Name, AID: g.AID})
		}
		return nil
	})
	return out, err
}

// ListTests lists the tests of an account group.
func (c *Client) ListTests(ctx context.Context, aid string) ([]ports.TestSummary, error) {
	var out []ports.TestSummary
	err := c.pages(ctx, endpoint("/v7/tests", aid), func(body []byte) error {
		var r struct {
			Tests []struct {
				ID      string `json:"testId"`
				Name    string `json:"testName"`
				Type    string `json:"type"`
				Enabled *bool  `json:"enabled"`
			} `json:"tests"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("thousandeyes adapter: tests: %w", err)
		}
		for _, t := range r.Tests {
			out = append(out, ports.TestSummary{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled != nil && *t.Enabled})
		}
		return nil
	})
	return out, err
}

type testDetail struct {
	ID      string `json:"testId"`
	Name    string `json:"testName"`
	Type    string `json:"type"`
	Enabled *bool  `json:"enabled"`
	Server  string `json:"server"`
	URL     string `json:"url"`
	Proto   string `json:"protocol"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Agents []struct {
		ID string `json:"agentId"`
	} `json:"agents"`
	OverrideAgentProxy bool   `json:"overrideAgentProxy"`
	OverrideProxyID    string `json:"overrideProxyId"`
	TargetAgentID      string `json:"targetAgentId"`
	Direction          string `json:"direction"`
	Port               *int   `json:"port"`
}

// GetTest reads the detail of an agent-to-server or http-server test.
func (c *Client) GetTest(ctx context.Context, aid string, s ports.TestSummary) (ports.TestConfig, error) {
	if s.Type != "agent-to-server" && s.Type != "http-server" && s.Type != "agent-to-agent" {
		return ports.TestConfig{}, fmt.Errorf("thousandeyes adapter: test type %q is not read", s.Type)
	}
	// agents and labels are only returned when the request expands them
	body, err := c.get(ctx, endpoint("/v7/tests/"+s.Type+"/"+url.PathEscape(s.ID), aid)+"&expand=agent,label")
	if err != nil {
		return ports.TestConfig{}, err
	}
	var d testDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return ports.TestConfig{}, fmt.Errorf("thousandeyes adapter: test %s: %w", s.ID, err)
	}
	cfg := ports.TestConfig{ID: d.ID, Name: d.Name, Type: d.Type, Enabled: d.Enabled != nil && *d.Enabled}
	for _, l := range d.Labels {
		cfg.Labels = append(cfg.Labels, l.Name)
	}
	slices.Sort(cfg.Labels)
	for _, a := range d.Agents {
		cfg.AgentIDs = append(cfg.AgentIDs, a.ID)
	}
	switch s.Type {
	case "agent-to-server":
		cfg.Protocol = strings.ToLower(d.Proto)
		cfg.Target, cfg.Port = splitServer(d.Server)
	case "agent-to-agent":
		cfg.Target, cfg.Protocol, cfg.Port = d.TargetAgentID, strings.ToLower(d.Proto), d.Port
		if d.Direction != "" {
			dir := d.Direction
			cfg.Direction = &dir
		}
	case "http-server":
		cfg.Target, cfg.Protocol = d.URL, "tcp"
		cfg.UsesProxy = d.OverrideProxyID != "" || d.OverrideAgentProxy
	}
	return cfg, nil
}

// splitServer reads ThousandEyes' "server": a name or address, with ":port"
// appended when the test has a port.
func splitServer(server string) (string, *int) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return server, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return server, nil
	}
	return host, &n
}

// ListAgents lists the agents of an account group. A cloud agent is located by
// its ID, so its addresses are dropped; a cluster is one agent whose addresses
// are the sorted union of its members'.
func (c *Client) ListAgents(ctx context.Context, aid string) ([]ports.AgentInfo, error) {
	var out []ports.AgentInfo
	err := c.pages(ctx, endpoint("/v7/agents", aid), func(body []byte) error {
		var r struct {
			Agents []struct {
				ID      string   `json:"agentId"`
				Type    string   `json:"agentType"`
				IPs     []string `json:"ipAddresses"`
				Members []struct {
					IPs []string `json:"ipAddresses"`
				} `json:"clusterMembers"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("thousandeyes adapter: agents: %w", err)
		}
		for _, a := range r.Agents {
			info := ports.AgentInfo{ID: a.ID, Kind: ports.AgentKind(a.Type)}
			if info.Kind != ports.AgentCloud {
				ips := a.IPs
				for _, m := range a.Members {
					ips = append(ips, m.IPs...)
				}
				info.Addresses = parseAddrs(ips)
			}
			out = append(out, info)
		}
		return nil
	})
	return out, err
}

func parseAddrs(ips []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ips {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(out)
}
