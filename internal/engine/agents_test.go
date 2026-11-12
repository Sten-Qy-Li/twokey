package engine_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/state"
)

// The tests for running agents (M2): timeouts, usage limits, Docker
// cleanup, sessions, usage and the report. The real CLIs are tested in
// internal/runner with recorded output; here the fake runner stands in.

// recorder wraps a runner, records the requests and may change results.
type recorder struct {
	inner runner.Runner
	mu    sync.Mutex
	reqs  []runner.Request
	edit  func(req runner.Request, res *runner.Result)
	delay time.Duration
}

func (r *recorder) Run(ctx context.Context, req runner.Request) runner.Result {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	res := r.inner.Run(ctx, req)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if r.edit != nil {
		r.edit(req, &res)
	}
	return res
}

// cleaner records the Docker prefixes it was asked to clean up.
type cleaner struct {
	mu       sync.Mutex
	prefixes []string
}

func (c *cleaner) Cleanup(_ context.Context, prefix string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefixes = append(c.prefixes, prefix)
	return nil, nil
}

func TestAuditTimeoutGetsOneFreshTry(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "codex-audit", runner.Actions{Timeout: true})
	w.json(1, "codex-audit.2.json", audit())
	w.expect(w.start(""), engine.ExitDone)
	if a := w.state().Rounds[0].Step(state.CodexAudit).Attempts; a != 2 {
		t.Errorf("attempts = %d", a)
	}
	w.outHas("codex-audit ran out of time. It gets one fresh try.")
}

func TestAuditTimesOutTwice(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "codex-audit", runner.Actions{Timeout: true})
	w.actions(1, "codex-audit.2", runner.Actions{Timeout: true})
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_TIMEOUT" {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas("codex-audit ran out of time twice ([timeouts] audit = 90 minutes)")
	w.json(1, "codex-audit.3.json", audit())
	w.expect(w.resume("", 0), engine.ExitDone)
}

// A fix that runs out of time pauses at once: it may have left changes.
func TestFixTimeout(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"half-done.txt": "x"}, Timeout: true})
	w.expect(w.start(""), engine.ExitPaused)
	st := w.state()
	if st.Pause.Reason != "PAUSE_TIMEOUT" || st.Rounds[0].Step(state.Fix).Attempts != 1 {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas("The fix step ran out of time ([timeouts] fix = 120 minutes). It may have left changes:")
	w.outHas("?? half-done.txt")
}

func TestUsageLimitIsWaitedOut(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "claude-audit", runner.Actions{Limit: "You've hit your session limit · resets 3:45pm", LimitResetIn: "30m"})
	w.json(1, "claude-audit.2.json", audit())
	o := w.opts("", 0)
	var slept []time.Duration
	o.Sleep = func(_ context.Context, d time.Duration) bool { slept = append(slept, d); return true }
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	if len(slept) != 1 || slept[0] < 30*time.Minute || slept[0] > 32*time.Minute {
		t.Errorf("waits %v", slept)
	}
	w.outHas("claude-audit hit a usage limit: You've hit your session limit · resets 3:45pm")
	w.outHas("Waiting until ")
}

func TestUsageLimitWithoutResetTimePauses(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	w.actions(1, "codex-audit", runner.Actions{Limit: "You’ve hit your usage limit. Try again later."})
	w.expect(w.start(""), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_USAGE_LIMIT" {
		t.Fatalf("pause %+v", st.Pause)
	}
	w.outHas("Run twokey resume once the limit has reset.")
	w.json(1, "codex-audit.2.json", audit())
	w.expect(w.resume("", 0), engine.ExitDone)
}

// A fix that hit a limit after it committed doesn't simply start again.
func TestFixLimitAfterCommitPauses(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.actions(1, "fix", runner.Actions{Write: map[string]string{"a.txt": "x"}, Commit: "Part of the fix",
		Limit: "You've hit your session limit · resets 3:45pm", LimitResetIn: "10m"})
	o := w.opts("", 0)
	o.Sleep = func(context.Context, time.Duration) bool { t.Error("must not wait"); return true }
	w.expect(engine.Start(t.Context(), o), engine.ExitPaused)
	if st := w.state(); st.Pause.Reason != "PAUSE_USAGE_LIMIT" {
		t.Errorf("pause %+v", st.Pause)
	}
}

