package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/docker"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// outcome is how an agent step ended.
type outcome struct {
	answer  []byte
	problem string // "" if there is an answer
	reason  string // the pause reason for the problem
}

// maxLimitWaits caps how often one step waits for a usage limit.
const maxLimitWaits = 3

// runAgent runs one agent step (SPEC sections 9 and 10):
//   - an invalid answer gets one retry in the same session;
//   - a timeout gets one fresh retry, except in the fix step;
//   - a usage limit is waited out if the reset time is known and close
//     enough, then the step runs again from the start (a fix only if it
//     changed nothing).
//
// Every try ends with the step's Docker cleanup.
func (e *engine) runAgent(ctx context.Context, r *state.Round, s *state.Step, step, prompt, resume string, check func([]byte) []string) outcome {
	scratch := filepath.Join(e.o.ScratchRoot, e.st.RunID, fmt.Sprintf("r%d", r.N), s.Name)
	for _, d := range []string{scratch, e.staging} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return outcome{problem: "can't create " + d + ": " + err.Error(), reason: PauseAgentFailed}
		}
	}
	req := runner.Request{Agent: s.Agent, Name: s.Name, Step: step, Round: r.N, Prompt: prompt,
		RepoDir: e.o.Root, ScratchDir: scratch, Commit: r.Commit, Resume: resume}
	head := e.headOr(ctx) // a fix that hit a limit may rerun only if HEAD is still here
	fail := func(problem, reason string) outcome {
		e.update(func() { s.Status, s.Error = state.Failed, strings.ReplaceAll(problem, "\n", "; ") })
		return outcome{problem: problem, reason: reason}
	}

	var all []string
	waits := 0
	for try := 1; try <= 2; {
		req.SessionID = ""
		if req.Resume == "" && s.Agent == "claude" {
			req.SessionID = newSessionID() // in state.json before the step starts (D8)
		}
		e.update(func() {
			s.Status, s.Error = state.Active, ""
			s.Attempts++
			if s.Started.IsZero() {
				s.Started = e.o.Now().UTC()
			}
			if req.SessionID != "" {
				s.SessionID = req.SessionID
			}
		})
		req.Attempt = s.Attempts
		req.OutBase = filepath.Join(e.staging, fmt.Sprintf("r%d-%s.%d", r.N, s.Name, req.Attempt))
		res := e.attempt(ctx, r, s, step, req)
		if ctx.Err() != nil {
			return outcome{problem: "stopped"}
		}
		e.recordRun(s, req, res)

		switch {
		case res.Limit != nil:
			until, ok := e.limitWait(res.Limit)
			if s.Name == state.Fix && !e.unchanged(ctx, head) {
				ok = false
			}
			if !ok || waits >= maxLimitWaits {
				return fail(limitText(s.Name, res.Limit), PauseUsageLimit)
			}
			waits++
			ui.Line(e.o.Out, ui.Busy, fmt.Sprintf("%s hit a usage limit: %s", s.Name, res.Limit.Message))
			ui.Detail(e.o.Out, fmt.Sprintf("Waiting until %s, then it runs again. Ctrl+C stops the run; twokey resume continues it.", until.Local().Format("15:04")))
			if !e.o.Sleep(ctx, until.Sub(e.o.Now())) {
				return outcome{problem: "stopped"}
			}
			req.Resume, req.Prompt = resume, prompt
			continue
		case res.TimedOut:
			all = append(all, fmt.Sprintf("attempt %d: ran out of time after %s", req.Attempt, plural(e.timeoutMinutes(step), "minute")))
			if s.Name == state.Fix || try == 2 {
				return fail(strings.Join(all, "\n"), PauseTimeout)
			}
			ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("%s ran out of time. It gets one fresh try.", s.Name))
			try++
			req.Resume, req.Prompt = resume, prompt
			continue
		}

		var probs []string
		switch {
		case res.Problem != "":
			probs = []string{"the agent gave no answer: " + res.Problem}
		case len(res.Answer) == 0:
			probs = []string{"the agent gave no answer"}
		default:
			probs = check(res.Answer)
		}
		if len(probs) == 0 {
			e.update(func() { s.Status, s.Finished = state.Done, e.o.Now().UTC() })
			if err := os.WriteFile(e.stagingPath(r, s), res.Answer, 0o644); err != nil {
				return fail("can't save the answer: "+err.Error(), PauseAgentFailed)
			}
			return outcome{answer: res.Answer}
		}
		for _, p := range probs {
			all = append(all, fmt.Sprintf("attempt %d: %s", req.Attempt, p))
		}
		try++
		if s.SessionID == "" || try > 2 {
			break // nothing to resume, or no tries left
		}
		ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("twokey can't use %s's answer. It gets one more try in the same session:", s.Name))
		for _, p := range probs {
			ui.Detail(e.o.Out, "- "+p)
		}
		e.update(func() {
			s.Notes = append(s.Notes, fmt.Sprintf("Try %d's answer was sent back: %s", req.Attempt, strings.Join(probs, "; ")))
		})
		req.Resume, req.Prompt = s.SessionID, retryPrompt(probs)
	}
	return fail(strings.Join(all, "\n"), PauseAgentFailed)
}

