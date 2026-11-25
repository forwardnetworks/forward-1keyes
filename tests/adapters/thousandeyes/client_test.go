package thousandeyes_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/adapters/thousandeyes"
	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// fakeTE is an in-process ThousandEyes. A RoundTripper delivers requests meant
// for api.thousandeyes.com to it, so the URL the adapter builds is the real one.
type fakeTE struct {
	srv        *httptest.Server
	mu         sync.Mutex
	seen       []string // every URL the adapter asked for, as it built it
	auth       []string
	limited    int // answer the next n requests with 429
	resetAt    string
	nextToEvil bool
}

func newFakeTE(t *testing.T) *fakeTE {
	t.Helper()
	f := &fakeTE{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTE) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.seen = append(f.seen, req.URL.String())
	f.auth = append(f.auth, req.Header.Get("Authorization"))
	f.mu.Unlock()
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "https"
	clone.URL.Host = strings.TrimPrefix(f.srv.URL, "https://")
	return f.srv.Client().Transport.RoundTrip(clone)
}

func (f *fakeTE) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	expand := r.URL.Query().Get("expand")
	write := func(v any) {
		// like the real API: sub-resources only come back when they are expanded
		if m, ok := v.(map[string]any); ok && strings.HasPrefix(r.URL.Path, "/v7/tests/") {
			if !strings.Contains(expand, "agent") {
				delete(m, "agents")
			}
			if !strings.Contains(expand, "label") {
				delete(m, "labels")
			}
		}
		_ = json.NewEncoder(w).Encode(v)
	}
	if f.limited > 0 {
		f.limited--
		if f.resetAt != "" {
			w.Header().Set("X-Organization-Rate-Limit-Reset", f.resetAt)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		write(map[string]any{"error": "rate limited"})
		return
	}
	if r.Header.Get("Authorization") == "Bearer wrong" {
		w.WriteHeader(http.StatusUnauthorized)
		write(map[string]any{"error": "unauthorized"})
		return
	}
	aid := r.URL.Query().Get("aid")
	switch {
	case r.URL.Path == "/v7/account-groups":
		write(map[string]any{"accountGroups": []any{
			map[string]any{"accountGroupName": "Acme", "aid": "1234"}, map[string]any{"accountGroupName": "Other", "aid": "99"}}})
	case r.URL.Path == "/v7/tests" && r.URL.Query().Get("page") == "":
		next := "https://api.thousandeyes.com/v7/tests?aid=" + aid + "&page=2"
		if f.nextToEvil {
			next = "https://evil.example/v7/tests?aid=" + aid + "&page=2"
		}
		write(map[string]any{"tests": []any{map[string]any{"testId": "1", "testName": "web", "type": "agent-to-server", "enabled": true}},
			"_links": map[string]any{"next": map[string]any{"href": next}}})
	case r.URL.Path == "/v7/tests":
		write(map[string]any{"tests": []any{map[string]any{"testId": "2", "testName": "site", "type": "http-server", "enabled": false}}})
	case r.URL.Path == "/v7/tests/agent-to-server/1":
		write(map[string]any{"testId": "1", "testName": "web", "type": "agent-to-server", "enabled": true, "server": "app.example.com:8443", "protocol": "tcp",
			"labels": []any{map[string]any{"labelId": "l1", "name": "prod"}, map[string]any{"labelId": "l2", "name": "edge"}},
			"agents": []any{map[string]any{"agentId": "201"}, map[string]any{"agentId": "202"}}})
	case r.URL.Path == "/v7/tests/agent-to-server/3":
		write(map[string]any{"testId": "3", "testName": "ping", "type": "agent-to-server", "enabled": true, "server": "10.0.0.9", "protocol": "icmp",
			"agents": []any{map[string]any{"agentId": "202"}}})
	case r.URL.Path == "/v7/tests/agent-to-server/5":
		write(map[string]any{"testId": "5", "testName": "loud", "type": "agent-to-server", "enabled": true, "server": "10.0.0.5:53", "protocol": "UDP", "agents": []any{}})
	case r.URL.Path == "/v7/tests/agent-to-server/6":
		http.Redirect(w, r, "https://evil.example/v7/tests/agent-to-server/6", http.StatusFound)
	case r.URL.Path == "/v7/tests/agent-to-agent/7":
		write(map[string]any{"testId": "7", "testName": "dc to dc", "type": "agent-to-agent", "enabled": true, "targetAgentId": "203", "direction": "bidirectional",
			"protocol": "tcp", "port": 8443, "agents": []any{map[string]any{"agentId": "202"}}})
	case r.URL.Path == "/v7/tests/agent-to-agent/8":
		write(map[string]any{"testId": "8", "testName": "one way", "type": "agent-to-agent", "enabled": true, "targetAgentId": "203", "protocol": "tcp", "agents": []any{}})
	case r.URL.Path == "/v7/tests/http-server/2":
		write(map[string]any{"testId": "2", "testName": "site", "type": "http-server", "enabled": true, "url": "https://app.example.com/x", "protocol": "tcp",
			"overrideAgentProxy": true, "overrideProxyId": "p1", "agents": []any{map[string]any{"agentId": "201"}}})
	case r.URL.Path == "/v7/tests/http-server/4":
		write(map[string]any{"testId": "4", "testName": "plain", "type": "http-server", "enabled": true, "url": "http://app.example.com:8080/", "agents": []any{}})
	case r.URL.Path == "/v7/agents":
		write(map[string]any{"agents": []any{
			map[string]any{"agentId": "201", "agentType": "cloud", "ipAddresses": []any{"198.51.100.4"}},
			map[string]any{"agentId": "202", "agentType": "enterprise", "ipAddresses": []any{"10.1.0.7", "fe80::1"}},
			map[string]any{"agentId": "203", "agentType": "enterprise-cluster", "clusterMembers": []any{
				map[string]any{"ipAddresses": []any{"10.2.0.2", "10.2.0.1"}}, map[string]any{"ipAddresses": []any{"10.2.0.1"}}}},
		}})
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"error": "no such route " + r.URL.Path})
	}
}

