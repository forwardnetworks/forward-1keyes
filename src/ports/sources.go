package ports

import (
	"context"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// ObservedSource is one result that says where a test was sent from.
type ObservedSource = domain.ObservedSource

// SourceAddressReader reads the source addresses observed for a test. It is
// apart from TeClient, which record and replay serve: a recording holds none.
type SourceAddressReader interface {
	// SourceAddresses reads every page inside the window. An error means that
	// not all was read; what was read before it comes back with it. A rate
	// limit is an error at once: it is not waited for.
	SourceAddresses(ctx context.Context, aid, testID string, window time.Duration) ([]ObservedSource, error)
}
