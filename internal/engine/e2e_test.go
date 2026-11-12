package engine_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

const marker = "<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->"

// world is a throwaway project with a bare remote, a config and a scenario
// folder for the fake agents (SPEC section 15).
type world struct {
	t        *testing.T
	repo     testutil.Repo
	cfg      *config.Config
	scenario string
	staging  string
	scratch  string
	out      bytes.Buffer
	stepper  bool
}

func newWorld(t *testing.T) *world {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, ".gitignore", "local-only_*\n")
	r.Commit(t, "ignore local files")
	testutil.Git(t, r.Dir, "push", "--quiet", "origin", "feature")
	r.WriteFile(t, "local-only_req.md", "# Requirements\n")
	r.WriteFile(t, "local-only_logs.md", "# Log\n\n"+marker+"\n")
	cfg := config.Default()
	cfg.Project.Branch = "feature"
	cfg.Checkpoint = config.Checkpoint{Label: "Milestone 1", RequirementsFile: "local-only_req.md"}
	cfg.Verify.Commands = []string{}
	return &world{t: t, repo: r, cfg: cfg, scenario: t.TempDir(), staging: t.TempDir(), scratch: t.TempDir()}
}

func (w *world) file(round int, name, content string) {
	w.t.Helper()
	p := filepath.Join(w.scenario, fmt.Sprintf("r%d", round), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) json(round int, name string, v any) {
	w.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		w.t.Fatal(err)
	}
	w.file(round, name, string(b))
}

func (w *world) actions(round int, name string, a runner.Actions) {
	w.json(round, name+".actions.json", a)
}

func (w *world) opts(input string, maxRounds int) engine.Options {
	return engine.Options{
		Root: w.repo.Dir, Cfg: w.cfg, Proc: proc.OS{}, Out: &w.out,
		Runner:      runner.Fake{Dir: w.scenario, Git: proc.OS{}},
		UI:          engine.NewTextUI(strings.NewReader(input), &w.out, !w.stepper),
		StagingRoot: w.staging, ScratchRoot: w.scratch,
		FakeAgents: true, MaxRounds: maxRounds, Autopilot: !w.stepper,
	}
}

func (w *world) start(input string) int { return engine.Start(w.t.Context(), w.opts(input, 0)) }

func (w *world) resume(input string, more int) int {
	return engine.Resume(w.t.Context(), w.opts(input, 0), "", more)
}

func (w *world) runDir() string {
	dir, err := engine.FindRun(filepath.Join(w.repo.Dir, w.cfg.Project.RunDir), "")
	if err != nil {
		w.t.Fatal(err)
	}
	return dir
}

