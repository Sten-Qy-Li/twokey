// Package version reports which twokey build is running.
package version

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"runtime/debug"
	"strings"
)

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

// release is a tag: vX.Y.Z, maybe with a pre-release such as -rc.8.
// Go gives a build of an untagged commit a pseudo-version
// (v0.2.1-0.20261113150000-156bb60ad32f), whose hyphen after the time
// doesn't fit, and adds "+dirty" for uncommitted changes.
var release = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`)

// Release reports whether v is a tagged release, not a development build
// (D68).
func Release(v string) bool { return release.MatchString(v) }

// OfFile returns the version of the twokey program in file, as String
// prints it there, without running it. Go stamps the tag into a build of
// a clean checkout of a tagged commit, and into "go install ...@<tag>".
func OfFile(file string) (string, error) {
	bi, err := buildinfo.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s doesn't exist", file)
	case err != nil || !strings.HasSuffix(bi.Path, "/cmd/twokey"):
		return "", fmt.Errorf("%s isn't a twokey program", file)
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v, nil
	}
	return "dev", nil
}
