package domain

import (
	"net/netip"
	"slices"
	"strings"
)

// Host is a Forward host and the addresses it answers on.
type Host struct {
	Name  string
	Addrs []netip.Addr
}

// Subnet is a Forward subnet, identified by prefix and VRF.
type Subnet struct {
	Prefix netip.Prefix
	VRF    string
}

// Inventory is the hosts and subnets of one pinned snapshot, read once.
type Inventory struct {
	Hosts   []Host
	Subnets []Subnet
}

// LocationKind says whether a location is a host or a subnet.
type LocationKind int

const (
	_              LocationKind = iota // the zero value is never a valid location
	HostLocation                       // identified by its name
	SubnetLocation                     // identified by its prefix and VRF
)

// Location is where an address sits in the Forward model.
type Location struct {
	Kind   LocationKind
	Name   string       // HostLocation
	Prefix netip.Prefix // SubnetLocation
	VRF    string       // SubnetLocation
}

// LocateStatus is the outcome of locating an agent.
type LocateStatus int

const (
	_         LocateStatus = iota // the zero value is never a valid status
	Located                       // exactly one location
	Ambiguous                     // more than one distinct location
	Unmapped                      // none
)

// LocateResult holds the single location, or the candidates when ambiguous.
type LocateResult struct {
	Status     LocateStatus
	Location   Location
	Candidates []Location
}

// Locate finds where an agent's addresses sit. For each address an exact host
// match comes first; otherwise the most specific subnet that contains it, and
// two such subnets of one length in different VRFs are both candidates. The
// agent is located when its addresses give exactly one distinct location.
func Locate(inv Inventory, addrs []netip.Addr) LocateResult {
	var found []Location
	add := func(l Location) {
		if !slices.Contains(found, l) {
			found = append(found, l)
		}
	}
	for _, a := range addrs {
		hosts := 0
		for _, h := range inv.Hosts {
			if slices.Contains(h.Addrs, a) {
				add(Location{Kind: HostLocation, Name: h.Name})
				hosts++
			}
		}
		if hosts > 0 {
			continue
		}
		best := -1
		var bestLocs []Location
		for _, s := range inv.Subnets {
			if !s.Prefix.Contains(a) {
				continue
			}
			switch bits := s.Prefix.Bits(); {
			case bits > best:
				best = bits
				bestLocs = []Location{{Kind: SubnetLocation, Prefix: s.Prefix, VRF: s.VRF}}
			case bits == best:
				l := Location{Kind: SubnetLocation, Prefix: s.Prefix, VRF: s.VRF}
				if !slices.Contains(bestLocs, l) {
					bestLocs = append(bestLocs, l)
				}
			}
		}
		for _, l := range bestLocs {
			add(l)
		}
	}
	switch len(found) {
	case 0:
		return LocateResult{Status: Unmapped}
	case 1:
		return LocateResult{Status: Located, Location: found[0]}
	}
	slices.SortFunc(found, compareLocations)
	return LocateResult{Status: Ambiguous, Candidates: found}
}

func compareLocations(a, b Location) int {
	if a.Kind != b.Kind {
		return int(a.Kind) - int(b.Kind)
	}
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if c := a.Prefix.Addr().Compare(b.Prefix.Addr()); c != 0 {
		return c
	}
	if c := a.Prefix.Bits() - b.Prefix.Bits(); c != 0 {
		return c
	}
	return strings.Compare(a.VRF, b.VRF)
}
