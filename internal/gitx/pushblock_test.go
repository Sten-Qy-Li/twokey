package gitx_test

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

func TestPushBlockEnv(t *testing.T) {
	none := func(string) string { return "" }
	want := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url.twokey-agents-may-not-push::.pushInsteadOf", "GIT_CONFIG_VALUE_0="}
	if got := gitx.PushBlockEnv(none); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	two := func(k string) string {
		if k == "GIT_CONFIG_COUNT" {
			return "2"
		}
		return ""
	}
	want = []string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_2=url.twokey-agents-may-not-push::.pushInsteadOf", "GIT_CONFIG_VALUE_2="}
	if got := gitx.PushBlockEnv(two); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// git runs git as an agent does: with the push block in its environment.
func agentGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitx.PushBlockEnv(os.Getenv)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// In the agents' environment git push fails, by remote name and by URL,
// while fetch and ls-remote still work (DECISIONS.md D62).
func TestAgentsCantPush(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, "a.txt", "a")
	r.Commit(t, "Local work")
	before := testutil.Git(t, r.Remote, "for-each-ref", "--format=%(refname) %(objectname)")

	for _, args := range [][]string{
		{"push", "origin", "HEAD:refs/heads/feature"},
		{"push", "origin", "HEAD:refs/heads/main"},
		{"push", "--no-verify", "--force", "origin", "HEAD:refs/heads/other"},
		{"push", r.Remote, "HEAD:refs/heads/feature"},
	} {
		out, err := agentGit(t, r.Dir, args...)
		if err == nil || !strings.Contains(out, "twokey-agents-may-not-push") {
			t.Errorf("git %s: err %v, output:\n%s", strings.Join(args, " "), err, out)
		}
	}
	if out, err := agentGit(t, r.Dir, "fetch", "origin"); err != nil {
		t.Errorf("fetch: %v\n%s", err, out)
	}
	if out, err := agentGit(t, r.Dir, "ls-remote", "origin", "refs/heads/main"); err != nil || !strings.Contains(out, "refs/heads/main") {
		t.Errorf("ls-remote: %v\n%s", err, out)
	}
	if out, _ := agentGit(t, r.Dir, "remote", "get-url", "--push", "origin"); !gitx.PushBlocked(out) {
		t.Errorf("push URL: %s", out)
	}
	if after := testutil.Git(t, r.Remote, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Errorf("the remote changed:\n%s\n->\n%s", before, after)
	}

	// twokey's own git, without the block, still pushes.
	testutil.Git(t, r.Dir, "push", "--quiet", "origin", "HEAD:refs/heads/feature")
}

// A pushurl or another pushInsteadOf would get a push past the block, so
// twokey looks for both.
func TestPushBypasses(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	g := gitx.Repo{Dir: r.Dir, R: proc.OS{}}
	if got, err := g.PushBypasses(t.Context()); err != nil || got != nil {
		t.Fatalf("clean repo: %q, %v", got, err)
	}

	testutil.Git(t, r.Dir, "config", "remote.origin.pushurl", r.Remote)
	if out, err := agentGit(t, r.Dir, "push", "origin", "HEAD:refs/heads/viapushurl"); err != nil {
		t.Fatalf("a pushurl should get past the block (that's why twokey checks for it): %v\n%s", err, out)
	}
	testutil.Git(t, r.Dir, "config", "--global", "url."+r.Remote+".pushInsteadOf", r.Remote)
	got, err := g.PushBypasses(t.Context())
	if err != nil || len(got) != 2 || !strings.HasPrefix(got[0], "url.") || !strings.HasPrefix(got[1], "remote.origin.pushurl ") {
		t.Errorf("got %q, %v", got, err)
	}
	if h := gitx.UnsetHint(got[1]); !strings.HasPrefix(h, "git config --unset-all remote.origin.pushurl") {
		t.Errorf("hint: %s", h)
	}
}
