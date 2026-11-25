package compositionroot

import "os"

// OSFS is the real filesystem. Files it writes are readable by their owner only:
// a plan file holds the checks that will be written to Forward.
type OSFS struct{}

// ReadFile reads a file.
func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// WriteFile writes a file with mode 0600.
func (OSFS) WriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }
