package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/gate"
	"github.com/Sten-Qy-Li/twokey/internal/invariant"
	"github.com/Sten-Qy-Li/twokey/internal/logfile"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// preflight checks the repository before round 1 (SPEC section 8).
func (e *engine) preflight(ctx context.Context) bool {
	out, p := e.o.Out, e.cfg.Project
	ok := true
	fail := func(msg, next string) {
		ok = false
		ui.Line(out, ui.Fail, msg)
		if next != "" {
			ui.Next(out, next)
		}
	}
	if cur, err := e.repo.CurrentBranch(ctx); err != nil || cur != p.Branch {
		fail(fmt.Sprintf("The repository must be on %s.", p.Branch), "git switch "+p.Branch)
	}
	if st, err := e.repo.Status(ctx); err != nil || st != "" {
		fail("The working tree is not clean:"+indent(st), "commit the changes, or make git ignore them.")
	}
	if ign, err := e.repo.IsIgnored(ctx, p.RunDir); err != nil || !ign {
		fail("git does not ignore "+p.RunDir+".", "add this line to .gitignore: /"+p.RunDir+"/")
	}
	if err := e.repo.Fetch(ctx, p.Remote); err != nil {
		fail("git fetch failed: "+err.Error(), "check your network and login.")
	} else {
		head, _ := e.repo.RevParse(ctx, "HEAD")
		remote, err := e.repo.RevParse(ctx, "refs/remotes/"+p.Remote+"/"+p.Branch)
		if err != nil || head != remote {
			fail(fmt.Sprintf("HEAD must equal %s/%s after git fetch.", p.Remote, p.Branch), "push or pull until they match (twokey doctor shows how).")
		}
		if p.Upstream != "" {
			if _, err := e.repo.RevParse(ctx, "refs/remotes/"+p.Remote+"/"+p.Upstream); err != nil {
				fail(fmt.Sprintf("%s/%s doesn't exist after git fetch.", p.Remote, p.Upstream), "check [project] upstream, or set it to \"\" for off.")
			}
		}
	}
	req := e.cfg.Checkpoint.RequirementsFile
	if !filepath.IsAbs(req) {
		req = filepath.Join(e.o.Root, req)
	}
	if _, err := os.Stat(req); err != nil {
		fail("The requirements file is missing: "+e.cfg.Checkpoint.RequirementsFile, "")
	}
	if data, err := os.ReadFile(e.logPath); err != nil {
		fail("The log file is missing: "+p.LogFile, "")
	} else if n := len(logfile.Inspect(data, p.LogMarker).MarkerLines); n != 1 {
		fail(fmt.Sprintf("The log marker line must appear once in %s (found %d).", p.LogFile, n), "")
	}
	if !e.o.FakeAgents {
		for _, c := range []string{e.cfg.Codex.Command, e.cfg.Claude.Command} {
			if _, err := e.o.Proc.LookPath(c); err != nil {
				fail(c+" is not on PATH.", "run twokey doctor.")
			}
		}
	}
	if ok {
		ui.Line(out, ui.OK, "Preflight passed")
	}
	return ok
}

// Agent display names for prompts.
var agentNames = map[string]string{"codex": "Codex", "claude": "Claude Code"}

func otherAgent(agent string) string {
	if agent == "codex" {
		return agentNames["claude"]
	}
	return agentNames["codex"]
}

// roundDir is where a round's files go in the run folder.
func (e *engine) roundDir(r *state.Round) string {
	return filepath.Join(e.runDir, fmt.Sprintf("r%d", r.N))
}

