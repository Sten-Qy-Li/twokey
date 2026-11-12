package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

// The tests for [project] upstream (D39, D57: twokey compares, never moves
// the branch) and for protected branches that someone else moves (D40). In
// newWorld, feature is one commit ahead of main.

// mergeOnRemote merges one branch of the remote into another with a merge
// commit, as a merge request does, and returns the merge commit.
func (w *world) mergeOnRemote(from, into string) string {
	w.t.Helper()
	dir := w.repo.Clone(w.t)
	testutil.Git(w.t, dir, "checkout", "--quiet", into)
	testutil.Git(w.t, dir, "merge", "--quiet", "--no-ff", "--no-edit", "-m", fmt.Sprintf("Merge branch '%s' into '%s'", from, into), "origin/"+from)
	testutil.Git(w.t, dir, "push", "--quiet", "origin", into)
	return testutil.Git(w.t, dir, "rev-parse", "HEAD")
}

// pushOnRemote pushes a teammate's commit to a branch of the remote.
func (w *world) pushOnRemote(branch, msg string) string {
	w.t.Helper()
	dir := w.repo.Clone(w.t)
	testutil.Git(w.t, dir, "checkout", "--quiet", branch)
	testutil.Git(w.t, dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	testutil.Git(w.t, dir, "push", "--quiet", "origin", branch)
	return testutil.Git(w.t, dir, "rev-parse", "HEAD")
}

func (w *world) remoteSHA(branch string) string {
	w.t.Helper()
	sha, _, _ := strings.Cut(testutil.Git(w.t, w.repo.Dir, "ls-remote", "origin", "refs/heads/"+branch), "\t")
	return sha
}

func (w *world) head() string {
	w.t.Helper()
	return testutil.Git(w.t, w.repo.Dir, "rev-parse", "HEAD")
}

func (w *world) report() string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.runDir(), "report.md"))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

// Behind: twokey says how far, and audits the branch as it is. It never
// moves the branch (D57).
func TestUpstreamBehindIsOnlyReported(t *testing.T) {
	w := newWorld(t)
	w.cfg.Project.Upstream = "main"
	w.mergeOnRemote("feature", "main")
	tip := w.pushOnRemote("main", "Teammate's feature")
	head := w.head()
	w.cleanRound(1)
	w.expect(w.start(""), engine.ExitDone)

	if c := w.state().Rounds[0].Commit; c != head || w.head() != head || w.remoteSHA("feature") != head || w.remoteSHA("main") != tip {
		t.Errorf("nothing may move: round 1 audits %s, HEAD is %s, origin/feature is %s, origin/main is %s", c, w.head(), w.remoteSHA("feature"), w.remoteSHA("main"))
	}
	w.outHas("[WARN]  origin/feature is 2 commits behind origin/main. twokey doesn't move feature: this round audits it as it is.\n" +
		"        Next: to bring those commits in, run git merge --ff-only origin/main and git push yourself.")
	for _, name := range []string{"codex-audit", "claude-audit", "convergence"} {
		prompt, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", name+".prompt.md"))
		want := fmt.Sprintf("Upstream branch: origin/main. When this round started, it was %s, 2 commits ahead of the commit you audit (%s). "+
			"A deployment built from origin/main may include changes that this commit lacks.", tip[:12], head[:12])
		if !strings.Contains(string(prompt), want) {
			t.Errorf("the %s prompt lacks %q", name, want)
		}
	}
	if report := w.report(); strings.Contains(report, "fast-forward") || strings.Contains(report, "**Next:**") {
		t.Errorf("report:\n%s", report)
	}
}

