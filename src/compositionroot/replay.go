package compositionroot

import (
	"context"
	"fmt"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/usecases"
)

// replaySource is a recording given with --te-replay, checked, with the mark
// that goes on everything made from it.
type replaySource struct {
	rec  domain.Recording
	mark domain.RecordingMark
}

// limitedFS is a filesystem that can refuse a file by size before it reads it.
type limitedFS interface {
	ReadFileLimited(path string, limit int64) ([]byte, error)
}

// recordFS is the filesystem a recording is written to: the file is all there
// or not there, and an existing one is replaced only on request.
type recordFS interface {
	Exists(path string) (bool, error)
	WriteAtomic(path string, data []byte, force bool) error
}

// loadReplay reads and checks the recording --te-replay names, or returns nil
// when it is not given. A group of "" skips the check of the account group.
// Everything that can refuse it is checked before anything is built or read.
func (r Runner) loadReplay(o options, group string) (*replaySource, error) {
	if o.replay == "" {
		return nil, nil
	}
	if o.resolver != "" {
		return nil, usageError{"--resolver cannot be used with --te-replay: a replay looks no name up"}
	}
	var raw []byte
	var err error
	if l, ok := r.FS.(limitedFS); ok {
		raw, err = l.ReadFileLimited(o.replay, domain.MaxRecordingBytes)
	} else {
		raw, err = r.FS.ReadFile(o.replay)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the recording: %w", err)
	}
	rec, mark, err := domain.ParseRecording(raw)
	if err != nil {
		return nil, fmt.Errorf("the recording %s is refused: %w", o.replay, err)
	}
	if group != "" {
		if err := rec.CheckGroup(group); err != nil {
			return nil, err
		}
	}
	return &replaySource{rec: rec, mark: mark}, nil
}

// wire makes the recording the only source of ThousandEyes data and lookups.
func (rp *replaySource) wire(cfg *Config) {
	if rp == nil {
		return
	}
	cfg.Recording = &rp.rec
	cfg.ResolverSetting = "recording"
}

// markOrNil is the mark of the recording, or nil for live data.
func (rp *replaySource) markOrNil() *domain.RecordingMark {
	if rp == nil {
		return nil
	}
	m := rp.mark
	return &m
}

// holds says whether a replay can answer for a test; live data always can.
func (rp *replaySource) holds(testID string) error {
	if rp == nil {
		return nil
	}
	return rp.rec.Holds(testID)
}

// window is the window a report reads. A replay measures it back from the time
// of the recording: with no --window it is the recorded one, and a longer one
// is refused.
func (rp *replaySource) window(o options) (time.Duration, error) {
	if rp == nil {
		return o.window, nil
	}
	recorded := rp.rec.WindowDuration()
	switch {
	case !o.windowSet:
		return recorded, nil
	case o.window > recorded:
		return 0, fmt.Errorf("--window %s is longer than the %s window of the recording", o.window, recorded)
	}
	return o.window, nil
}

// record reads ThousandEyes and the hostnames of the selected tests, and writes
// what it read to a file, only when every read succeeded.
func (r Runner) record(o options) (int, error) {
	if o.window < time.Second {
		return 0, usageError{"--window must be at least one second"}
	}
	fsys, ok := r.FS.(recordFS)
	if !ok {
		return 0, fmt.Errorf("this filesystem cannot write a recording")
	}
	if !o.force {
		exists, err := fsys.Exists(o.recordOut)
		if err != nil {
			return 0, err
		}
		if exists {
			return 0, fmt.Errorf("%s exists: use --force to replace it", o.recordOut)
		}
	}
	cfg, err := r.config("record", true, o)
	if err != nil {
		return 0, err
	}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	rec, err := usecases.Record(context.Background(), usecases.RecordDeps{TE: deps.TE, Resolver: deps.Resolver}, usecases.RecordParams{
		AccountGroup: o.group, Window: o.window, ToolVersion: ToolVersion, RecordedAt: r.now(),
		Selection: domain.Selection{TestIDs: o.testIDs, IncludeLabels: o.includeLabels, ExcludeLabels: o.excludeLabels, IncludeNames: o.includeNames, ExcludeNames: o.excludeNames},
	})
	if err != nil {
		return 0, err
	}
	raw, err := rec.Marshal()
	if err != nil {
		return 0, err
	}
	if err := fsys.WriteAtomic(o.recordOut, raw, o.force); err != nil {
		return 0, fmt.Errorf("writing the recording: %w", err)
	}
	fmt.Fprintf(r.IO.Out, "recorded %d tests, %d agents and %d lookups of account group %s at %s into %s\n",
		len(rec.Tests), len(rec.Agents), len(rec.Lookups), rec.AccountGroup.Name, rec.RecordedAt.Format(time.RFC3339), o.recordOut)
	return 0, nil
}
