package compositionroot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// OSFS is the real filesystem. Files it writes are readable by their owner only:
// a plan file holds the checks that will be written to Forward.
type OSFS struct{}

// ReadFile reads a file.
func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// WriteFile writes a file with mode 0600.
func (OSFS) WriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

// ReadFileLimited reads a file and fails, having read no more than limit and
// one byte, when it is longer than limit.
func (OSFS) ReadFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is larger than %d bytes", limit)
	}
	return data, nil
}

// Exists says whether something is at path.
func (OSFS) Exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// WriteAtomic writes a file with mode 0600 so that it is all there or not
// there: the bytes go to a temporary file beside it, which then takes its
// place. Without force, an existing file is never replaced.
func (OSFS) WriteAtomic(path string, data []byte, force bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		keep = true
		if err := os.Rename(name, path); err != nil {
			keep = false
			return err
		}
		return nil
	}
	// A hard link fails when the name is taken, so a file made since the
	// check is not replaced.
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s exists: use --force to replace it", path)
		}
		// Some filesystems (exFAT, certain network and FUSE mounts) cannot make a
		// hard link; --force writes by rename and does not need one.
		return fmt.Errorf("%w (if this filesystem does not support hard links, use --force)", err)
	}
	return nil
}
