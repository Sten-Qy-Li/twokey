package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

func (w *world) round(input string) int {
	w.t.Helper()
	w.out.Reset()
	return engine.Round(w.t.Context(), w.opts(input, 0))
}

func (w *world) runs() []string {
	w.t.Helper()
	entries, _ := os.ReadDir(filepath.Join(w.repo.Dir, w.cfg.Project.RunDir, "runs"))
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// twokey round runs one round and stops after the push. The next one
// continues the same run, so its audits check the fix (D58).
func TestOneRound(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)

	w.expect(w.round(""), engine.ExitRoundDone)
	st := w.state()
	head := testutil.Git(t, w.repo.Dir, "rev-parse", "HEAD")
	if st.Status != state.RoundDone || len(st.Rounds) != 1 || !st.Rounds[0].Pushed || st.Rounds[0].PushedSHA != head {
		t.Fatalf("state: %s, %d rounds", st.Status, len(st.Rounds))
	}
	if remote := testutil.Git(t, w.repo.Dir, "ls-remote", "origin", "refs/heads/feature"); !strings.HasPrefix(remote, head) {
		t.Errorf("the fix wasn't pushed: remote %s, HEAD %s", remote, head)
	}
	w.outHas("Round 1 is done. Its fix is pushed, but not audited yet")
	w.outHas("Next: twokey round")
	report, _ := os.ReadFile(filepath.Join(w.runDir(), "report.md"))
	if !strings.Contains(string(report), "**it hasn't been audited yet**") {
		t.Errorf("report:\n%s", report)
	}
	if !strings.Contains(w.read("local-only_logs.md"), "step `report`") {
		t.Error("the report has no log entry")
	}
	status, err := engine.Status(w.opts("", 0), "")
	if err != nil || !strings.Contains(status, "Next:       twokey round") {
		t.Errorf("status (%v):\n%s", err, status)
	}

	// The next round continues the run and audits the pushed fix.
	w.expect(w.round(""), engine.ExitDone)
	st = w.state()
	if st.Status != state.Converged || len(st.Rounds) != 2 || st.Rounds[1].Commit != head || len(w.runs()) != 1 {
		t.Fatalf("state: %s, %d rounds, runs %v", st.Status, len(st.Rounds), w.runs())
	}
	r2, _ := os.ReadFile(filepath.Join(w.runDir(), "r2", "codex-audit.prompt.md"))
	if !strings.Contains(string(r2), "Round 1, fix: log entry") {
		t.Errorf("round 2's history lacks round 1's fix:\n%s", r2)
	}

	// Converged, and nothing moved: no new run.
	w.expect(w.round(""), engine.ExitDone)
	w.outHas("hasn't moved since. There is nothing new to audit.")
	if len(w.runs()) != 1 {
		t.Errorf("runs: %v", w.runs())
	}

	// A new commit on the branch: a new run.
	w.repo.WriteFile(t, "more.txt", "x")
	w.repo.Commit(t, "More work")
	testutil.Git(t, w.repo.Dir, "push", "--quiet", "origin", "feature")
	w.scenario = t.TempDir()
	w.cleanRound(1)
	w.expect(w.round(""), engine.ExitDone)
	if len(w.runs()) != 2 {
		t.Errorf("runs: %v", w.runs())
	}
}

// max_rounds doesn't stop one round from fixing; the prompts' "round N of
// at most M" stays true.
func TestOneRoundGoesPastMaxRounds(t *testing.T) {
	w := newWorld(t)
	w.cfg.Loop.MaxRounds = 1
	w.oneFinding(1, "")
	w.fixes(1, "fix1.txt", "R1-M-01")
	w.oneFinding(2, "")
	w.fixes(2, "fix2.txt", "R2-M-01")
	w.expect(w.round(""), engine.ExitRoundDone)
	w.expect(w.round(""), engine.ExitRoundDone)
	st := w.state()
	if len(st.Rounds) != 2 || !st.Rounds[1].Pushed || st.MaxRounds != 2 {
		t.Fatalf("%d rounds, max %d", len(st.Rounds), st.MaxRounds)
	}
	p, _ := os.ReadFile(filepath.Join(w.runDir(), "r2", "claude-audit.prompt.md"))
	if !strings.Contains(string(p), "round 2 of at most 2)") {
		t.Errorf("prompt heading:\n%s", strings.SplitN(string(p), "\n", 2)[0])
	}
}

// A round that stopped before its fix (q at the prompt) is finished, not
// audited again.
func TestOneRoundFinishesAStoppedRound(t *testing.T) {
	w := newWorld(t)
	w.stepper = true
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	// Enter at the audits and the convergence, then q at the fix.
	w.expect(w.start("\n\nq\n"), engine.ExitDone)
	if st := w.state(); st.Status != state.Stopped || st.Rounds[0].IsDone(state.Fix) {
		t.Fatalf("status %s", st.Status)
	}
	// Enter at the fix and the push.
	w.expect(w.round("\n\n"), engine.ExitRoundDone)
	st := w.state()
	if st.Status != state.RoundDone || len(st.Rounds) != 1 || st.Rounds[0].Step(state.CodexAudit).Attempts != 1 {
		t.Errorf("status %s, %d rounds", st.Status, len(st.Rounds))
	}
	w.outHas("resuming run " + st.RunID + ", one round")
}

// A run that stopped after its last round gets that round's fix.
func TestOneRoundAfterMaxRounds(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.expect(engine.Start(t.Context(), w.opts("", 1)), engine.ExitMaxRounds)
	w.expect(w.round(""), engine.ExitRoundDone)
	if st := w.state(); !st.Rounds[0].Pushed || len(st.Rounds) != 1 {
		t.Errorf("%d rounds, pushed %v", len(st.Rounds), st.Rounds[0].Pushed)
	}
}

// Between rounds, the next round needs the same clean start as a new run.
func TestOneRoundChecksTheRepoBetweenRounds(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.round(""), engine.ExitRoundDone)

	w.repo.WriteFile(t, "notes.txt", "x")
	w.expect(w.round(""), engine.ExitError)
	w.outHas("The working tree is not clean:")
	if err := os.Remove(filepath.Join(w.repo.Dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	w.repo.WriteFile(t, "local.txt", "x")
	w.repo.Commit(t, "Not pushed yet")
	w.expect(w.round(""), engine.ExitError)
	w.outHas("HEAD must equal origin/feature after git fetch.")
	if st := w.state(); st.Status != state.RoundDone || len(st.Rounds) != 1 {
		t.Errorf("status %s, %d rounds", st.Status, len(st.Rounds))
	}
}
