package main

import (
	"os"

	"github.com/openshift-online/openshellctl/internal/cli"
	buildinfo "github.com/openshift-online/openshellctl/internal/version"
)

// Build metadata injected via -ldflags (see the Makefile): the linker targets
// these main-package symbols per spec §4. They are copied into internal/version
// before the command runs so the rest of the code has a single accessor.
var (
	version       = "dev"
	commit        = "none"
	openshellPin  = ""
	installMethod = ""
)

func main() {
	buildinfo.Set(version, commit, openshellPin, installMethod)
	os.Exit(cli.Execute())
}
