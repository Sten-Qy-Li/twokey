package invariant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

type fixture struct {
	testutil.Repo
	taker Taker
}

func setup(t *testing.T) fixture {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, ".gitignore", "local-only_*\n")
	r.Commit(t, "ignore")
	testutil.Git(t, r.Dir, "push", "--quiet", "origin", "feature")
	r.WriteFile(t, "local-only_logs.md", "log\n")
	runDir := filepath.Join(r.Dir, "local-only_twokey", "runs", "x")
	r.WriteFile(t, "local-only_twokey/runs/x/state.json", "{}")
	return fixture{Repo: r, taker: Taker{
		Repo: gitx.Repo{Dir: r.Dir, R: proc.OS{}}, Remote: "origin", Branches: []string{"feature", "main"},
		LogPath: filepath.Join(r.Dir, "local-only_logs.md"), RunDir: runDir,
	}}
}

func (f fixture) snap(t *testing.T) Snapshot {
	t.Helper()
	s, err := f.taker.Take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func contains(p []string, want string) bool {
	for _, s := range p {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestNothingChanged(t *testing.T) {
	f := setup(t)
	before := f.snap(t)
	// Ignored files may change, and a fetch may move remote-tracking refs.
	f.WriteFile(t, "local-only_scratch.txt", "x")
	testutil.Git(t, f.Dir, "fetch", "--quiet", "origin")
	after := f.snap(t)
	if p := ReadOnly(before, after, nil); p != nil {
		t.Errorf("no violations expected: %v", p)
	}
	if before.Remote["main"] == "" || before.Remote["feature"] == "" {
		t.Errorf("remote SHAs: %v", before.Remote)
	}
}

func TestReadOnlyViolations(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, f fixture)
		want   string
	}{
		{"commit", func(t *testing.T, f fixture) { f.WriteFile(t, "a.txt", "a"); f.Commit(t, "x") }, "HEAD moved from"},
		{"edit a tracked file", func(t *testing.T, f fixture) { f.WriteFile(t, "README.md", "changed\n") }, "The working tree changed. git status now shows:\n   M README.md"},
		{"new untracked file", func(t *testing.T, f fixture) { f.WriteFile(t, "notes.txt", "x") }, "?? notes.txt"},
		{"switch branch", func(t *testing.T, f fixture) { testutil.Git(t, f.Dir, "checkout", "--quiet", "main") }, "The current branch changed from feature to main."},
		{"new branch", func(t *testing.T, f fixture) { testutil.Git(t, f.Dir, "branch", "side") }, "A new ref appeared: refs/heads/side."},
		{"new tag", func(t *testing.T, f fixture) { testutil.Git(t, f.Dir, "tag", "v1") }, "A new ref appeared: refs/tags/v1."},
		{"stash", func(t *testing.T, f fixture) {
			f.WriteFile(t, "README.md", "changed\n")
			testutil.Git(t, f.Dir, "stash", "--quiet")
		}, "A new ref appeared: refs/stash."},
		{"touch the log", func(t *testing.T, f fixture) { f.WriteFile(t, "local-only_logs.md", "log\nmore\n") }, "The log file changed."},
		{"push", func(t *testing.T, f fixture) {
			testutil.Git(t, f.Dir, "commit", "--quiet", "--allow-empty", "-m", "sneaky")
			testutil.Git(t, f.Dir, "push", "--quiet", "origin", "feature")
		}, "Branch feature changed on the remote"},
		{"edit twokey's file", func(t *testing.T, f fixture) { f.WriteFile(t, "local-only_twokey/runs/x/state.json", "{1}") }, "twokey's file state.json was changed."},
		{"new file in the run folder", func(t *testing.T, f fixture) { f.WriteFile(t, "local-only_twokey/runs/x/junk.txt", "x") }, "A new file appeared in twokey's run folder: junk.txt."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := setup(t)
			before := f.snap(t)
			c.change(t, f)
			if p := ReadOnly(before, f.snap(t), nil); !contains(p, c.want) {
				t.Errorf("got %q, want %q", p, c.want)
			}
		})
	}
}

func TestAllowedNewRunFiles(t *testing.T) {
	f := setup(t)
	before := f.snap(t)
	f.WriteFile(t, "local-only_twokey/runs/x/r1/codex-audit.stderr.log", "x")
	if p := ReadOnly(before, f.snap(t), []string{"r1/codex-audit.stderr.log"}); p != nil {
		t.Errorf("twokey's own new file is allowed: %v", p)
	}
}

func TestAfterFix(t *testing.T) {
	f := setup(t)
	before := f.snap(t)
	f.WriteFile(t, "fixed.txt", "x")
	f.Commit(t, "fix")
	if p := AfterFix(before, f.snap(t), "feature", nil); p != nil {
		t.Errorf("a clean commit on the branch is fine: %v", p)
	}

	f.WriteFile(t, "half.txt", "x")
	testutil.Git(t, f.Dir, "branch", "other")
	p := AfterFix(before, f.snap(t), "feature", nil)
	if !contains(p, "The fix left uncommitted changes.") || !contains(p, "A new ref appeared: refs/heads/other.") {
		t.Errorf("got %q", p)
	}
	if err := os.Remove(filepath.Join(f.Dir, "half.txt")); err != nil {
		t.Fatal(err)
	}
}