// prepare renders a step's prompt and saves it in the run folder.
func (e *engine) prepare(r *state.Round, s *state.Step, step string, extra map[string]string) (string, error) {
	vars := e.vars(r, s, step)
	for k, v := range extra {
		vars[k] = v
	}
	prompt, err := e.tmpl.Prompt(step, vars)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.roundDir(r), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(e.roundDir(r), s.Name+".prompt.md")
	if err := os.WriteFile(path, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	e.update(func() { s.Prompt = path })
	return path, nil
}

// before asks the owner in stepper mode, and opens the editor on "e". It
// returns the prompts to send, by step name, or false if the owner quit.
func (e *engine) before(what string, steps []*state.Step) (map[string]string, bool) {
	var paths []string
	for _, s := range steps {
		paths = append(paths, s.Prompt)
	}
	switch e.o.UI.Before(what, paths) {
	case Quit:
		return nil, false
	case Edit:
		for _, s := range steps {
			edited := strings.TrimSuffix(s.Prompt, ".prompt.md") + ".prompt.edited.md"
			data, err := os.ReadFile(s.Prompt)
			if err == nil {
				err = os.WriteFile(edited, data, 0o644)
			}
			if err != nil {
				ui.Line(e.o.Out, ui.Warn, "Can't copy the prompt for editing: "+err.Error())
				continue
			}
			e.openEditor(edited)
			if !e.o.UI.Edited(edited) {
				return nil, false
			}
			e.update(func() { s.Prompt = edited })
		}
	}
	prompts := map[string]string{}
	for _, s := range steps {
		data, err := os.ReadFile(s.Prompt)
		if err != nil {
			ui.Line(e.o.Out, ui.Fail, "Can't read the prompt: "+err.Error())
			return nil, false
		}
		prompts[s.Name] = string(data)
	}
	return prompts, true
}

func (e *engine) openEditor(path string) {
	f := strings.Fields(e.o.Editor)
	if len(f) == 0 {
		return
	}
	r := e.o.Proc.Run(context.Background(), proc.Cmd{Name: f[0], Args: append(f[1:], path)})
	if r.Err != nil {
		ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("The editor (%s) reported a problem: %v. Edit the file yourself.", e.o.Editor, r.Err))
	}
}

func retryPrompt(probs []string) string {
	return "twokey can't use your answer, for these reasons:\n- " + strings.Join(probs, "\n- ") +
		"\n\nFix these problems and answer again with only the JSON object that the schema asks for. The rules from the start of this session still apply.\n"
}

// stagingPath keeps answers outside the repository until both blind
// audits are done (SPEC section 9, "Blindness").
func (e *engine) stagingPath(r *state.Round, s *state.Step) string {
	return filepath.Join(e.staging, fmt.Sprintf("r%d-%s.json", r.N, s.Name))
}

// publish copies an answer from staging into the run folder, with the
// CLI's raw output of every try: r1-codex-audit.2.events.jsonl in staging
// becomes r1/codex-audit.2.events.jsonl.
func (e *engine) publish(r *state.Round, s *state.Step) error {
	data, err := os.ReadFile(e.stagingPath(r, s))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.roundDir(r), 0o755); err != nil {
		return err
	}
	stem := fmt.Sprintf("r%d-%s.", r.N, s.Name)
	raw, _ := filepath.Glob(filepath.Join(e.staging, stem+"*.*"))
	for _, src := range raw {
		b, err := os.ReadFile(src)
		if err == nil {
			err = os.WriteFile(filepath.Join(e.roundDir(r), s.Name+"."+strings.TrimPrefix(filepath.Base(src), stem)), b, 0o600)
		}
		if err != nil {
			return err
		}
	}
	path := filepath.Join(e.roundDir(r), s.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	e.update(func() { s.Result = path })
	return nil
}

// logStep writes a published step's log entry.
func (e *engine) logStep(r *state.Round, s *state.Step, body string) bool {
	h, err := e.insertLog(e.label(s.Agent, s.Model), r.N, s.Name, body)
	if err != nil {
		return e.logFailed(err)
	}
	e.update(func() { s.Heading = h })
	return true
}

func (e *engine) snapshot(ctx context.Context) (invariant.Snapshot, bool) {
	snap, err := e.taker.Take(ctx)
	if err != nil {
		return snap, e.pause(PauseInvariant, "Can't check the repository: "+err.Error(), "Check the repository and your network, then run twokey resume.", "", nil)
	}
	return snap, true
}

// maskTeammatePush hides a change of the working branch on the remote that
// didn't come from this repository: someone else pushed, which the push
// step handles (PAUSE_REMOTE_MOVED). If the remote now has a commit from
// local history, an agent pushed, and the change stays a violation.
func (e *engine) maskTeammatePush(ctx context.Context, before invariant.Snapshot, after *invariant.Snapshot) {
	b := e.st.Branch
	sha := after.Remote[b]
	if sha == before.Remote[b] {
		return
	}
	if sha != "" && e.repo.CommitExists(ctx, sha) {
		if ok, _ := e.repo.IsAncestor(ctx, sha, after.Head); ok {
			return
		}
	}
	ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("Someone else pushed to %s/%s during this step. twokey will pause before its own push.", e.st.Remote, b))
	after.Remote[b] = before.Remote[b]
}

