package thousandeyes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

var _ ports.SourceAddressReader = (*Client)(nil)

// Why a path visualization read stopped. The texts are fixed: nothing from a
// response, its status line or its links goes into an error of this read.
var (
	errPageUnread  = errors.New("a page could not be read")
	errPageDecode  = errors.New("a page could not be decoded")
	errPageOutside = errors.New("a next page points outside this read")
	errPageLimit   = errors.New("too many pages")
)

// SourceAddresses reads the path visualization results of a test inside the
// window, over every page up to the page limit. Each page is asked for once: a
// rate limit is not waited for. A next page is followed only to the same path
// on the same host.
func (c *Client) SourceAddresses(ctx context.Context, aid, testID string, window time.Duration) ([]ports.ObservedSource, error) {
	resource := "/v7/test-results/" + url.PathEscape(testID) + "/path-vis"
	target := endpoint(resource, aid) + "&window=" + strconv.FormatInt(int64(window/time.Second), 10) + "s"
	var out []ports.ObservedSource
	stopped := func(why error) ([]ports.ObservedSource, error) {
		return out, fmt.Errorf("thousandeyes adapter: path visualization of test %s: %w", testID, why)
	}
	for page := 0; target != ""; page++ {
		if page >= maxPages {
			return stopped(errPageLimit)
		}
		if u, err := url.Parse(target); err != nil || u.EscapedPath() != resource {
			return stopped(errPageOutside)
		}
		resp, body, err := c.send(ctx, target)
		if err != nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return stopped(errPageUnread)
		}
		var r struct {
			Results []struct {
				Agent struct {
					ID string `json:"agentId"`
				} `json:"agent"`
				Direction string `json:"direction"`
				SourceIP  string `json:"sourceIp"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return stopped(errPageDecode)
		}
		for _, one := range r.Results {
			out = append(out, ports.ObservedSource{AgentID: one.Agent.ID, Direction: one.Direction, Address: sourceAddr(one.SourceIP)})
		}
		target = nextLink(body)
	}
	return out, nil
}

// sourceAddr is the address in s, or the zero value when s is not one IP
// address. A zone is free text, so an address with one is refused.
func sourceAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}
	}
	return a.Unmap()
}