func (w *world) state() *state.State {
	st, err := state.Load(w.runDir())
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

func (w *world) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(w.repo.Dir, rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *world) expect(code, want int) {
	w.t.Helper()
	if code != want {
		w.t.Fatalf("exit code %d, want %d. Output:\n%s", code, want, w.out.String())
	}
}

func (w *world) outHas(s string) {
	w.t.Helper()
	if !strings.Contains(w.out.String(), s) {
		w.t.Errorf("output lacks %q:\n%s", s, w.out.String())
	}
}

// Answer builders.

func finding(id, sev, kind string) schema.Finding {
	f := schema.Finding{ID: id, Title: "problem " + id, Severity: sev, Kind: kind, Evidence: "app.go:1", FixHint: "fix it", Options: []string{}}
	if kind == "needs_decision" {
		f.Options = []string{"A", "B"}
		f.RecommendedOption = "A"
	}
	return f
}

func audit(fs ...schema.Finding) schema.AuditResult {
	if fs == nil {
		fs = []schema.Finding{}
	}
	return schema.AuditResult{AuditedCommit: "${commit}", Verdict: "not_ready", Summary: "audited", Findings: fs, LogEntry: "- **Task:** blind audit"}
}

func merged(id, sev, kind, status string, from ...string) schema.MergedFinding {
	m := schema.MergedFinding{Finding: finding(id, sev, kind), Sources: []string{"codex"}, MergedFrom: append([]string{}, from...), Status: status}
	if status == "disputed" {
		m.DisputeReason = "not reproducible"
	}
	return m
}

func conv(ms ...schema.MergedFinding) schema.ConvergenceResult {
	if ms == nil {
		ms = []schema.MergedFinding{}
	}
	return schema.ConvergenceResult{AuditedCommit: "${commit}", Verdict: "not_ready", Summary: "merged", Findings: ms, LogEntry: "- **Task:** convergence\n## Merged list"}
}

func fixAnswer(done ...string) string {
	var items []string
	for _, id := range done {
		items = append(items, fmt.Sprintf(`{"id":%q,"change":"changed it","verified_how":"ran the tests"}`, id))
	}
	return fmt.Sprintf(`{"base_commit":"${commit}","commits":${new_commits},"done":[%s],"not_done":[],"log_entry_markdown":"- **Task:** fix"}`, strings.Join(items, ","))
}

// cleanRound scripts a round in which nobody finds anything.
func (w *world) cleanRound(n int) {
	w.json(n, "codex-audit.json", audit())
	w.json(n, "claude-audit.json", audit())
	w.json(n, "convergence.json", conv())
}

// oneFinding scripts a round in which Codex finds one P1 problem and the
// convergence step confirms it.
func (w *world) oneFinding(n int, recursOf string) {
	w.json(n, "codex-audit.json", audit(finding(fmt.Sprintf("R%d-CX-01", n), "P1", "repo_fix")))
	w.json(n, "claude-audit.json", audit())
	m := merged(fmt.Sprintf("R%d-M-01", n), "P1", "repo_fix", "confirmed", fmt.Sprintf("R%d-CX-01", n))
	m.RecursOf = recursOf
	w.json(n, "convergence.json", conv(m))
}

// fixes scripts a fix step that commits a file.
func (w *world) fixes(n int, file string, ids ...string) {
	w.actions(n, "fix", runner.Actions{Write: map[string]string{file: "fixed\n"}, Commit: "Fix " + strings.Join(ids, ", ")})
	w.file(n, "fix.json", fixAnswer(ids...))
}

// The scenarios of SPEC section 15.

func TestConvergeInRound1(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P3", "repo_fix")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P3", "repo_fix", "noted", "R1-CX-01")))
	w.expect(w.start(""), engine.ExitDone)

	st := w.state()
	if st.Status != state.Converged || len(st.Rounds) != 1 {
		t.Errorf("state: %s, %d rounds", st.Status, len(st.Rounds))
	}
	log := w.read("local-only_logs.md")
	for _, want := range []string{"### twokey ", "### Claude Code - Claude Opus 5.5 — ", "### Codex - GPT-6.1-Sol — ",
		"step `codex-audit`", "step `claude-audit`", "step `convergence`", "#### Merged list"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if strings.Index(log, "step `report`") > strings.Index(log, "step `convergence`") {
		t.Error("the report must be the newest entry")
	}
	report, _ := os.ReadFile(filepath.Join(w.runDir(), "report.md"))
	if !strings.Contains(string(report), "converged in round 1") || !strings.Contains(string(report), "- R1-M-01 problem R1-M-01") {
		t.Errorf("report:\n%s", report)
	}
	if _, err := lock.Read(filepath.Join(w.repo.Dir, w.cfg.Project.RunDir)); err == nil {
		t.Error("the lock was not released")
	}
	// Blindness: prompts never mention the staging folder.
	prompts, _ := filepath.Glob(filepath.Join(w.runDir(), "r1", "*.prompt.md"))
	if len(prompts) != 3 {
		t.Errorf("prompts: %v", prompts)
	}
	for _, p := range prompts {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), w.staging) {
			t.Errorf("%s mentions the staging folder", p)
		}
	}
}

