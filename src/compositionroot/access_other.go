//go:build !(linux || darwin || freebsd)

package compositionroot

import (
	"fmt"
	"os"
)

// writableByKernel is the mode bits only where access(2) is not used: a
// directory that nobody can write is refused, anything else is accepted and the
// write itself fails loudly if the filesystem refuses it.
func writableByKernel(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("read-only (mode %v)", info.Mode().Perm())
	}
	return nil
}
