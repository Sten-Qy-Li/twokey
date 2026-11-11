package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(t.Context(), args, Env{Dir: t.TempDir(), Out: &out, Err: &errb, Runner: proc.OS{}})
	return out.String(), errb.String(), code
}

func TestCommands(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string // in stdout or stderr
	}{
		{nil, 2, "Usage:"},
		{[]string{"help"}, 0, "Commands:"},
		{[]string{"version"}, 0, "twokey "},
		{[]string{"nope"}, 2, `unknown command "nope"`},
		{[]string{"run"}, 2, "Real agent runs arrive in the next milestone (M2)."},
		{[]string{"resume"}, 2, "Real agent runs arrive in the next milestone (M2)."},
		{[]string{"status"}, 2, "twokey status isn't built yet."},
		{[]string{"render"}, 2, "Usage: twokey render <audit|convergence|dispute|fix>"},
		{[]string{"unlock"}, 1, "This folder is not inside a git repository."},
		{[]string{"run", "--fake-agents", "x"}, 1, "This folder is not inside a git repository."},
		{[]string{"doctor", "--smoke"}, 2, "doctor --smoke isn't built yet."},
		{[]string{"doctor", "-h"}, 0, "Usage: twokey doctor [options]"},
		{[]string{"init", "extra"}, 2, `unexpected argument "extra"`},
		{[]string{"init", "--bogus"}, 2, "flag provided but not defined: -bogus"},
	}
	for _, c := range cases {
		out, errOut, code := run(t, c.args...)
		if code != c.code || !strings.Contains(out+errOut, c.want) {
			t.Errorf("twokey %v: exit %d, output:\n%s%s\nwant exit %d and %q", c.args, code, out, errOut, c.code, c.want)
		}
	}
}
