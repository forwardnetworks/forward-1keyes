package compositionroot

import (
	"fmt"
	"path/filepath"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
)

// stateFS is the filesystem a state file lives on: it is read whole or refused,
// and written whole or not at all.
type stateFS interface {
	ReadState(path string, limit int64) (data []byte, exists bool, err error)
	WriteAtomic(path string, data []byte, force bool) error
}

// readState reads and checks the file --state names. It returns nil when no
// file is there yet. A file that cannot be used is an error, never "no state".
func (r Runner) readState(o options) (*domain.State, error) {
	if o.state == "" {
		return nil, nil
	}
	fsys, ok := r.FS.(stateFS)
	if !ok {
		return nil, fmt.Errorf("this filesystem cannot keep a state file")
	}
	raw, exists, err := fsys.ReadState(o.state, domain.MaxStateBytes)
	if err != nil {
		return nil, fmt.Errorf("reading the state file: %w", err)
	}
	if !exists {
		return nil, nil
	}
	s, err := domain.ParseState(raw)
	if err != nil {
		return nil, fmt.Errorf("the state file %s is refused: %w", o.state, err)
	}
	return &s, nil
}

// writeState remembers that the run finished with the latest snapshot.
func (r Runner) writeState(path string, c domain.Changes) error {
	fsys, ok := r.FS.(stateFS)
	if !ok {
		return fmt.Errorf("this filesystem cannot keep a state file")
	}
	raw, err := domain.NewState(c.NetworkID, c.AccountGroupID, c.To, ToolVersion, r.now()).Marshal()
	if err != nil {
		return err
	}
	if err := fsys.WriteAtomic(path, raw, true); err != nil {
		return fmt.Errorf("writing the state file: %w", err)
	}
	return nil
}

// checkStateWritable refuses, before anything is read from the network, a state
// file that the run could not write at the end.
func (r Runner) checkStateWritable(o options) error {
	if o.state == "" {
		return nil
	}
	w, ok := r.FS.(interface{ WritableDir(dir string) error })
	if !ok {
		return fmt.Errorf("this filesystem cannot check that the state file can be written")
	}
	if err := w.WritableDir(filepath.Dir(o.state)); err != nil {
		return fmt.Errorf("the state file cannot be written, so nothing was read or sent: %w", err)
	}
	return nil
}
