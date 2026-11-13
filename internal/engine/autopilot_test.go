package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
)

// autopilot makes the world answer for the owner (D61).
func autopilot(t *testing.T) *world {
	w := newWorld(t)
	w.decisions = engine.Recommended
	return w
}

func (w *world) runFile(name string) string {
	w.t.Helper()
	b, _ := os.ReadFile(filepath.Join(w.runDir(), name))
	return string(b)
}

func (w *world) decide(id, answer string) int {
	w.t.Helper()
	w.out.Reset()
	return engine.Decide(w.t.Context(), w.opts("", 0), id, answer)
}

// A decision takes the merged finding's recommended option; the record
// says what the options were and who recommended what.
func TestAutopilotTakesTheRecommendedOption(t *testing.T) {
	w := autopilot(t)
	cx := finding("R1-CX-01", "P2", "needs_decision")
	cc := finding("R1-CC-01", "P2", "needs_decision")
	cc.RecommendedOption = "B"
	w.json(1, "codex-audit.json", audit(cx))
	w.json(1, "claude-audit.json", audit(cc))
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01", "R1-CC-01")))
	w.fixes(1, "decided.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.start(""), engine.ExitDone)

	st := w.state()
	if st.Decisions["R1-M-01"] != "A" || st.Automatic["R1-M-01"] != "A" {
		t.Fatalf("decisions %v, automatic %v", st.Decisions, st.Automatic)
	}
	want := `- **R1-M-01** (automatic decision, round 1): problem R1-M-01 → **A**. Options: 1. A; 2. B. Recommended by the convergence step (Claude Code); ` +
		`in the blind audits, R1-CX-01 (Codex) recommended "A" and R1-CC-01 (Claude Code) recommended "B".`
	if !strings.Contains(w.runFile("decisions.md"), want) {
		t.Errorf("decisions.md lacks %q:\n%s", want, w.runFile("decisions.md"))
	}
	if !strings.Contains(w.read("local-only_logs.md"), "step `automatic decision`") {
		t.Error("the automatic decision has no log entry")
	}
	if fix := w.runFile("r1/fix.prompt.md"); !strings.Contains(fix, "Decision, taken automatically (the recommended option): A") {
		t.Errorf("fix prompt:\n%s", fix)
	}
	if !strings.Contains(w.runFile("report.md"), "Autopilot took the ones marked \"automatic\".") {
		t.Errorf("report:\n%s", w.runFile("report.md"))
	}
	w.outHas("Automatic decision: R1-M-01 → A")
}

// A decision without a recommended option is skipped this round and the
// rest is fixed. The owner answers later with twokey decide.
func TestAutopilotSkipsWhatNeedsTheOwner(t *testing.T) {
	w := autopilot(t)
	open := finding("R1-CX-02", "P2", "needs_decision")
	open.RecommendedOption = ""
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P1", "repo_fix"), open))
	w.json(1, "claude-audit.json", audit())
	m2 := merged("R1-M-02", "P2", "needs_decision", "confirmed", "R1-CX-02")
	m2.RecommendedOption = ""
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"), m2))
	w.fixes(1, "fixed.txt", "R1-M-01")

	w.expect(w.round(""), engine.ExitRoundDone)
	st := w.state()
	if len(st.Rounds[0].Skipped) != 1 || st.Rounds[0].Skipped[0].ID != "R1-M-02" || st.Rounds[0].Gate.Action != "FIX" {
		t.Fatalf("skipped %v, gate %+v", st.Rounds[0].Skipped, st.Rounds[0].Gate)
	}
	if fix := w.runFile("r1/fix.prompt.md"); strings.Contains(fix, "R1-M-02") {
		t.Errorf("the skipped finding went to the fix:\n%s", fix)
	}
	for _, f := range []string{"pending.md", "report.md"} {
		if !strings.Contains(w.runFile(f), "R1-M-02 (round 1): it needs your decision, and no option was recommended. Options: 1. A; 2. B") {
			t.Errorf("%s:\n%s", f, w.runFile(f))
		}
	}
	w.outHas("Skipped R1-M-02 this round")

	w.expect(w.decide("R1-M-02", "2"), engine.ExitDone)
	st = w.state()
	if st.Decisions["R1-M-02"] != "B" || len(st.Rounds[0].Skipped) != 0 {
		t.Errorf("decision %q, skipped %v", st.Decisions["R1-M-02"], st.Rounds[0].Skipped)
	}
	if _, err := os.Stat(filepath.Join(w.runDir(), "pending.md")); err == nil {
		t.Error("pending.md should be gone: nothing waits any more")
	}
	w.outHas("Next: twokey round")
}

// With only skipped findings left, the round has nothing to fix: it
// pauses, and continues once the owner has answered.
func TestAutopilotPausesWhenOnlySkippedAreLeft(t *testing.T) {
	w := autopilot(t)
	open := finding("R1-CX-01", "P2", "needs_decision")
	open.RecommendedOption = ""
	w.json(1, "codex-audit.json", audit(open))
	w.json(1, "claude-audit.json", audit())
	m := merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01")
	m.RecommendedOption = ""
	w.json(1, "convergence.json", conv(m))
	w.fixes(1, "decided.txt", "R1-M-01")

	w.expect(w.round(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_SKIPPED" {
		t.Fatalf("pause %+v", st.Pause)
	}
	if !strings.Contains(w.runFile("pending.md"), "R1-M-01 (round 1): it needs your decision") {
		t.Errorf("pending.md:\n%s", w.runFile("pending.md"))
	}
	// Another round before the owner answers pauses again, without agents.
	w.expect(w.round(""), engine.ExitPaused)
	if a := w.state().Rounds[0].Step(state.CodexAudit).Attempts; a != 1 {
		t.Errorf("the audits ran again: %d attempts", a)
	}

	w.expect(w.decide("R1-M-01", "1"), engine.ExitDone)
	w.outHas("Next: twokey resume, or twokey round")
	w.expect(w.round(""), engine.ExitRoundDone)
	if fix := w.runFile("r1/fix.prompt.md"); !strings.Contains(fix, "The owner's decision: A") {
		t.Errorf("fix prompt:\n%s", fix)
	}
}

// A finding Codex maintained after the dispute is fixed, with both sides
// in the record.
func TestAutopilotFixesWhatCodexMaintains(t *testing.T) {
	w := autopilot(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P1", "repo_fix")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01")))
	w.json(1, "dispute.json", schema.DisputeResult{Rechecks: []schema.Recheck{{ID: "R1-M-01", Decision: "maintain", Evidence: "still broken"}}, LogEntry: "- kept it"})
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.start(""), engine.ExitDone)

	if r := w.state().Rulings["R1-M-01"]; r != "fix" {
		t.Errorf("ruling %q", r)
	}
	want := "- **R1-M-01** (automatic ruling, round 1): problem R1-M-01 → **fix**. The convergence step (Claude Code) disputed it: not reproducible. " +
		"Codex maintained it in the dispute step: still broken. So twokey fixes it, and the next round's audits check the result."
	if !strings.Contains(w.runFile("decisions.md"), want) {
		t.Errorf("decisions.md:\n%s", w.runFile("decisions.md"))
	}
	if fix := w.runFile("r1/fix.prompt.md"); !strings.Contains(fix, "Codex maintained it after the dispute, so it is fixed (an automatic ruling).") {
		t.Errorf("fix prompt:\n%s", fix)
	}
}