func newClient(t *testing.T, f *fakeTE, sleeps *[]time.Duration) *thousandeyes.Client {
	t.Helper()
	cfg := thousandeyes.Config{
		Token: "te-secret-token-123", HTTPClient: &http.Client{Transport: f},
		Sleep: func(d time.Duration) {
			if sleeps != nil {
				*sleeps = append(*sleeps, d)
			}
		},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	c, err := thousandeyes.New(cfg)
	if err != nil {
		t.Fatalf("thousandeyes.New: %v", err)
	}
	return c
}

func TestAccountGroups(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	got, err := c.AccountGroups(context.Background())
	if err != nil || !slices.Equal(got, []domain.AccountGroup{{Name: "Acme", AID: "1234"}, {Name: "Other", AID: "99"}}) {
		t.Fatalf("AccountGroups = %+v, %v", got, err)
	}
}

func TestListTestsFollowsPagesAndPassesTheAid(t *testing.T) {
	f := newFakeTE(t)
	c := newClient(t, f, nil)
	got, err := c.ListTests(context.Background(), "1234")
	want := []domain.TestSummary{{ID: "1", Name: "web", Type: "agent-to-server", Enabled: true}, {ID: "2", Name: "site", Type: "http-server", Enabled: false}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ListTests = %+v, %v; want %+v", got, err, want)
	}
	for _, u := range f.seen {
		if !strings.Contains(u, "aid=1234") {
			t.Fatalf("a request did not pass the aid: %s", u)
		}
	}
}

func TestListTestsRefusesANextLinkOnAnotherHost(t *testing.T) {
	f := newFakeTE(t)
	f.nextToEvil = true
	c := newClient(t, f, nil)
	if _, err := c.ListTests(context.Background(), "1234"); err == nil {
		t.Fatal("ListTests followed a next link to another host")
	}
	for _, u := range f.seen {
		if strings.Contains(u, "evil.example") {
			t.Fatalf("a request went to the other host: %s", u)
		}
	}
}

func TestGetTestAgentToServer(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	got, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "1", Type: "agent-to-server"})
	if err != nil {
		t.Fatalf("GetTest: %v", err)
	}
	if got.ID != "1" || got.Name != "web" || got.Type != "agent-to-server" || !got.Enabled || got.Target != "app.example.com" ||
		got.Protocol != "tcp" || got.Port == nil || *got.Port != 8443 || got.UsesProxy ||
		!slices.Equal(got.Labels, []string{"edge", "prod"}) || !slices.Equal(got.AgentIDs, []string{"201", "202"}) {
		t.Fatalf("GetTest = %+v", got)
	}
	icmp, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "3", Type: "agent-to-server"})
	if err != nil || icmp.Protocol != "icmp" || icmp.Port != nil || icmp.Target != "10.0.0.9" {
		t.Fatalf("ICMP test = %+v, %v; want icmp, no port, target 10.0.0.9", icmp, err)
	}
}