func TestDockerCleanupAfterEachStep(t *testing.T) {
	w := newWorld(t)
	w.oneFinding(1, "")
	w.fixes(1, "fixed.txt", "R1-M-01")
	w.cleanRound(2)
	o := w.opts("", 0)
	c := &cleaner{}
	o.Docker = c
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	stamp, _, _ := strings.Cut(w.state().RunID, "-")
	stamp = strings.ToLower(stamp)
	got := strings.Join(c.prefixes, " ")
	for _, want := range []string{"r1-audit-cx-", "r1-audit-cc-", "r1-convergence-cc-", "r1-fix-cc-", "r2-audit-cx-", "r2-audit-cc-", "r2-convergence-cc-"} {
		if !strings.Contains(got, "tk-"+stamp+"-"+want) {
			t.Errorf("no cleanup for %s: %s", want, got)
		}
	}
	if len(c.prefixes) != 7 {
		t.Errorf("%d cleanups: %v", len(c.prefixes), c.prefixes)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Claude's session ID is chosen by twokey and saved before the step runs
// (D8). A resumed Claude session usually reports the session's totals,
// earlier calls included; either way the step's usage is counted once
// (D53). An answer sent back is shown and reported (D54). Raw output is
// published with the answer, and the report lists denials, cost and a
// label that doesn't match.
func TestSessionsUsageAndReport(t *testing.T) {
	for name, resumed := range map[string]runner.Usage{
		"totals restored":     {InputTokens: 150, OutputTokens: 15, CostUSD: 1.5, CallInputTokens: 50},
		"totals not restored": {InputTokens: 50, OutputTokens: 5, CostUSD: 0.5, CallInputTokens: 50},
	} {
		t.Run(name, func(t *testing.T) { testSessionsUsageAndReport(t, resumed) })
	}
}

func testSessionsUsageAndReport(t *testing.T, resumed runner.Usage) {
	w := newWorld(t)
	w.cleanRound(1)
	wrong := conv()
	wrong.AuditedCommit = strings.Repeat("0", 40)
	w.json(1, "convergence.json", wrong)
	w.json(1, "convergence.2.json", conv())
	o := w.opts("", 0)
	rec := &recorder{inner: o.Runner}
	rec.edit = func(req runner.Request, res *runner.Result) {
		_ = os.WriteFile(req.OutBase+".stdout.json", []byte("{}"), 0o600)
		if req.Agent != "claude" {
			return
		}
		if req.SessionID != "" {
			res.SessionID = req.SessionID
		}
		res.Model = "claude-opus-6-0"
		res.Usage = runner.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 1, CallInputTokens: 100}
		if req.Resume != "" {
			res.Usage = resumed
		}
		if req.Name == state.ClaudeAudit {
			res.Denials = []string{"Bash: git push origin HEAD"}
		}
	}
	o.Runner = rec
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	w.outHas("[WARN]  twokey can't use convergence's answer. It gets one more try in the same session:\n        - audited_commit is ")

	st := w.state()
	for _, req := range rec.reqs {
		switch {
		case req.Agent == "codex" && req.SessionID != "":
			t.Errorf("Codex picks its own session ID: %+v", req)
		case req.Agent == "claude" && req.Resume == "" && !uuidRe.MatchString(req.SessionID):
			t.Errorf("not a UUID: %q", req.SessionID)
		case req.OutBase == "" || !strings.HasPrefix(req.OutBase, w.staging):
			t.Errorf("raw output must go to staging: %q", req.OutBase)
		}
	}
	cv := st.Rounds[0].Step(state.Convergence)
	if cv.Usage != (state.Usage{InputTokens: 150, OutputTokens: 15, CostUSD: 1.5}) || cv.Attempts != 2 {
		t.Errorf("convergence usage %+v after %d attempts", cv.Usage, cv.Attempts)
	}
	if st.Rounds[0].Step(state.ClaudeAudit).SessionID != rec.reqs[0].SessionID && st.Rounds[0].Step(state.ClaudeAudit).SessionID != rec.reqs[1].SessionID {
		t.Error("the Claude audit's session ID wasn't kept")
	}
	if _, err := os.Stat(filepath.Join(w.runDir(), "r1", "convergence.2.stdout.json")); err != nil {
		t.Error("the raw output of the second try wasn't published")
	}
	report := w.report()
	for _, want := range []string{
		"## Permission denials and refused commands", "- Round 1, claude-audit: Bash: git push origin HEAD",
		"| 1 | convergence | Claude Code - claude-opus-6-0 | 2 |", "$1.50",
		"- Round 1, convergence: Try 1's answer was sent back: audited_commit is ",
		`The log headings say "Claude Code - claude-opus-6-0", from the model the CLI reported (claude-opus-6-0), but [claude] label is "Claude Code - Claude Opus 5.5".`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if n := strings.Count(report, "The log headings say"); n != 1 {
		t.Errorf("the label note must appear once, not %d times", n)
	}
	if !strings.Contains(w.read("local-only_logs.md"), "### Claude Code - claude-opus-6-0 — ") {
		t.Error("the log heading must use the reported model")
	}
}

// syncBuffer is a bytes.Buffer that the heartbeat can write to safely.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestHeartbeat(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	o := w.opts("", 0)
	out := &syncBuffer{}
	o.Out, o.UI = out, engine.NewTextUI(strings.NewReader(""), out, true)
	o.Heartbeat = 20 * time.Millisecond
	o.Runner = &recorder{inner: o.Runner, delay: 300 * time.Millisecond}
	if code := engine.Start(t.Context(), o); code != engine.ExitDone {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "[..]    round 1: claude-audit 0 min, codex-audit 0 min") {
		t.Errorf("no heartbeat:\n%s", out.String())
	}
}

func TestStatusAndReport(t *testing.T) {
	w := newWorld(t)
	if _, err := engine.Status(w.opts("", 0), ""); !errors.Is(err, engine.ErrNoRuns) {
		t.Errorf("before the first run: %v", err)
	}
	w.cleanRound(1)
	w.file(1, "codex-audit.json", "not json")
	w.expect(w.start(""), engine.ExitPaused)
	out, err := engine.Status(w.opts("", 0), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(autopilot mode)", "Status:     paused", "Paused:     PAUSE_AGENT_FAILED: codex-audit gave no usable answer",
		"Round:      1 of at most 4", "codex-audit", "failed", "Next:       twokey resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	path, err := engine.Report(t.Context(), w.opts("", 0), "")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "**Outcome:** paused.") {
		t.Errorf("report:\n%s", b)
	}
	if _, err := engine.Status(w.opts("", 0), "no-such-run"); err == nil {
		t.Error("an unknown run must be an error")
	}
}

// The sandbox check at the start of a run (D51) tells Codex which checks
// to record as not verified; Claude isn't told anything.
func TestSandboxLimitsInPrompts(t *testing.T) {
	w := newWorld(t)
	w.cleanRound(1)
	o := w.opts("", 0)
	o.Sandbox = func(context.Context) []runner.SandboxResult {
		return []runner.SandboxResult{
			{Name: "git ls-remote", Status: runner.SandboxLimited, Detail: "Access is denied."},
			{Name: "docker", Status: runner.SandboxUntested, Detail: "it isn't running"},
		}
	}
	w.expect(engine.Start(t.Context(), o), engine.ExitDone)
	if l := w.state().CodexLimits; len(l) != 1 || l[0] != "git ls-remote (Access is denied.)" {
		t.Errorf("limits %v", l)
	}
	w.outHas("[WARN]  Codex can't run these in its sandbox: git ls-remote.")
	codex, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "codex-audit.prompt.md"))
	claude, _ := os.ReadFile(filepath.Join(w.runDir(), "r1", "claude-audit.prompt.md"))
	if !strings.Contains(string(codex), "Sandbox limits: these commands don't work in your sandbox: git ls-remote (Access is denied.). Don't retry them") {
		t.Errorf("codex prompt:\n%s", codex)
	}
	if !strings.Contains(string(claude), "Sandbox limits: none known.") {
		t.Errorf("claude prompt:\n%s", claude)
	}
}