// A finding that keeps coming back is skipped, not fixed a third time.
func TestAutopilotSkipsARecurringFinding(t *testing.T) {
	w := autopilot(t)
	w.oneFinding(1, "")
	w.fixes(1, "fix1.txt", "R1-M-01")
	w.oneFinding(2, "R1-M-01")
	w.fixes(2, "fix2.txt", "R2-M-01")
	w.oneFinding(3, "R2-M-01")
	w.expect(w.start(""), engine.ExitPaused)
	st := w.state()
	if st.Pause.Reason != "PAUSE_SKIPPED" || len(st.Rounds) != 3 || st.Rounds[2].Skipped[0].ID != "R3-M-01" {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas("it came back 2 times after being fixed (latest: recurs_of R2-M-01). Fix it once more, or drop it")

	w.expect(w.decide("R3-M-01", "2"), engine.ExitError)
	w.outHas("R3-M-01 takes fix or drop")
	w.expect(w.decide("R3-M-01", "drop"), engine.ExitDone)
	w.expect(w.resume("", 0), engine.ExitDone)
	if st := w.state(); st.Status != state.Converged || st.Rulings["R3-M-01"] != "drop" {
		t.Errorf("status %s, rulings %v", st.Status, st.Rulings)
	}
}

// With [autopilot] decide = false, autopilot pauses for the owner instead.
func TestAutopilotWithoutDecisions(t *testing.T) {
	w := newWorld(t)
	w.decisions = engine.WaitOwner
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P2", "needs_decision")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01")))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_DECISION" || len(st.Decisions) != 0 {
		t.Errorf("pause %+v, decisions %v", st.Pause, st.Decisions)
	}
	w.outHas("Run twokey resume without --autopilot, which asks you, or answer each with twokey decide <ID> <answer>.")
}

