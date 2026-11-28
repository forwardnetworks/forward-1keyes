package ports

import (
	"context"
	"net/netip"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// Domain types the ThousandEyes and Forward-reading ports speak in.
type (
	AccountGroup = domain.AccountGroup
	TestSummary  = domain.TestSummary
	TestConfig   = domain.TestConfig
	AgentInfo    = domain.AgentInfo
	AgentKind    = domain.AgentKind
	// AgentCondition is what is kept of an agent that is not online.
	AgentCondition = domain.AgentCondition
	Inventory      = domain.Inventory
	Subnet         = domain.Subnet
)

// ConditionOf normalizes the state and last-seen time ThousandEyes gives for
// an agent, re-exported so adapters need not import the domain.
func ConditionOf(state, lastSeen string) AgentCondition {
	return domain.ConditionOf(state, lastSeen)
}

// Agent kinds, re-exported so adapters need not import the domain.
const (
	AgentCloud      = domain.AgentCloud
	AgentEnterprise = domain.AgentEnterprise
	AgentCluster    = domain.AgentCluster
)

// TeClient is what the tool reads from ThousandEyes. It has no write method:
// plan can only read. Every call names the account group by its aid.
type TeClient interface {
	AccountGroups(ctx context.Context) ([]AccountGroup, error)
	ListTests(ctx context.Context, aid string) ([]TestSummary, error)
	// GetTest reads the detail of a test of a supported type.
	GetTest(ctx context.Context, aid string, summary TestSummary) (TestConfig, error)
	ListAgents(ctx context.Context, aid string) ([]AgentInfo, error)
}

// Resolver looks a hostname up once.
type Resolver interface {
	Lookup(ctx context.Context, name string) ([]netip.Addr, error)
}

// InternetNodeReader is an optional read a Forward reader may also offer: how
// many connections the network's internet synthetic node has. A reader that
// does not offer it leaves the reason for a cloud-agent flow generic.
type InternetNodeReader interface {
	InternetConnections(ctx context.Context, networkID string) (int, error)
}

// ForwardReader is what plan reads from Forward. It has no write method.
type ForwardReader interface {
	// NetworkByName returns the ID of the network with exactly this name.
	NetworkByName(ctx context.Context, name string) (string, error)
	LatestSnapshot(ctx context.Context, networkID string) (string, error)
	// Inventory reads the hosts and subnets of one snapshot, in one pass.
	Inventory(ctx context.Context, snapshotID string) (Inventory, error)
	ListChecks(ctx context.Context, snapshotID string) ([]ObservedRecord, error)
}
