// Command twokey runs a cross-checked audit-and-fix loop between two
// coding agents on one repository. See docs/SPEC.md.
package main

import (
	"os"

	"github.com/Sten-Qy-Li/twokey/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
