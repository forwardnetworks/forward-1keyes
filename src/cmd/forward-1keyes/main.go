// Command forward-1keyes turns the tests in a ThousandEyes account into
// Forward intent checks: plan, apply and status.
package main

import (
	"os"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/compositionroot"
)

func main() {
	os.Exit(compositionroot.Runner{
		Env:   os.Getenv,
		IO:    compositionroot.IO{Out: os.Stdout, Err: os.Stderr},
		FS:    compositionroot.OSFS{},
		Now:   time.Now,
		Build: compositionroot.DefaultBuild,
	}.Run(os.Args[1:]))
}