func TestFixThenConverge(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P1", "repo_fix")))
	w.json(1, "claude-audit.json", audit(finding("R1-CC-01", "P2", "repo_fix")))
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01", "R1-CC-01")))
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.start(""), engine.ExitDone)

	st := w.state()
	if st.Status != state.Converged || len(st.Rounds) != 2 || !st.Rounds[0].Pushed {
		t.Fatalf("state: %s, %d rounds", st.Status, len(st.Rounds))
	}
	head := testutil.Git(t, w.repo.Dir, "rev-parse", "HEAD")
	remote := testutil.Git(t, w.repo.Dir, "ls-remote", "origin", "refs/heads/feature")
	if !strings.HasPrefix(remote, head) || st.Rounds[1].Commit != head {
		t.Errorf("HEAD %s, remote %s, round 2 commit %s", head, remote, st.Rounds[1].Commit)
	}
	if mainSHA := testutil.Git(t, w.repo.Dir, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(mainSHA, st.ProtectedSHAs["main"]) {
		t.Error("main changed")
	}
	r2, _ := os.ReadFile(filepath.Join(w.runDir(), "r2", "codex-audit.prompt.md"))
	if !strings.Contains(string(r2), "Round 1, fix: log entry \"Claude Code - Claude Opus 5.5 — ") {
		t.Errorf("round 2's history lacks round 1's fix:\n%s", r2)
	}
	w.outHas("Pushed 1 commit to origin/feature.")
}

func TestDisputeWithdrawn(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P1", "repo_fix")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01")))
	w.json(1, "dispute.json", schema.DisputeResult{Rechecks: []schema.Recheck{{ID: "R1-M-01", Decision: "withdraw", Evidence: "agreed"}}, LogEntry: "- withdrew"})
	w.expect(w.start(""), engine.ExitDone)
	st := w.state()
	if st.Status != state.Converged || !st.Rounds[0].IsDone(state.Dispute) {
		t.Errorf("state: %s", st.Status)
	}
	prompt, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "dispute.prompt.md"))
	if !strings.Contains(string(prompt), "- R1-M-01 (your R1-CX-01) [P1 repo_fix] problem R1-M-01\n  Dispute reason: not reproducible") {
		t.Errorf("dispute prompt:\n%s", prompt)
	}
}

func TestDisputeMaintainedThenRuling(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P1", "repo_fix")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01")))
	w.json(1, "dispute.json", schema.DisputeResult{Rechecks: []schema.Recheck{{ID: "R1-M-01", Decision: "maintain", Evidence: "still broken"}}, LogEntry: "- kept it"})
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)

	// No answer at the prompt: the run pauses.
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Status != state.Paused || st.Pause.Reason != "PAUSE_ADJUDICATE" {
		t.Fatalf("state: %+v", st.Pause)
	}
	if _, err := os.Stat(filepath.Join(w.runDir(), "pending.md")); err != nil {
		t.Error("pending.md missing")
	}
	w.outHas("Codex keeps it: still broken")

	w.expect(w.resume("fix it needs a test\n", 0), engine.ExitDone)
	decisions, _ := os.ReadFile(filepath.Join(w.runDir(), "decisions.md"))
	if !strings.Contains(string(decisions), "**R1-M-01** (ruling, round 1): problem R1-M-01 → **fix**. Note: it needs a test") {
		t.Errorf("decisions.md:\n%s", decisions)
	}
	if !strings.Contains(w.read("local-only_logs.md"), "step `ruling`") {
		t.Error("the ruling has no log entry")
	}
}

func TestDecisionPause(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P2", "needs_decision")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01")))
	w.fixes(1, "decided.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(w.start("2\n"), engine.ExitDone)

	if d := w.state().Decisions["R1-M-01"]; d != "B" {
		t.Errorf("decision = %q", d)
	}
	fix, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "fix.prompt.md"))
	if !strings.Contains(string(fix), "The owner's decision: B") {
		t.Errorf("fix prompt lacks the decision:\n%s", fix)
	}
	r2, _ := os.ReadFile(filepath.Join(w.runDir(), "r2", "claude-audit.prompt.md"))
	if !strings.Contains(string(r2), "**R1-M-01** (decision, round 1)") {
		t.Error("later prompts must carry the decisions")
	}
}

func TestDropDecision(t *testing.T) {
	w := newWorld(t)
	w.json(1, "codex-audit.json", audit(finding("R1-CX-01", "P2", "needs_decision")))
	w.json(1, "claude-audit.json", audit())
	w.json(1, "convergence.json", conv(merged("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-01")))
	w.expect(w.start("drop\n"), engine.ExitDone)
	if st := w.state(); st.Status != state.Converged || len(st.Rounds) != 1 {
		t.Errorf("a dropped decision resolves the finding: %s, %d rounds", st.Status, len(st.Rounds))
	}
}

