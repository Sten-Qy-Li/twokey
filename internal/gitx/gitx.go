// Package gitx runs plain git commands for twokey's checks.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// Repo runs git in one folder.
type Repo struct {
	Dir string
	R   proc.Runner
}

// Error is a failed git command.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := firstLine(e.Stderr)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// gitEnv stops git from waiting for a password prompt nobody can answer.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0"}

func (g Repo) run(ctx context.Context, args ...string) proc.Result {
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	return g.R.Run(ctx, proc.Cmd{Dir: g.Dir, Name: "git", Args: full, Env: gitEnv})
}

// Output runs git and returns stdout without the final line break.
func (g Repo) Output(ctx context.Context, args ...string) (string, error) {
	r := g.run(ctx, args...)
	if r.Err != nil {
		return "", &Error{Args: args, ExitCode: r.ExitCode, Stderr: r.Stderr, Err: r.Err}
	}
	return strings.TrimRight(r.Stdout, "\r\n"), nil
}

// Version returns the output of "git --version".
func (g Repo) Version(ctx context.Context) (string, error) {
	return g.Output(ctx, "--version")
}

// TopLevel returns the repository's root folder.
func (g Repo) TopLevel(ctx context.Context) (string, error) {
	out, err := g.Output(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(out), nil
}

// ErrDetached means HEAD is not on a branch.
var ErrDetached = errors.New("HEAD is not on a branch")

// CurrentBranch returns the checked-out branch.
func (g Repo) CurrentBranch(ctx context.Context) (string, error) {
	r := g.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if r.Err != nil {
		if r.ExitCode == 1 {
			return "", ErrDetached
		}
		return "", &Error{Args: []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, ExitCode: r.ExitCode, Stderr: r.Stderr, Err: r.Err}
	}
	return strings.TrimSpace(r.Stdout), nil
}

// Status returns "git status --porcelain" output. Ignored files don't show.
func (g Repo) Status(ctx context.Context) (string, error) {
	return g.Output(ctx, "status", "--porcelain=v1", "--untracked-files=all")
}

// RevParse resolves a ref to a full commit SHA.
func (g Repo) RevParse(ctx context.Context, ref string) (string, error) {
	return g.Output(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// Fetch updates the remote-tracking branches of one remote.
func (g Repo) Fetch(ctx context.Context, remote string) error {
	_, err := g.Output(ctx, "fetch", "--quiet", "--no-tags", remote)
	return err
}

// RemoteURL returns the fetch URL of a remote.
func (g Repo) RemoteURL(ctx context.Context, remote string) (string, error) {
	return g.Output(ctx, "remote", "get-url", remote)
}

// AheadBehind counts commits in a but not b, and in b but not a.
func (g Repo) AheadBehind(ctx context.Context, a, b string) (ahead, behind int, err error) {
	out, err := g.Output(ctx, "rev-list", "--left-right", "--count", a+"..."+b)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("git rev-list: unexpected output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	if behind, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// LsRemoteBranch asks the remote for a branch's SHA. found is false if the
// remote has no such branch.
func (g Repo) LsRemoteBranch(ctx context.Context, remote, branch string) (sha string, found bool, err error) {
	out, err := g.Output(ctx, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "refs/heads/"+branch {
			return f[0], true, nil
		}
	}
	return "", false, nil
}

// RemoteDefaultBranch returns the branch the remote's HEAD points to, as
// recorded locally (refs/remotes/<remote>/HEAD).
func (g Repo) RemoteDefaultBranch(ctx context.Context, remote string) (string, error) {
	out, err := g.Output(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(out, remote+"/"), nil
}

// IsIgnored reports whether git ignores a path (relative to the repo root).
// The path doesn't need to exist. It also checks a name inside the path,
// so a folder rule such as "/local-only_twokey/" counts before the folder
// exists. A tracked file is never ignored.
func (g Repo) IsIgnored(ctx context.Context, path string) (bool, error) {
	for _, p := range []string{path, filepath.Join(path, "state.json")} {
		args := []string{"check-ignore", "--quiet", "--", filepath.ToSlash(p)}
		r := g.run(ctx, args...)
		switch {
		case r.Err == nil:
			return true, nil
		case r.ExitCode != 1:
			return false, &Error{Args: args, ExitCode: r.ExitCode, Stderr: r.Stderr, Err: r.Err}
		}
	}
	return false, nil
}

// Refs returns "git for-each-ref" output: one "refname sha" line per
// branch, tag and stash ref. Remote-tracking refs are left out: an agent's
// harmless "git fetch" changes them, and pushes are caught on the remote
// itself (DECISIONS.md D29).
func (g Repo) Refs(ctx context.Context) (string, error) {
	out, err := g.Output(ctx, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return "", err
	}
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "refs/remotes/") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n"), nil
}

// IsAncestor reports whether commit a is an ancestor of (or equal to) b.
func (g Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	args := []string{"merge-base", "--is-ancestor", a, b}
	r := g.run(ctx, args...)
	switch {
	case r.Err == nil:
		return true, nil
	case r.ExitCode == 1:
		return false, nil
	default:
		return false, &Error{Args: args, ExitCode: r.ExitCode, Stderr: r.Stderr, Err: r.Err}
	}
}

// CommitExists reports whether sha names a commit in the repository.
func (g Repo) CommitExists(ctx context.Context, sha string) bool {
	return g.run(ctx, "cat-file", "-e", sha+"^{commit}").Err == nil
}

// NewCommits lists the commits in head but not in base, oldest first.
func (g Repo) NewCommits(ctx context.Context, base, head string) ([]string, error) {
	out, err := g.Output(ctx, "rev-list", "--reverse", base+".."+head)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// Subject returns a commit's subject line.
func (g Repo) Subject(ctx context.Context, sha string) (string, error) {
	return g.Output(ctx, "log", "-1", "--format=%s", sha)
}

// Push pushes HEAD to one branch of the remote: no force, no push options.
func (g Repo) Push(ctx context.Context, remote, branch string) error {
	_, err := g.Output(ctx, "push", "--porcelain", remote, "HEAD:refs/heads/"+branch)
	return err
}

// LsRemoteBranches asks the remote for several branches at once. A branch
// the remote doesn't have maps to "".
func (g Repo) LsRemoteBranches(ctx context.Context, remote string, branches []string) (map[string]string, error) {
	args := []string{"ls-remote", "--heads", remote}
	for _, b := range branches {
		args = append(args, "refs/heads/"+b)
	}
	out, err := g.Output(ctx, args...)
	if err != nil {
		return nil, err
	}
	shas := map[string]string{}
	for _, b := range branches {
		shas[b] = ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if b, ok := strings.CutPrefix(f[1], "refs/heads/"); ok {
				if _, want := shas[b]; want {
					shas[b] = f[0]
				}
			}
		}
	}
	return shas, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