// attempt runs the CLI once, with the step's timeout and the heartbeat,
// then removes what the step left in Docker.
func (e *engine) attempt(ctx context.Context, r *state.Round, s *state.Step, step string, req runner.Request) runner.Result {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(e.timeoutMinutes(step))*time.Minute)
	defer cancel()
	name := s.Name
	if req.Attempt > 1 {
		name = fmt.Sprintf("%s (try %d)", s.Name, req.Attempt)
	}
	done := e.beat.add(r.N, name, e.o.Now())
	res := e.o.Runner.Run(cctx, req)
	done()
	e.cleanupDocker(ctx, r, s)
	return res
}

// recordRun keeps what a run reported: session, model, usage, denials.
func (e *engine) recordRun(s *state.Step, req runner.Request, res runner.Result) {
	e.update(func() {
		if res.SessionID != "" {
			s.SessionID = res.SessionID
		}
		if res.Model != "" {
			s.Model = res.Model
		}
		u := state.Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CostUSD: res.Usage.CostUSD}
		s.Usage, s.SessionUsage = addRun(s.Usage, s.SessionUsage, u, res.Usage.CallInputTokens, req.Resume != "" && s.Agent == "claude")
		s.Denials = append(s.Denials, res.Denials...)
		s.Notes = append(s.Notes, res.Notes...)
	})
}

// addRun adds one run's usage to a step's and to its session's (D53). A
// resumed Claude session usually reports the session's totals, earlier
// calls included: then the run's share is the new total minus the
// session's earlier one. If the total is smaller than the earlier total
// plus this call's input, Claude didn't restore the earlier calls, and
// the run's numbers are its own.
func addRun(step, session, run state.Usage, callInput int64, resumedClaude bool) (newStep, newSession state.Usage) {
	switch {
	case !resumedClaude:
		return step.Plus(run), run
	case run.InputTokens >= session.InputTokens+callInput:
		return step.Minus(session).Plus(run), run
	default:
		return step.Plus(run), session.Plus(run)
	}
}

// limitWait says until when to wait for a usage limit, and whether to
// wait at all: only if the reset time is known and at most [limits]
// max_wait_hours away.
func (e *engine) limitWait(l *runner.Limit) (time.Time, bool) {
	if l.Reset.IsZero() {
		return time.Time{}, false
	}
	until := l.Reset.Add(time.Minute)
	if now := e.o.Now(); until.Before(now) {
		until = now
	}
	return until, until.Sub(e.o.Now()) <= time.Duration(e.cfg.Limits.MaxWaitHours)*time.Hour
}

func limitText(name string, l *runner.Limit) string {
	s := fmt.Sprintf("%s hit a usage limit: %s", name, l.Message)
	if !l.Reset.IsZero() {
		s += fmt.Sprintf("\nIt resets at %s.", l.Reset.Local().Format("Mon 15:04"))
	}
	return s
}

// unchanged reports whether the tree is clean and HEAD is still head.
func (e *engine) unchanged(ctx context.Context, head string) bool {
	st, err := e.repo.Status(ctx)
	return err == nil && st == "" && e.headOr(ctx) == head
}