func TestRecurrence(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fix1.txt", "R1-M-01")
	w.oneFinding(2, "R1-M-01")
	w.fixes(2, "fix2.txt", "R2-M-01")
	w.oneFinding(3, "R2-M-01")
	w.expect(w.start("drop\n"), engine.ExitDone)
	st := w.state()
	if len(st.Rounds) != 3 || st.Rulings["R3-M-01"] != "drop" || st.Status != state.Converged {
		t.Errorf("rounds %d, rulings %v, status %s", len(st.Rounds), st.Rulings, st.Status)
	}
	w.outHas("This problem came back 2 times after being fixed (latest: recurs_of R2-M-01).")
}

func TestMaxRoundsThenMore(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	w.expect(engine.Start(t.Context(), w.opts("", 1)), engine.ExitMaxRounds)
	st := w.state()
	if st.Status != state.MaxRounds || st.Rounds[0].IsDone(state.Fix) {
		t.Fatalf("no fix after the last round: %s", st.Status)
	}
	report, _ := os.ReadFile(filepath.Join(w.runDir(), "report.md"))
	if !strings.Contains(string(report), "stopped after round 1, the last one, with confirmed findings left: R1-M-01") {
		t.Errorf("report:\n%s", report)
	}
	w.expect(w.resume("", 0), engine.ExitMaxRounds)
	w.expect(w.resume("", 1), engine.ExitDone)
	if st := w.state(); st.Status != state.Converged || st.MaxRounds != 2 {
		t.Errorf("after --more-rounds 1: %s, max %d", st.Status, st.MaxRounds)
	}
}

func TestVerifyFailureThenSuccess(t *testing.T) {
	w := newWorld(t)
	check := "test -f good.txt"
	if runtime.GOOS == "windows" {
		check = "if exist good.txt (exit 0) else (exit 1)"
	}
	w.cfg.Verify.Commands = []string{"git --version", check}
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"bad.txt": "x"}, Commit: "First try"})
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.actions(1, "fix.2", runner.Actions{Write: map[string]string{"good.txt": "x"}, Commit: "Second try"})
	w.file(1, "fix.2.json", fixAnswer("R1-M-01"))
	w.cleanRound(2)
	w.expect(w.start(""), engine.ExitDone)

	vlog, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "verify.log"))
	if !strings.Contains(string(vlog), "[exit 1]") || strings.Count(string(vlog), "[exit 0]") != 3 {
		t.Errorf("verify.log:\n%s", vlog)
	}
	if st := w.state(); st.Rounds[0].VerifyRetries != 1 || st.Status != state.Converged {
		t.Errorf("retries %d, status %s", st.Rounds[0].VerifyRetries, st.Status)
	}
}

func TestVerifyFailsTwice(t *testing.T) {
	w := newWorld(t)
	w.cfg.Verify.Commands = []string{"git no-such-command"}
	w.oneFinding(1, "")
	w.fixes(1, "a.txt", "R1-M-01")
	w.actions(1, "fix.2", runner.Actions{Write: map[string]string{"b.txt": "x"}, Commit: "Second try"})
	w.file(1, "fix.2.json", fixAnswer("R1-M-01"))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_VERIFY_FAILED" {
		t.Errorf("pause: %+v", st.Pause)
	}
}

func TestRemoteMoved(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"fixed.txt": "x"}, Commit: "Fix", TeammatePush: "Teammate's work"})
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_REMOTE_MOVED" || st.Rounds[0].Pushed {
		t.Errorf("pause: %+v", st.Pause)
	}
	w.outHas("Someone else pushed to origin/feature during this step.")
	remote := testutil.Git(t, w.repo.Dir, "ls-remote", "origin", "refs/heads/feature")
	if strings.HasPrefix(remote, testutil.Git(t, w.repo.Dir, "rev-parse", "HEAD")) {
		t.Error("twokey overwrote the teammate's push")
	}
}

