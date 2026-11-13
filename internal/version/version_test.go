package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.1.0":                               true,
		"v0.2.0-rc.8":                          true,
		"v1.10.3-beta":                         true,
		"v0.2.1-0.20261113150000-156bb60ad32f": false, // an untagged commit
		"v0.2.0-rc.8.0.20261113150000-156bb60ad32f": false,
		"v0.0.0-20261113150000-156bb60ad32f":        false,
		"v0.2.0-rc.8+dirty":                         false, // uncommitted changes
		"dev":                                       false,
		"(devel)":                                   false,
		"":                                          false,
		"0.1.0":                                     false,
	} {
		if got := Release(v); got != want {
			t.Errorf("Release(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestOfFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := OfFile(filepath.Join(dir, "twokey.exe")); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("a missing file: %v", err)
	}
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OfFile(text); err == nil || !strings.Contains(err.Error(), "isn't a twokey program") {
		t.Errorf("a text file: %v", err)
	}
	// The test binary is a Go program, but not twokey.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OfFile(self); err == nil || !strings.Contains(err.Error(), "isn't a twokey program") {
		t.Errorf("the test binary: %v", err)
	}
}