// agentFailed pauses after runAgent gave up on a step.
func (e *engine) agentFailed(ctx context.Context, s *state.Step, step string, o outcome) bool {
	switch o.reason {
	case PauseUsageLimit:
		return e.pause(PauseUsageLimit, o.problem, "Run twokey resume once the limit has reset. The step runs again.", s.Name, nil)
	case PauseTimeout:
		key := "[timeouts] " + timeoutKey(step)
		if s.Name == state.Fix {
			status, _ := e.repo.Status(ctx)
			return e.pause(PauseTimeout, fmt.Sprintf("The fix step ran out of time (%s = %d minutes). It may have left changes:%s",
				key, e.timeoutMinutes(step), indent(status)),
				"Look at the repository. Commit or discard the changes yourself (twokey never does), then run twokey resume. Raise "+key+" if the fix needs longer.", s.Name, nil)
		}
		return e.pause(PauseTimeout, fmt.Sprintf("%s ran out of time twice (%s = %d minutes):\n%s", s.Name, key, e.timeoutMinutes(step), o.problem),
			"Raise "+key+" in the config if the step needs longer, then run twokey resume.", s.Name, nil)
	}
	if s.Name == state.Fix {
		return e.pause(PauseAgentFailed, fmt.Sprintf("The fix step gave no usable answer, even after one retry:\n%s", o.problem),
			"Look at the step's files and the repository. Undo any commits you don't want, then run twokey resume.", s.Name, nil)
	}
	return e.pause(PauseAgentFailed, fmt.Sprintf("%s gave no usable answer, even after one retry:\n%s", s.Name, o.problem),
		"Look at the step's files in the run folder, then run twokey resume to try again.", s.Name, nil)
}

func timeoutKey(step string) string {
	switch step {
	case schema.Convergence, schema.Dispute, schema.Fix:
		return step
	}
	return "audit"
}

func (e *engine) timeoutMinutes(step string) int {
	t := e.cfg.Timeouts
	switch step {
	case schema.Convergence:
		return t.Convergence
	case schema.Dispute:
		return t.Dispute
	case schema.Fix:
		return t.Fix
	}
	return t.Audit
}

// stepKind is the schema step of a step name: codex-audit is an audit.
func stepKind(name string) string {
	if strings.HasSuffix(name, "-audit") {
		return schema.Audit
	}
	return name
}

// cleanupDocker removes the step's Docker resources by prefix (D12). It
// runs even after Ctrl+C.
func (e *engine) cleanupDocker(ctx context.Context, r *state.Round, s *state.Step) {
	if e.o.Docker == nil || s == nil {
		return
	}
	short := "cx"
	if s.Agent == "claude" {
		short = "cc"
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	removed, err := e.o.Docker.Cleanup(cctx, containerPrefix(e.st.RunID, r.N, stepKind(s.Name), short))
	switch {
	case errors.Is(err, docker.ErrNotRunning):
		e.dockerWarn.Do(func() {
			ui.Line(e.o.Out, ui.Warn, "Docker is not running, so twokey skips the Docker cleanup after each step.")
		})
	case err != nil:
		ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("Docker cleanup after %s: %v", s.Name, err))
	}
	if len(removed) > 0 {
		ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Removed what %s left in Docker: %s", s.Name, strings.Join(removed, ", ")))
	}
}

// newSessionID returns a random UUID (version 4) for a new Claude session.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// heartbeat prints the running steps every interval (SPEC section 9):
// "[..] round 2: codex-audit 14 min, claude-audit 11 min".
type heartbeat struct {
	mu     sync.Mutex
	active map[string]beatItem
}

type beatItem struct {
	round int
	start time.Time
}

// add marks a step as running; the returned function marks it done.
func (h *heartbeat) add(round int, name string, now time.Time) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[name] = beatItem{round, now}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.active, name)
	}
}

// line describes the running steps, or "" if none runs.
func (h *heartbeat) line(now time.Time) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.active) == 0 {
		return ""
	}
	names := make([]string, 0, len(h.active))
	for n := range h.active {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	round := 0
	for _, n := range names {
		it := h.active[n]
		round = it.round
		parts = append(parts, fmt.Sprintf("%s %d min", n, int(now.Sub(it.start).Minutes())))
	}
	return fmt.Sprintf("round %d: %s", round, strings.Join(parts, ", "))
}

// run prints the heartbeat until the returned function is called.
func (h *heartbeat) run(out io.Writer, every time.Duration, now func() time.Time) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if l := h.line(now()); l != "" {
					ui.Line(out, ui.Busy, l)
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}

// lockedWriter lets parallel steps and the heartbeat print whole lines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
