// Package testutil builds throwaway git repositories for tests. Tests never
// touch a real project, a real remote or the developer's git config.
package testutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// IsolateGit makes git ignore the developer's global and system config
// (for example core.autocrlf on Windows) for the rest of the test.
func IsolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "twokey test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "twokey test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

// Git runs git in dir and returns stdout. It fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimRight(string(out), "\r\n")
}

// Repo is a working clone with a bare "remote" next to it.
type Repo struct {
	Dir    string // the working clone
	Remote string // the bare repository that "origin" points to
}

// NewRepo makes a bare remote and a clone. main has one commit; branch
// (if not "main") is created from it. Both are pushed, and the clone is
// left on branch with a clean tree.
func NewRepo(t *testing.T, branch string) Repo {
	t.Helper()
	IsolateGit(t)
	base := t.TempDir()
	r := Repo{Dir: filepath.Join(base, "work"), Remote: filepath.Join(base, "remote.git")}

	Git(t, base, "init", "--quiet", "--bare", "--initial-branch=main", r.Remote)
	Git(t, base, "clone", "--quiet", r.Remote, r.Dir)
	Git(t, r.Dir, "checkout", "--quiet", "-B", "main")
	r.WriteFile(t, "README.md", "test project\n")
	r.Commit(t, "first commit")
	Git(t, r.Dir, "push", "--quiet", "origin", "main")
	// Record origin/HEAD, as a normal clone of a non-empty remote would.
	Git(t, r.Dir, "remote", "set-head", "origin", "main")
	if branch != "main" {
		Git(t, r.Dir, "checkout", "--quiet", "-b", branch)
		Git(t, r.Dir, "push", "--quiet", "-u", "origin", branch)
	}
	return r
}

// WriteFile writes a file inside the clone, making folders as needed.
func (r Repo) WriteFile(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(r.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Commit stages everything and commits.
func (r Repo) Commit(t *testing.T, msg string) {
	t.Helper()
	Git(t, r.Dir, "add", "--all")
	Git(t, r.Dir, "commit", "--quiet", "-m", msg)
}
