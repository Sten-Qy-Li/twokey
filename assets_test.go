package twokey

import (
	"os"
	"strings"
	"testing"
)

// SPEC section 11 shows the config template; it must not drift from the
// file twokey init writes (DECISIONS.md D26).
func TestSpecShowsTheConfigTemplate(t *testing.T) {
	spec, err := os.ReadFile("docs/SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(spec), "This is the template `twokey init` writes")
	if !ok {
		t.Fatal("SPEC section 11 lost its template introduction")
	}
	_, block, ok := strings.Cut(after, "```toml\n")
	if !ok {
		t.Fatal("no ```toml block after the introduction")
	}
	block, _, ok = strings.Cut(block, "```\n")
	if !ok {
		t.Fatal("the ```toml block doesn't end")
	}
	if block != ConfigTemplate {
		t.Error("docs/SPEC.md section 11 and assets/config.toml differ. Copy assets/config.toml into the SPEC.")
	}
}