// Equal or ahead: nothing moves. The audits learn how the commit stands to
// the upstream (D42), and an ahead branch gets the advice to merge it.
func TestUpstreamNothingToDo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(w *world)
		want   string
		prompt string
		advice bool
	}{
		{"ahead", func(*world) {}, "origin/feature is 1 commit ahead of origin/main.",
			"and the commit you audit (%s) was 1 commit ahead of it. A deployment built from origin/main doesn't include those commits yet.", true},
		{"equal", func(w *world) {
			testutil.Git(w.t, w.repo.Clone(w.t), "push", "--quiet", "origin", "origin/feature:refs/heads/main")
		}, "origin/feature is even with origin/main.",
			"it was the same commit as the one you audit (%s).", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.cfg.Project.Upstream = "main"
			tc.setup(w)
			head, mainSHA := w.head(), w.remoteSHA("main")
			w.cleanRound(1)
			w.expect(w.start(""), engine.ExitDone)
			if w.head() != head || w.remoteSHA("feature") != head || w.remoteSHA("main") != mainSHA {
				t.Error("nothing may move")
			}
			w.outHas(tc.want)
			prompt, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "claude-audit.prompt.md"))
			if want := fmt.Sprintf(tc.prompt, head[:12]); !strings.Contains(string(prompt), want) {
				t.Errorf("the audit prompt lacks %q:\n%s", want, prompt)
			}
			const advice = "merge feature into main (merge commit, not squash). Then run git merge --ff-only origin/main and git push yourself."
			report := w.report()
			if got := strings.HasSuffix(report, "\n**Next:** "+advice+"\n"); got != tc.advice {
				t.Errorf("report ends with the advice: %v, want %v:\n%s", got, tc.advice, report)
			}
			if strings.Contains(w.out.String(), advice) != tc.advice {
				t.Errorf("the terminal shows the advice: want %v", tc.advice)
			}
		})
	}
}

func TestUpstreamOffInPrompts(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.expect(w.start(""), engine.ExitDone)
	prompt, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "codex-audit.prompt.md"))
	if !strings.Contains(string(prompt), "Upstream branch: none is set, so twokey doesn't compare this commit with another branch.") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

// Diverged: a warning with what to do, no pause. The audits learn how the
// commit stands to the upstream, and the report ends with the advice.
func TestUpstreamDivergedIsOnlyReported(t *testing.T) {
	w := newWorld(t)
	w.cfg.Project.Upstream = "main"
	tip := w.pushOnRemote("main", "Teammate's work")
	head := w.head()
	w.cleanRound(1)
	w.expect(w.start(""), engine.ExitDone)
	if w.head() != head || w.remoteSHA("feature") != head || w.remoteSHA("main") != tip {
		t.Error("nothing may move")
	}
	w.outHas("[WARN]  origin/feature and origin/main have diverged: origin/feature has 1 commit that origin/main lacks")
	w.outHas("Next: merge feature into main through a merge request with a merge commit (not squash: a squash merge keeps them diverged).")
	prompt, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "codex-audit.prompt.md"))
	if want := fmt.Sprintf("it was %s, and it had diverged from the commit you audit (%s): this commit has 1 commit that origin/main lacks, "+
		"and origin/main has 1 commit that this commit lacks.", tip[:12], head[:12]); !strings.Contains(string(prompt), want) {
		t.Errorf("the audit prompt lacks %q:\n%s", want, prompt)
	}
	if !strings.HasSuffix(w.report(), "**Next:** merge feature into main (merge commit, not squash). Then run git merge --ff-only origin/main and git push yourself.\n") {
		t.Errorf("report:\n%s", w.report())
	}
}