func TestAgentTouchesTheLog(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "claude-audit", runner.Actions{Append: map[string]string{"local-only_logs.md": "sneaky entry\n"}})
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_INVARIANT" || st.Rounds[0].Published {
		t.Errorf("pause: %+v", st.Pause)
	}
	w.outHas("The log file changed.")
}

func TestAgentPushesByItself(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"fixed.txt": "x"}, Commit: "Fix", Push: true})
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_INVARIANT" {
		t.Errorf("pause: %+v", st.Pause)
	}
	w.outHas("Branch feature changed on the remote")
}

func TestAuditMustNotChangeTheRepo(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "codex-audit", runner.Actions{Write: map[string]string{"README.md": "edited\n"}})
	w.expect(w.start(""), engine.ExitPaused)
	w.outHas("M README.md")
}

func TestNoProgress(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.file(1, "fix.json", fixAnswer("R1-M-01"))
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_NO_PROGRESS" {
		t.Errorf("pause: %+v", st.Pause)
	}
}

func TestInvalidAnswerGetsOneRetry(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	wrong := audit()
	wrong.AuditedCommit = strings.Repeat("0", 40)
	w.json(1, "codex-audit.json", wrong)
	w.json(1, "codex-audit.2.json", audit())
	w.expect(w.start(""), engine.ExitDone)
	if a := w.state().Rounds[0].Step(state.CodexAudit).Attempts; a != 2 {
		t.Errorf("attempts = %d", a)
	}
}

func TestAgentFailsTwiceThenResume(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.file(1, "codex-audit.json", "not json")
	w.expect(w.start(""), engine.ExitPaused)
	st := w.state()
	if st.Pause.Reason != "PAUSE_AGENT_FAILED" || st.Rounds[0].Step(state.ClaudeAudit).Status != state.Done {
		t.Fatalf("pause %+v", st.Pause)
	}
	// The other audit's result stays in staging; resume reruns only Codex,
	// as its third try.
	w.json(1, "codex-audit.3.json", audit())
	w.expect(w.resume("", 0), engine.ExitDone)
	if a := w.state().Rounds[0].Step(state.ClaudeAudit).Attempts; a != 1 {
		t.Errorf("Claude's audit ran again (%d attempts)", a)
	}
}

func TestStepperQuitAndResume(t *testing.T) {
	w := newWorld(t)
	w.stepper = true
	w.cleanRound(1)
	w.expect(w.start("\nq\n"), engine.ExitDone)
	if st := w.state(); st.Status != state.Stopped || !st.Rounds[0].Published {
		t.Fatalf("status %s", st.Status)
	}
	w.outHas("Enter = run, e = edit the prompt first, q = quit (resume later):")
	w.expect(w.resume("\n\n", 0), engine.ExitDone)
	if st := w.state(); st.Status != state.Converged {
		t.Errorf("status %s", st.Status)
	}
}

func TestPreflightAndLock(t *testing.T) {
	w := newWorld(t)
	w.repo.WriteFile(t, "notes.txt", "x")
	w.expect(w.start(""), engine.ExitError)
	w.outHas("The working tree is not clean:")

	w = newWorld(t)
	runRoot := filepath.Join(w.repo.Dir, w.cfg.Project.RunDir)
	if _, err := lock.Acquire(runRoot, lock.Info{PID: os.Getpid(), RunID: "other"}); err != nil {
		t.Fatal(err)
	}
	w.expect(w.start(""), engine.ExitError)
	w.outHas("is running run other in this repository")
}

func TestStepperEditsThePrompt(t *testing.T) {
	w := newWorld(t)
	w.stepper = true
	w.cleanRound(1)
	o := w.opts("e\n\n\n\n\n\n", 0) // edit, Enter after each of 2 edits, then Enter at each stop
	o.Editor = "git --version"      // a harmless stand-in that exits at once
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	s := w.state().Rounds[0].Step(state.CodexAudit)
	if !strings.HasSuffix(s.Prompt, "codex-audit.prompt.edited.md") {
		t.Errorf("the edited prompt was not used: %s", s.Prompt)
	}
	if _, err := os.Stat(filepath.Join(w.runDir(), "r1", "codex-audit.prompt.md")); err != nil {
		t.Error("the rendered prompt must stay next to the edited one")
	}
	w.outHas("Save and close")
}
