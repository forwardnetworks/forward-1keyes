//go:build linux || darwin || freebsd

package compositionroot

import "syscall"

// writableByKernel asks access(2) whether dir can be written, which looks at the
// owner, the mode, ACLs and a read-only mount, and changes nothing.
func writableByKernel(dir string) error { return syscall.Access(dir, 2) }