// checkStep compares the snapshots around an agent step (SPEC section 8),
// plus state.json against what twokey last wrote. It pauses and returns
// false if the step changed what it must not change. dirtyOK accepts
// uncommitted changes, for a fix that was cut short.
func (e *engine) checkStep(ctx context.Context, what string, before, after invariant.Snapshot, fix, dirtyOK bool) bool {
	e.maskTeammatePush(ctx, before, &after)
	moved := e.movedProtected(before, &after)
	if dirtyOK {
		after.Status = ""
	}
	var v []string
	if fix {
		v = invariant.AfterFix(before, after, e.st.Branch, nil)
	} else {
		v = invariant.ReadOnly(before, after, nil)
	}
	if h, err := logfile.Hash(filepath.Join(e.runDir, state.FileName)); err != nil || h != e.stateHash {
		v = append(v, "twokey's file state.json was changed.")
	}
	if len(v) == 0 && len(moved) == 0 {
		return true
	}
	return e.stepViolated(ctx, what, v, before.Remote, moved)
}

func (e *engine) invariantFailed(what string, problems []string) bool {
	msg := fmt.Sprintf("%s changed things it must not change:\n- %s", what, strings.Join(problems, "\n- "))
	next := "Look at the changes and undo what the agent shouldn't have done. If you made the change yourself (for example, you edited the log), just resume."
	return e.pause(PauseInvariant, msg, next, what, nil)
}

// finishStep publishes a step's answer and writes its log entry. It skips
// what an earlier try already did, so a run that paused at the check after
// the step carries on from there on resume (DECISIONS.md D41).
func finishStep[T any](e *engine, r *state.Round, s *state.Step, entry func(T) string) (T, bool) {
	var v T
	if s.Result == "" {
		if err := e.publish(r, s); err != nil {
			return v, e.pause(PauseInvariant, "Can't publish "+s.Name+": "+err.Error(), "Check the run folder, then run twokey resume.", s.Name, nil)
		}
	}
	data, err := os.ReadFile(s.Result)
	if err == nil {
		err = json.Unmarshal(data, &v)
	}
	if err != nil {
		return v, e.pause(PauseInvariant, "Can't read the answer of "+s.Name+": "+err.Error(), "Check the run folder, then run twokey resume.", s.Name, nil)
	}
	if s.Heading == "" && !e.logStep(r, s, entry(v)) {
		return v, false
	}
	return v, true
}