// The owner's answer replaces an automatic one; mistakes are refused.
func TestDecideReplacesAnAutomaticChoice(t *testing.T) {
	w := autopilot(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P2", "needs_decision")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01")))
	w.fixes(1, "decided.txt", "R1-M-01")
	w.expect(w.round(""), engine.ExitRoundDone)

	w.expect(w.decide("R9-M-01", "1"), engine.ExitError)
	w.outHas("has no finding R9-M-01")
	w.expect(w.decide("R1-M-01", "5"), engine.ExitError)
	w.outHas("R1-M-01 has options 1 to 2 (got 5): 1. A; 2. B.")

	w.expect(w.decide("R1-M-01", "2"), engine.ExitDone)
	st := w.state()
	if st.Decisions["R1-M-01"] != "B" || st.Automatic["R1-M-01"] != "" {
		t.Errorf("decisions %v, automatic %v", st.Decisions, st.Automatic)
	}
	if want := "- **R1-M-01** (decision, round 1): problem R1-M-01 → **B**. Your answer. It replaces the automatic choice: A"; !strings.Contains(w.runFile("decisions.md"), want) {
		t.Errorf("decisions.md:\n%s", w.runFile("decisions.md"))
	}
	if !strings.Contains(w.read("local-only_logs.md"), "step `decision`") {
		t.Error("the owner's answer has no log entry")
	}
}

// twokey stop: the run stops before its next agent step and saves.
func TestStopBeforeTheNextStep(t *testing.T) {
	w := autopilot(t)
	w.oneFinding(1, "")
	w.actions(1, "claude-audit", runner.Actions{Write: map[string]string{"local-only_twokey/stop": "step\n"}})
	w.expect(w.start(""), engine.ExitDone)
	st := w.state()
	if st.Status != state.Stopped || !st.Rounds[0].Published || st.Rounds[0].Step(state.Convergence) == nil ||
		st.Rounds[0].Step(state.Convergence).Attempts != 0 {
		t.Fatalf("status %s", st.Status)
	}
	w.outHas("Stopped by twokey stop, before the next agent step. The run is saved.")
	if _, err := os.Stat(filepath.Join(w.repo.Dir, "local-only_twokey", "stop")); err == nil {
		t.Error("the stop request must be used up")
	}
}

// twokey stop writes the request only while a run holds the lock.
func TestRequestStop(t *testing.T) {
	w := newWorld(t)
	runRoot := filepath.Join(w.repo.Dir, w.cfg.Project.RunDir)
	w.expect(engine.RequestStop(w.opts("", 0), false), engine.ExitDone)
	w.outHas("No twokey run is going in this repository.")

	lk, err := lock.Acquire(runRoot, lock.Info{PID: os.Getpid(), RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lk.Release() }()
	w.expect(engine.RequestStop(w.opts("", 0), true), engine.ExitDone)
	w.outHas("Asked run r1 to stop now.")
	if b, _ := os.ReadFile(filepath.Join(runRoot, "stop")); strings.TrimSpace(string(b)) != "now" {
		t.Errorf("stop file: %q", b)
	}
}

// The loop holds keep-awake while it runs, and lets go at the end.
func TestKeepAwake(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	held, released := 0, 0
	o := w.opts("", 0)
	o.KeepAwake = func() func() {
		held++
		return func() { released++ }
	}
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	if held != 1 || released != 1 {
		t.Errorf("held %d, released %d", held, released)
	}
}