func TestGetTestHTTPServer(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	proxied, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "2", Type: "http-server"})
	if err != nil || proxied.Target != "https://app.example.com/x" || !proxied.UsesProxy || proxied.Type != "http-server" {
		t.Fatalf("proxied test = %+v, %v", proxied, err)
	}
	plain, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "4", Type: "http-server"})
	if err != nil || plain.UsesProxy || plain.Target != "http://app.example.com:8080/" || len(plain.AgentIDs) != 0 {
		t.Fatalf("plain test = %+v, %v", plain, err)
	}
}

func TestListAgents(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	got, err := c.ListAgents(context.Background(), "1234")
	if err != nil || len(got) != 3 {
		t.Fatalf("ListAgents = %+v, %v", got, err)
	}
	by := map[string]domain.AgentInfo{}
	for _, a := range got {
		by[a.ID] = a
	}
	if a := by["201"]; a.Kind != domain.AgentCloud || len(a.Addresses) != 0 {
		t.Fatalf("cloud agent = %+v, want kind cloud and no addresses", a)
	}
	if a := by["202"]; a.Kind != domain.AgentEnterprise || !slices.Equal(a.Addresses, []netip.Addr{netip.MustParseAddr("10.1.0.7"), netip.MustParseAddr("fe80::1")}) {
		t.Fatalf("enterprise agent = %+v", a)
	}
	if a := by["203"]; a.Kind != domain.AgentCluster || !slices.Equal(a.Addresses, []netip.Addr{netip.MustParseAddr("10.2.0.1"), netip.MustParseAddr("10.2.0.2")}) {
		t.Fatalf("cluster = %+v, want the sorted union of its members' addresses", a)
	}
}

// Rate limits: on 429 the adapter waits until the reset time the header gives,
// or 5, 10 and 20 seconds without one, and gives up after 3 retries.
func TestRateLimits(t *testing.T) {
	t.Run("backoff without a header", func(t *testing.T) {
		f := newFakeTE(t)
		f.limited = 2
		var sleeps []time.Duration
		c := newClient(t, f, &sleeps)
		if _, err := c.AccountGroups(context.Background()); err != nil {
			t.Fatalf("AccountGroups: %v", err)
		}
		if !slices.Equal(sleeps, []time.Duration{5 * time.Second, 10 * time.Second}) {
			t.Fatalf("slept %v, want 5s then 10s", sleeps)
		}
	})
	t.Run("until the reset time", func(t *testing.T) {
		f := newFakeTE(t)
		f.limited = 1
		f.resetAt = "1800000007"
		var sleeps []time.Duration
		c := newClient(t, f, &sleeps)
		if _, err := c.AccountGroups(context.Background()); err != nil {
			t.Fatalf("AccountGroups: %v", err)
		}
		if !slices.Equal(sleeps, []time.Duration{7 * time.Second}) {
			t.Fatalf("slept %v, want 7s", sleeps)
		}
	})
	t.Run("gives up after three retries", func(t *testing.T) {
		f := newFakeTE(t)
		f.limited = 10
		var sleeps []time.Duration
		c := newClient(t, f, &sleeps)
		if _, err := c.AccountGroups(context.Background()); err == nil {
			t.Fatal("AccountGroups succeeded while rate limited")
		}
		if len(sleeps) != 3 {
			t.Fatalf("slept %d times, want 3", len(sleeps))
		}
	})
}

