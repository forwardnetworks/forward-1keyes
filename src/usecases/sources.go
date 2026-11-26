package usecases

import (
	"context"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// SourceWarning reads what was observed for the tests in which an agent gives
// more than one source, and returns the warning, or "" when there is none. It
// is for plan alone and changes nothing in the plan. A test that cannot be
// read is disclosed in the warning and is never an error. With a recording, or
// no reader, nothing is read.
func SourceWarning(ctx context.Context, reader ports.SourceAddressReader, aid string, found domain.SourceReport) string {
	if found.Recording {
		return found.Warning()
	}
	tests, unread := domain.SourceTests(found.Agents)
	found.Unread = unread
	found.Reads = map[string]domain.SourceRead{}
	for _, test := range tests {
		var read domain.SourceRead
		if reader != nil {
			sources, err := reader.SourceAddresses(ctx, aid, test, domain.SourceWindow)
			read = domain.SourceRead{Sources: sources, Complete: err == nil}
		}
		found.Reads[test] = read
	}
	return found.Warning()
}