// audits runs the blind audits in parallel and publishes both results
// only when both are done and checked (SPEC sections 3 and 9).
func (e *engine) audits(ctx context.Context, r *state.Round) bool {
	steps := []*state.Step{r.Ensure(state.CodexAudit, "codex"), r.Ensure(state.ClaudeAudit, "claude")}
	var todo []*state.Step
	for _, s := range steps {
		if s.Status == state.Done {
			continue
		}
		if _, err := e.prepare(r, s, schema.Audit, nil); err != nil {
			return e.pause(PauseAgentFailed, "Can't render the prompt: "+err.Error(), "Fix the template, then run twokey resume.", s.Name, nil)
		}
		todo = append(todo, s)
	}

	if len(todo) > 0 {
		prompts, ok := e.before(fmt.Sprintf("round %d: blind audits of %s (Codex and Claude, in parallel)", r.N, short(r.Commit)), todo)
		if !ok {
			return e.quit()
		}
		before, ok := e.snapshotBefore(ctx)
		if !ok {
			return false
		}
		results := make([]outcome, len(todo))
		var wg sync.WaitGroup
		for i, s := range todo {
			wg.Add(1)
			go func() {
				defer wg.Done()
				prefix := schema.IDPrefix(r.N, schema.CodexID)
				if s.Agent == "claude" {
					prefix = schema.IDPrefix(r.N, schema.ClaudeID)
				}
				results[i] = e.runAgent(ctx, r, s, schema.Audit, prompts[s.Name], "", func(b []byte) []string {
					var a schema.AuditResult
					if p := schema.Decode(schema.Audit, b, &a); p != nil {
						return p
					}
					return schema.CheckAudit(a, r.Commit, prefix)
				})
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			e.stop("Stopped (Ctrl+C).")
			return false
		}
		after, ok := e.snapshot(ctx)
		if !ok || !e.checkStep(ctx, fmt.Sprintf("Round %d's blind audits", r.N), before, after, false, false) {
			return false
		}
		for i, s := range todo {
			if results[i].problem != "" {
				return e.agentFailed(ctx, s, schema.Audit, results[i])
			}
		}
	}

	// Publish both, then log them oldest first, so the newer entry ends
	// up on top (SPEC section 7).
	for _, s := range steps {
		if err := e.publish(r, s); err != nil {
			return e.pause(PauseInvariant, "Can't publish "+s.Name+": "+err.Error(), "Check the run folder, then run twokey resume.", s.Name, nil)
		}
	}
	ordered := slices.Clone(steps)
	slices.SortStableFunc(ordered, func(a, b *state.Step) int { return a.Finished.Compare(b.Finished) })
	var tables []string
	for _, s := range ordered {
		a := loadJSON[schema.AuditResult](s.Result)
		if s.Heading == "" && !e.logStep(r, s, a.LogEntry) {
			return false
		}
		tables = append(tables, fmt.Sprintf("%s (%s): %s\n%s", s.Name, a.Verdict, a.Summary, auditTable(a.Findings)))
	}
	e.update(func() { r.Published = true })
	e.o.UI.After(strings.Join(tables, "\n"))
	return true
}

// convergence runs Claude's convergence step.
func (e *engine) convergence(ctx context.Context, r *state.Round) bool {
	s := r.Ensure(state.Convergence, "claude")
	if s.Status != state.Done {
		if _, err := e.prepare(r, s, schema.Convergence, nil); err != nil {
			return e.pause(PauseAgentFailed, "Can't render the prompt: "+err.Error(), "Fix the template, then run twokey resume.", s.Name, nil)
		}
		prompts, ok := e.before(fmt.Sprintf("round %d: convergence (Claude)", r.N), []*state.Step{s})
		if !ok {
			return e.quit()
		}
		codex := loadJSON[schema.AuditResult](r.Step(state.CodexAudit).Result)
		claude := loadJSON[schema.AuditResult](r.Step(state.ClaudeAudit).Result)
		prior := e.priorMergedIDs(r)
		if !e.readOnlyStep(ctx, r, s, schema.Convergence, prompts[s.Name], func(b []byte) []string {
			var c schema.ConvergenceResult
			if p := schema.Decode(schema.Convergence, b, &c); p != nil {
				return p
			}
			return schema.CheckConvergence(c, r.Commit, schema.IDPrefix(r.N, schema.ConvergenceID), codex, claude, prior)
		}) {
			return false
		}
	}
	c, ok := finishStep(e, r, s, func(c schema.ConvergenceResult) string { return c.LogEntry })
	if !ok {
		return false
	}
	g := e.decide(r)
	text := fmt.Sprintf("convergence (%s): %s\n%s\nGate: %s", c.Verdict, c.Summary, mergedTable(c.Findings),
		strings.TrimSpace(string(g.Action)+" "+strings.Join(g.IDs, ", ")))
	e.o.UI.After(text)
	return true
}

// readOnlyStep runs a step that may not change the repository and checks
// the invariants. The caller publishes the answer.
func (e *engine) readOnlyStep(ctx context.Context, r *state.Round, s *state.Step, step, prompt string, check func([]byte) []string) bool {
	before, ok := e.snapshotBefore(ctx)
	if !ok {
		return false
	}
	o := e.runAgent(ctx, r, s, step, prompt, "", check)
	if ctx.Err() != nil {
		e.stop("Stopped (Ctrl+C).")
		return false
	}
	after, ok := e.snapshot(ctx)
	if !ok || !e.checkStep(ctx, fmt.Sprintf("Round %d's %s step", r.N, s.Name), before, after, false, false) {
		return false
	}
	if o.problem != "" {
		return e.agentFailed(ctx, s, step, o)
	}
	return true
}

// dispute lets Codex re-check the findings convergence disputed.
func (e *engine) dispute(ctx context.Context, r *state.Round, ids []string) bool {
	s := r.Ensure(state.Dispute, "codex")
	if s.Status != state.Done {
		conv := loadJSON[schema.ConvergenceResult](r.Step(state.Convergence).Result)
		var list []string
		for _, m := range conv.Findings {
			if slices.Contains(ids, m.ID) {
				list = append(list, fmt.Sprintf("- %s (your %s) [%s %s] %s\n  Dispute reason: %s",
					m.ID, strings.Join(m.MergedFrom, ", "), m.Severity, m.Kind, m.Title, m.DisputeReason))
			}
		}
		if _, err := e.prepare(r, s, schema.Dispute, map[string]string{"disputes": strings.Join(list, "\n")}); err != nil {
			return e.pause(PauseAgentFailed, "Can't render the prompt: "+err.Error(), "Fix the template, then run twokey resume.", s.Name, nil)
		}
		prompts, ok := e.before(fmt.Sprintf("round %d: dispute re-check (Codex) of %s", r.N, strings.Join(ids, ", ")), []*state.Step{s})
		if !ok {
			return e.quit()
		}
		if !e.readOnlyStep(ctx, r, s, schema.Dispute, prompts[s.Name], func(b []byte) []string {
			var d schema.DisputeResult
			if p := schema.Decode(schema.Dispute, b, &d); p != nil {
				return p
			}
			return schema.CheckDispute(d, ids)
		}) {
			return false
		}
	}
	d, ok := finishStep(e, r, s, func(d schema.DisputeResult) string { return d.LogEntry })
	if !ok {
		return false
	}
	var lines []string
	for _, rc := range d.Rechecks {
		lines = append(lines, fmt.Sprintf("  %s: %s", rc.ID, rc.Decision))
	}
	e.o.UI.After("dispute:\n" + strings.Join(lines, "\n"))
	return true
}

// ask gets the owner's decisions or rulings (SPEC section 10).
func (e *engine) ask(r *state.Round, g gate.Result) bool {
	conv := loadJSON[schema.ConvergenceResult](r.Step(state.Convergence).Result)
	evidence := map[string]string{}
	if r.IsDone(state.Dispute) {
		for _, rc := range loadJSON[schema.DisputeResult](r.Step(state.Dispute).Result).Rechecks {
			evidence[rc.ID] = rc.Evidence
		}
	}
	lineage := e.lineage()
	for _, id := range g.IDs {
		var m schema.MergedFinding
		for _, f := range conv.Findings {
			if f.ID == id {
				m = f
			}
		}
		var kind, answer, note string
		var quit bool
		switch g.Action {
		case gate.PauseDecision:
			kind = "decision"
			answer, quit = e.o.UI.Decide(m)
		case gate.PauseAdjudicate:
			kind = "ruling"
			answer, note, quit = e.o.UI.Rule(m, evidence[id])
		default:
			kind = "ruling on a recurring finding"
			answer, note, quit = e.o.UI.Recurring(m, lineage[id])
		}
		if quit {
			return e.pause(string(g.Action), fmt.Sprintf("Round %d needs your answer on %s.", r.N, strings.Join(g.IDs, ", ")),
				"Run twokey resume and answer at the prompt.", "", g.IDs)
		}
		if !e.record(r, m, kind, answer, note) {
			return false
		}
	}
	return true
}

// record saves a decision or ruling: state, decisions.md and the log.
func (e *engine) record(r *state.Round, m schema.MergedFinding, kind, answer, note string) bool {
	e.update(func() {
		if kind == "decision" {
			e.st.Decisions[m.ID] = answer
		} else {
			e.st.Rulings[m.ID] = answer
		}
	})
	line := fmt.Sprintf("- **%s** (%s, round %d): %s → **%s**", m.ID, kind, r.N, m.Title, answer)
	if note != "" {
		line += ". Note: " + note
	}
	path := filepath.Join(e.runDir, "decisions.md")
	old, err := os.ReadFile(path)
	if err != nil {
		old = []byte(fmt.Sprintf("# Decisions and rulings (run %s)\n\n", e.st.RunID))
	}
	if err := os.WriteFile(path, append(old, []byte(line+"\n")...), 0o644); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't write decisions.md: "+err.Error())
	}
	if _, err := e.insertLog(e.twokeyLabel(), r.N, kind, line); err != nil {
		return e.logFailed(err)
	}
	ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Saved: %s → %s", m.ID, answer))
	return true
}