// Adapter side: every request goes to https://api.thousandeyes.com with
// the bearer token, whatever the environment says; no field of Config can move
// the host; and a failure never shows the token.
func TestRequestsGoToTheFixedHostWithTheToken(t *testing.T) {
	t.Setenv("TE_BASE_URL", "https://evil.example")
	t.Setenv("THOUSANDEYES_URL", "https://evil.example")
	f := newFakeTE(t)
	c := newClient(t, f, nil)
	if _, err := c.ListTests(context.Background(), "1234"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListAgents(context.Background(), "1234"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	for i, u := range f.seen {
		if !strings.HasPrefix(u, "https://api.thousandeyes.com/v7/") {
			t.Fatalf("request %d went to %s", i, u)
		}
		if f.auth[i] != "Bearer te-secret-token-123" {
			t.Fatalf("request %d carried Authorization %q", i, f.auth[i])
		}
	}
	f.mu.Unlock()

	for i := 0; i < reflect.TypeOf(thousandeyes.Config{}).NumField(); i++ {
		name := strings.ToLower(reflect.TypeOf(thousandeyes.Config{}).Field(i).Name)
		for _, bad := range []string{"url", "host", "base", "endpoint", "server"} {
			if strings.Contains(name, bad) {
				t.Fatalf("Config.%s could move the host", reflect.TypeOf(thousandeyes.Config{}).Field(i).Name)
			}
		}
	}

	bad, err := thousandeyes.New(thousandeyes.Config{Token: "wrong", HTTPClient: &http.Client{Transport: f}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.AccountGroups(context.Background()); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a 401 must be an error that does not show the token: %v", err)
	}
	if _, err := thousandeyes.New(thousandeyes.Config{}); err == nil {
		t.Fatal("New accepted an empty token")
	}
}

// A protocol written in capitals is read as lower case.
func TestProtocolIsLowerCased(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	got, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "5", Type: "agent-to-server"})
	if err != nil || got.Protocol != "udp" || got.Port == nil || *got.Port != 53 {
		t.Fatalf("GetTest = %+v, %v; want udp on port 53", got, err)
	}
}

// A redirect is never followed, so the token cannot travel to another host.
func TestARedirectIsNeverFollowed(t *testing.T) {
	f := newFakeTE(t)
	c := newClient(t, f, nil)
	if _, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "6", Type: "agent-to-server"}); err == nil {
		t.Fatal("GetTest followed a redirect")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.seen {
		if strings.Contains(u, "evil.example") {
			t.Fatalf("a request went to the redirect target: %s", u)
		}
	}
}

// agent-to-agent tests are read too: the target agent, the direction, the
// protocol and the port, with a direction that is absent left absent.
func TestGetTestAgentToAgent(t *testing.T) {
	c := newClient(t, newFakeTE(t), nil)
	got, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "7", Type: "agent-to-agent"})
	if err != nil {
		t.Fatalf("GetTest: %v", err)
	}
	if got.Type != "agent-to-agent" || got.Target != "203" || got.Protocol != "tcp" || got.Port == nil || *got.Port != 8443 ||
		got.Direction == nil || *got.Direction != "bidirectional" || !slices.Equal(got.AgentIDs, []string{"202"}) {
		t.Fatalf("GetTest = %+v", got)
	}
	bare, err := c.GetTest(context.Background(), "1234", domain.TestSummary{ID: "8", Type: "agent-to-agent"})
	if err != nil || bare.Direction != nil || bare.Port != nil || bare.Target != "203" {
		t.Fatalf("a test without a direction or port = %+v, %v; want both absent", bare, err)
	}
}
