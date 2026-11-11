// Package paths names twokey's own folders outside the repository.
package paths

import (
	"os"
	"path/filepath"
)

// DataDir is twokey's folder for files the agents must not see:
// %LOCALAPPDATA%\twokey on Windows, ~/.cache/twokey on Linux.
func DataDir() (string, error) {
	c, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "twokey"), nil
}

// StagingRoot holds each run's blind audit results until both audits are
// done (SPEC section 9).
func StagingRoot() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "staging"), nil
}

// ScratchRoot holds the agents' scratch folders: %TEMP%\twokey.
func ScratchRoot() string {
	return filepath.Join(os.TempDir(), "twokey")
}