// fix runs Claude's fix step and checks its commits (SPEC section 8).
func (e *engine) fix(ctx context.Context, r *state.Round, ids []string) bool {
	s := r.Ensure(state.Fix, "claude")
	if s.Status != state.Done {
		if _, err := e.prepare(r, s, schema.Fix, e.fixVars(r, ids)); err != nil {
			return e.pause(PauseAgentFailed, "Can't render the prompt: "+err.Error(), "Fix the template, then run twokey resume.", s.Name, nil)
		}
		prompts, ok := e.before(fmt.Sprintf("round %d: fix (Claude) of %s", r.N, strings.Join(ids, ", ")), []*state.Step{s})
		if !ok {
			return e.quit()
		}
		if !e.runFix(ctx, r, s, ids, prompts[s.Name], "") {
			return false
		}
	}
	return e.fixDone(ctx, r, s)
}

// runFix runs or resumes the fix session, then checks what it did.
func (e *engine) runFix(ctx context.Context, r *state.Round, s *state.Step, ids []string, prompt, resume string) bool {
	before, ok := e.snapshotBefore(ctx)
	if !ok {
		return false
	}
	o := e.runAgent(ctx, r, s, schema.Fix, prompt, resume, func(b []byte) []string {
		var f schema.FixResult
		if p := schema.Decode(schema.Fix, b, &f); p != nil {
			return p
		}
		if p := schema.CheckFix(f, r.Commit, ids); p != nil {
			return p
		}
		return e.checkCommits(ctx, r, f.Commits)
	})
	if ctx.Err() != nil {
		e.stop("Stopped (Ctrl+C). The fix step may have left changes; resume checks them.")
		return false
	}
	// A fix that was cut short may leave changes; its own pause says so.
	cut := o.reason == PauseTimeout || o.reason == PauseUsageLimit
	after, ok := e.snapshot(ctx)
	if !ok || !e.checkStep(ctx, fmt.Sprintf("Round %d's fix step", r.N), before, after, true, cut) {
		return false
	}
	if o.problem != "" {
		return e.agentFailed(ctx, s, schema.Fix, o)
	}
	var f schema.FixResult
	_ = json.Unmarshal(o.answer, &f)
	if len(f.Commits) == 0 {
		return e.noProgress(s)
	}
	return true
}

