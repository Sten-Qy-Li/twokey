// Package version reports which twokey build is running.
package version

import "runtime/debug"

// Version is set for release builds:
//
//	-ldflags "-X github.com/Sten-Qy-Li/twokey/internal/version.Version=v0.1.0"
var Version = ""

// String returns the release version, the module version that
// "go install" or "go build" recorded, or "dev".
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
