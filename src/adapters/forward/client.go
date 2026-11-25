// Package forward is the Forward adapter: it implements ports.ForwardClient on
// top of forward-go-sdk. It imports the ports and the SDK, never the domain.
package forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	sdk "github.com/forwardnetworks/forward-go-sdk"
	sdkports "github.com/forwardnetworks/forward-go-sdk/ports"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// Config configures the adapter. BaseURL must be https and HTTPClient, when
// set, is copied: the adapter installs its own redirect policy on the copy.
type Config struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
}

// Client implements ports.ForwardClient.
type Client struct {
	sdk *sdk.Client
}

var (
	_ ports.ForwardClient = (*Client)(nil)
	_ ports.ForwardReader = (*Client)(nil)
)

const maxRedirects = 5

// New builds the adapter. It refuses a URL that is not https before anything
// is sent, and it refuses every redirect that leaves the Forward host or drops
// to http, so credentials never go anywhere else.
func New(cfg Config) (*Client, error) {
	base, err := ports.ValidateForwardURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		hc = &copied
	}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("forward adapter: too many redirects")
		}
		if !ports.HostAllowed(base, req.URL.String()) {
			return fmt.Errorf("forward adapter: refusing a redirect to %s://%s", req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
	client, err := sdk.NewClient(sdk.Config{BaseURL: base, Username: cfg.Username, Password: cfg.Password, HTTPClient: hc})
	if err != nil {
		return nil, fmt.Errorf("forward adapter: %w", err)
	}
	return &Client{sdk: client}, nil
}

// LatestSnapshot returns the newest processed snapshot of the network.
func (c *Client) LatestSnapshot(ctx context.Context, networkID string) (string, error) {
	snap, _, err := c.sdk.Snapshots.LatestProcessed(ctx, networkID)
	if err != nil {
		return "", err
	}
	return string(snap.ID), nil
}

// ListChecks returns every check on the snapshot. Forward cannot filter by tag,
// so the caller does.
func (c *Client) ListChecks(ctx context.Context, snapshotID string) ([]ports.ObservedRecord, error) {
	checks, _, err := c.sdk.Checks.List(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	out := make([]ports.ObservedRecord, 0, len(checks))
	for _, ck := range checks {
		out = append(out, record(ck))
	}
	return out, nil
}

// CreateCheck writes one check and returns its ID. A non-persistent check
// lives on this snapshot only.
func (c *Client) CreateCheck(ctx context.Context, snapshotID string, spec ports.CheckSpec, persistent bool) (string, error) {
	nc := sdkports.NewCheck{Definition: spec.Definition, Name: spec.Name, Note: spec.Note, Tags: spec.Tags}
	if persistent {
		id, _, err := c.sdk.Checks.CreatePersistent(ctx, snapshotID, nc)
		return string(id), err
	}
	no := false
	detail, _, err := c.sdk.Checks.Create(ctx, snapshotID, nc, &no)
	if err != nil {
		return "", err
	}
	return string(detail.ID), nil
}

// GetCheck reads one check back.
func (c *Client) GetCheck(ctx context.Context, snapshotID, checkID string) (ports.ObservedRecord, error) {
	detail, _, err := c.sdk.Checks.Get(ctx, snapshotID, checkID)
	if err != nil {
		return ports.ObservedRecord{}, err
	}
	return record(detail.Check), nil
}

// NetworkByName returns the ID of the network with exactly this name. A name
// that is missing, or shared by two networks, is an error.
func (c *Client) NetworkByName(ctx context.Context, name string) (string, error) {
	networks, _, err := c.sdk.Networks.List(ctx)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, n := range networks {
		if n.Name == name {
			ids = append(ids, string(n.ID))
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("forward adapter: no network named %q", name)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("forward adapter: %d networks are named %q", len(ids), name)
}

// subnetQuery lists every interface address with its VRF. It was checked
// against Forward's NQE schema (fwdctl nqe lint) and run on a real snapshot.
const subnetQuery = `foreach d in network.devices
foreach i in d.interfaces
foreach s in i.subinterfaces
foreach a in s.ipv4.addresses
select {device: d.name, iface: i.name, vrf: s.networkInstanceName, ip: a.ip, len: a.prefixLength}`

const inventoryPage = 1000

// Inventory reads the connected subnets of one snapshot, with their VRFs, in
// one NQE query read page by page. Host bits are masked and duplicates removed.
func (c *Client) Inventory(ctx context.Context, snapshotID string) (ports.Inventory, error) {
	seen := map[ports.Subnet]bool{}
	var inv ports.Inventory
	for offset := int32(0); ; {
		limit, off := int32(inventoryPage), offset
		res, _, err := c.sdk.NQE.Run(ctx, "", snapshotID, sdkports.NQEQueryRequest{
			Query: subnetQuery, Options: &sdkports.NQEOptions{Limit: &limit, Offset: &off},
		})
		if err != nil {
			return ports.Inventory{}, fmt.Errorf("forward adapter: reading the inventory of snapshot %s: %w", snapshotID, err)
		}
		for _, row := range res.Items {
			subnet, ok := subnetOf(row)
			if ok && !seen[subnet] {
				seen[subnet] = true
				inv.Subnets = append(inv.Subnets, subnet)
			}
		}
		offset += int32(len(res.Items))
		if len(res.Items) == 0 || int64(offset) >= res.TotalNumItems {
			break
		}
	}
	slices.SortFunc(inv.Subnets, func(a, b ports.Subnet) int {
		if c := a.Prefix.Addr().Compare(b.Prefix.Addr()); c != 0 {
			return c
		}
		if c := a.Prefix.Bits() - b.Prefix.Bits(); c != 0 {
			return c
		}
		return strings.Compare(a.VRF, b.VRF)
	})
	return inv, nil
}

func subnetOf(row sdkports.NQERecord) (ports.Subnet, bool) {
	var ip, vrf string
	var bits int
	if json.Unmarshal(row["ip"], &ip) != nil || json.Unmarshal(row["vrf"], &vrf) != nil || json.Unmarshal(row["len"], &bits) != nil {
		return ports.Subnet{}, false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ports.Subnet{}, false
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return ports.Subnet{}, false
	}
	return ports.Subnet{Prefix: prefix, VRF: vrf}, true
}

// UpdateCheck rewrites a check's name, note, tags and definition in place.
func (c *Client) UpdateCheck(ctx context.Context, snapshotID, checkID string, spec ports.CheckSpec) error {
	patch := sdkports.CheckPatch{Definition: spec.Definition, Name: &spec.Name, Note: &spec.Note, Tags: spec.Tags}
	_, _, err := c.sdk.Checks.Update(ctx, snapshotID, checkID, patch)
	return err
}

func record(ck sdkports.Check) ports.ObservedRecord {
	hash, _ := ports.DefinitionHashJSON(ck.Definition)
	return ports.ObservedRecord{
		ID: string(ck.ID), Name: ck.Name, Note: ck.Note, Tags: ck.Tags,
		DefinitionHash: hash, Active: ck.Enabled != nil && *ck.Enabled,
	}
}