func (e *engine) noProgress(s *state.Step) bool {
	return e.pause(PauseNoProgress, "The fix step made no commits.", "Look at the repository. Fix it yourself and commit, or run twokey resume to try the fix again.", s.Name, nil)
}

// fixDone checks the branch after a fix, then publishes and logs the
// answer. It also runs on resume after a pause at the fix's checks.
func (e *engine) fixDone(ctx context.Context, r *state.Round, s *state.Step) bool {
	head, _ := e.repo.RevParse(ctx, "HEAD")
	if desc, _ := e.repo.IsAncestor(ctx, r.Commit, head); !desc {
		return e.invariantFailed(fmt.Sprintf("Round %d's fix step", r.N), []string{"HEAD no longer descends from the round's commit " + short(r.Commit) + "."})
	}
	if head == r.Commit {
		return e.noProgress(s)
	}
	f, ok := finishStep(e, r, s, func(f schema.FixResult) string { return f.LogEntry })
	if !ok {
		return false
	}
	var lines []string
	for _, d := range f.Done {
		lines = append(lines, "  done:     "+d.ID+": "+d.Change)
	}
	for _, n := range f.NotDone {
		lines = append(lines, "  not done: "+n.ID+": "+n.Reason)
	}
	e.o.UI.After(fmt.Sprintf("fix: %s\n%s", plural(len(f.Commits), "commit"), strings.Join(lines, "\n")))
	return true
}

// checkCommits checks the facts behind a fix answer's commits (SPEC
// section 6): each exists, descends from the base and is on the branch.
func (e *engine) checkCommits(ctx context.Context, r *state.Round, commits []string) []string {
	var p []string
	for _, c := range commits {
		switch {
		case !e.repo.CommitExists(ctx, c):
			p = append(p, fmt.Sprintf("commit %s doesn't exist", c))
		case c == r.Commit:
			p = append(p, fmt.Sprintf("commit %s is the base commit, not a new one", c))
		default:
			if ok, _ := e.repo.IsAncestor(ctx, r.Commit, c); !ok {
				p = append(p, fmt.Sprintf("commit %s doesn't descend from %s", c, r.Commit))
			}
			if ok, _ := e.repo.IsAncestor(ctx, c, "HEAD"); !ok {
				p = append(p, fmt.Sprintf("commit %s is not on the branch", c))
			}
		}
	}
	return p
}

