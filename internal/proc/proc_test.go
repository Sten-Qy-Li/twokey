package proc

import (
	"runtime"
	"strings"
	"testing"
)

func TestShell(t *testing.T) {
	dir := t.TempDir()
	line := `echo "two words" && exit 3`
	r := OS{}.Run(t.Context(), Cmd{Dir: dir, Shell: line})
	if r.ExitCode != 3 || r.Err == nil {
		t.Errorf("exit code %d, err %v", r.ExitCode, r.Err)
	}
	want := "two words"
	if runtime.GOOS == "windows" {
		want = `"two words"` // cmd's echo keeps the quotes
	}
	if !strings.Contains(r.Stdout, want) {
		t.Errorf("stdout %q lacks %q", r.Stdout, want)
	}

	ok := OS{}.Run(t.Context(), Cmd{Dir: dir, Shell: "git --version"})
	if ok.Err != nil || !strings.HasPrefix(ok.Stdout, "git version") {
		t.Errorf("git through the shell: %+v", ok)
	}
}