// The whole story: a teammate's merge request lands on main (protected,
// and the upstream) while the fix runs. twokey can't tell it from an
// agent's push, so it asks. Round 2 then audits the fix as it is, with a
// warning that the branch and main have diverged.
func TestUpstreamMovesDuringARound(t *testing.T) {
	w := newWorld(t)
	w.cfg.Project.Upstream = "main"
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"fixed.txt": "fixed\n"}, Commit: "Fix R1-M-01",
		TeammatePush: "Merge branch 'teammate' into 'main'", TeammateBranch: "main"})
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.cleanRound(2)

	w.expect(w.start(""), engine.ExitPaused)
	st := w.state()
	if st.Pause.Reason != "PAUSE_PROTECTED_MOVED" || st.Pause.Protected["main"] != w.remoteSHA("main") {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas("Merge branch 'teammate' into 'main' (twokey test)")
	w.outHas("twokey can't tell a teammate's merge from an agent's push")

	w.expect(w.resume("y\n", 0), engine.ExitDone)
	st = w.state()
	if st.Status != state.Converged || len(st.Rounds) != 2 || !st.Rounds[0].Pushed || st.Rounds[1].Commit != w.head() || w.remoteSHA("feature") != w.head() {
		t.Fatalf("after the confirmation: status %s, %d rounds", st.Status, len(st.Rounds))
	}
	w.outHas("[WARN]  origin/feature and origin/main have diverged")
	if want := "Round 1: you confirmed that main moved on origin during round 1's fix step"; !strings.Contains(w.report(), want) {
		t.Errorf("report lacks %q:\n%s", want, w.report())
	}
}

// A protected branch moves during an agent step, with upstream off:
// twokey pauses until the owner confirms, then publishes the step's
// answer and goes on.
func TestProtectedBranchMovedBySomeoneElse(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "convergence", runner.Actions{TeammatePush: "Teammate's merge", TeammateBranch: "main"})
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	oldMain := w.remoteSHA("main")

	w.expect(w.start(""), engine.ExitPaused)
	newMain := w.remoteSHA("main")
	if st := w.state(); st.Pause.Reason != "PAUSE_PROTECTED_MOVED" || st.Rounds[0].IsDone(state.Convergence) {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas(fmt.Sprintf("- main: %s -> %s", oldMain[:12], newMain[:12]))
	w.outHas("Teammate's merge (twokey test)")

	w.expect(w.resume("\n", 0), engine.ExitPaused) // only y confirms
	w.outHas("Still paused: the protected branch change is not confirmed.")

	w.expect(w.resume("y\n", 0), engine.ExitDone)
	st := w.state()
	if st.Status != state.Converged || len(st.Rounds) != 2 || st.ProtectedSHAs["main"] != newMain {
		t.Errorf("status %s, %d rounds, main %s", st.Status, len(st.Rounds), st.ProtectedSHAs["main"])
	}
	if !strings.Contains(w.read("local-only_logs.md"), "step `convergence`") {
		t.Error("the convergence answer was not logged")
	}
}

// An agent pushes the branch while a protected branch moves: both are
// listed, under PAUSE_INVARIANT, and resume asks about the protected one.
func TestProtectedMoveAndAgentPush(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"fixed.txt": "x"}, Commit: "Fix", Push: true,
		TeammatePush: "Teammate's merge", TeammateBranch: "main"})
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_INVARIANT" || st.Pause.Protected["main"] == "" {
		t.Errorf("pause %+v", st.Pause)
	}
	w.outHas("Branch feature changed on the remote")
	w.outHas("A protected branch moved on origin during round 1's fix step")
}

// A run that paused at the check after convergence publishes that answer
// on resume. Before D41, resume ran the gate on an empty answer and
// reported "converged".
func TestResumeAfterPauseAtConvergence(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "convergence", runner.Actions{Append: map[string]string{"local-only_logs.md": "sneaky entry\n"}})
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.start(""), engine.ExitPaused)
	w.outHas("The log file changed.")

	w.repo.WriteFile(t, "local-only_logs.md", strings.Replace(w.read("local-only_logs.md"), "sneaky entry\n", "", 1))
	w.expect(w.resume("", 0), engine.ExitDone)
	if st := w.state(); len(st.Rounds) != 2 || !st.Rounds[0].Pushed {
		t.Errorf("%d rounds, round 1 pushed: %v", len(st.Rounds), st.Rounds[0].Pushed)
	}
}