// verify runs the project's checks. A failure resumes the fix session
// once with the output (SPEC section 8).
func (e *engine) verify(ctx context.Context, r *state.Round) bool {
	if cmds := e.cfg.Verify.Commands; len(cmds) > 0 &&
		e.o.UI.Before(fmt.Sprintf("round %d: the checks (%s)", r.N, strings.Join(cmds, ", ")), nil) == Quit {
		return e.quit()
	}
	failed, output := e.runChecks(ctx, r)
	if failed == "" {
		e.update(func() { r.Verified = true })
		return true
	}
	fs := r.Step(state.Fix)
	if r.VerifyRetries > 0 || fs == nil || fs.SessionID == "" {
		return e.pause(PauseVerify, fmt.Sprintf("The check %q failed after the fix.", failed),
			"Read verify.log in the run folder. Fix the problem yourself and commit, then run twokey resume.", "verify", nil)
	}
	// The second answer replaces the first: it is published and logged too.
	e.update(func() { r.VerifyRetries, fs.Result, fs.Heading = r.VerifyRetries+1, "", "" })
	ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("The check %q failed. The fix session gets one more try.", failed))
	prompt := fmt.Sprintf("The project's check `%s` failed after your fix. Its output ends with:\n\n```\n%s\n```\n\n"+
		"Fix this and commit. Don't push. Then answer again with the JSON object: base_commit is still %s, and commits lists all your commits, oldest first.\n",
		failed, tail(output, 60), r.Commit)
	if !e.runFix(ctx, r, fs, r.Gate.IDs, prompt, fs.SessionID) || !e.fixDone(ctx, r, fs) {
		return false
	}
	if failed, _ = e.runChecks(ctx, r); failed != "" {
		return e.pause(PauseVerify, fmt.Sprintf("The check %q failed again after the second try.", failed),
			"Read verify.log in the run folder. Fix the problem yourself and commit, then run twokey resume.", "verify", nil)
	}
	e.update(func() { r.Verified = true })
	return true
}

// runChecks runs [verify] commands in order, saving their output to
// verify.log. It returns the first failed command and its output.
func (e *engine) runChecks(ctx context.Context, r *state.Round) (failed, output string) {
	if err := os.MkdirAll(e.roundDir(r), 0o755); err != nil {
		return "(verify.log)", err.Error()
	}
	logPath := filepath.Join(e.roundDir(r), "verify.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "(verify.log)", err.Error()
	}
	defer f.Close()
	timeout := time.Duration(e.cfg.Timeouts.Verify) * time.Minute
	for _, c := range e.cfg.Verify.Commands {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		res := e.o.Proc.Run(cctx, proc.Cmd{Dir: e.o.Root, Shell: c})
		cancel()
		out := res.Stdout + res.Stderr
		fmt.Fprintf(f, "$ %s\n%s\n[exit %d]\n\n", c, out, res.ExitCode)
		if res.Err != nil {
			ui.Line(e.o.Out, ui.Fail, "Check failed: "+c)
			return c, out
		}
		ui.Line(e.o.Out, ui.OK, "Check passed: "+c)
	}
	return "", ""
}

// push pushes the branch after its checks (SPEC section 8, "Push").
func (e *engine) push(ctx context.Context, r *state.Round) bool {
	remote, branch := e.st.Remote, e.st.Branch
	head := e.headOr(ctx)
	if e.o.UI.Before(fmt.Sprintf("push %s to %s/%s", short(head), remote, branch), nil) == Quit {
		return e.quit()
	}
	commits, _ := e.repo.NewCommits(ctx, r.Commit, head)
	if !e.pushHead(ctx, r, "push") {
		return false
	}

	var lines []string
	for _, c := range commits {
		subj, _ := e.repo.Subject(ctx, c)
		lines = append(lines, short(c)+" "+subj)
	}
	_ = os.WriteFile(filepath.Join(e.roundDir(r), "push.log"),
		[]byte(fmt.Sprintf("Pushed %s to %s/%s:\n%s\n", short(head), remote, branch, strings.Join(lines, "\n"))), 0o644)
	e.update(func() { r.Pushed, r.PushedSHA = true, head })
	e.pushed = true
	ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Pushed %s to %s/%s.", plural(len(commits), "commit"), remote, branch))
	return true
}

func (e *engine) headOr(ctx context.Context) string {
	h, _ := e.repo.RevParse(ctx, "HEAD")
	return h
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// loadJSON reads a published answer. Answers were validated before they
// were published, so a read error leaves the zero value.
func loadJSON[T any](path string) T {
	var v T
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &v)
	}
	return v
}
