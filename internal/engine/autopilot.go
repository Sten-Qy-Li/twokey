package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/gate"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// How a run makes decisions and rulings (SPEC section 10).
const (
	AskOwner    = ""            // at the terminal prompt: stepper mode
	Recommended = "recommended" // autopilot: answer for the owner (D61)
	WaitOwner   = "pause"       // autopilot with [autopilot] decide = false: pause and exit
)

// answer handles a gate result that needs the owner: a decision, a ruling
// on a maintained dispute, or a ruling on a recurring finding. Autopilot
// never waits at a prompt (D60).
func (e *engine) answer(r *state.Round, g gate.Result) bool {
	switch e.o.Decisions {
	case Recommended:
		return e.autoDecide(r, g)
	case WaitOwner:
		return e.pause(string(g.Action), fmt.Sprintf("Round %d needs your answer on %s.", r.N, strings.Join(g.IDs, ", ")),
			"Run twokey resume without --autopilot, which asks you, or answer each with twokey decide <ID> <answer>.", "", g.IDs)
	}
	return e.ask(r, g)
}

// autoDecide answers for the owner in autopilot (DECISIONS.md D61). A
// decision takes the merged finding's recommended option, and a finding
// that Codex maintained after the dispute is fixed: the next round's audits
// check the result. A decision without a recommendation, and a finding that
// keeps coming back, are skipped this round: they wait for the owner, and
// the rest is fixed.
func (e *engine) autoDecide(r *state.Round, g gate.Result) bool {
	byID := mergedByID(r)
	lineage := e.lineage()
	for _, id := range g.IDs {
		m := byID[id]
		var ok bool
		switch {
		case g.Action == gate.PauseAdjudicate:
			ok = e.record(r, m, "ruling", gate.RuleFix, e.maintainedText(r, m), true)
		case g.Action == gate.PauseDecision && m.RecommendedOption != "":
			ok = e.record(r, m, "decision", m.RecommendedOption, e.recommendedText(r, m), true)
		case g.Action == gate.PauseDecision:
			ok = e.skip(r, id, "it needs your decision, and no option was recommended. Options: "+numbered(m.Options))
		default:
			ok = e.skip(r, id, fmt.Sprintf("it came back %d times after being fixed (latest: recurs_of %s). Fix it once more, or drop it", lineage[id], m.RecursOf))
		}
		if !ok {
			return false
		}
	}
	return true
}

// skip leaves a finding for the owner this round (D61).
func (e *engine) skip(r *state.Round, id, why string) bool {
	e.update(func() {
		if !slices.ContainsFunc(r.Skipped, func(s state.Skip) bool { return s.ID == id }) {
			r.Skipped = append(r.Skipped, state.Skip{ID: id, Why: why})
		}
	})
	ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("Skipped %s this round: %s. It waits for you; the rest is fixed.", id, why))
	return true
}

// recommendedText says where an automatic decision's option came from: the
// convergence step, and the blind audits that recommended an option too.
func (e *engine) recommendedText(r *state.Round, m schema.MergedFinding) string {
	s := fmt.Sprintf("Options: %s. Recommended by the convergence step (Claude Code)", numbered(m.Options))
	var by []string
	for _, a := range []struct{ step, agent string }{{state.CodexAudit, "Codex"}, {state.ClaudeAudit, "Claude Code"}} {
		for _, f := range loadJSON[schema.AuditResult](resultOf(r, a.step)).Findings {
			if slices.Contains(m.MergedFrom, f.ID) && f.RecommendedOption != "" {
				by = append(by, fmt.Sprintf("%s (%s) recommended %q", f.ID, a.agent, f.RecommendedOption))
			}
		}
	}
	if len(by) > 0 {
		s += "; in the blind audits, " + strings.Join(by, " and ")
	}
	return s + "."
}

// maintainedText gives both sides of an automatic ruling.
func (e *engine) maintainedText(r *state.Round, m schema.MergedFinding) string {
	evidence := ""
	for _, rc := range loadJSON[schema.DisputeResult](resultOf(r, state.Dispute)).Rechecks {
		if rc.ID == m.ID {
			evidence = rc.Evidence
		}
	}
	return fmt.Sprintf("The convergence step (Claude Code) disputed it: %s. Codex maintained it in the dispute step: %s. So twokey fixes it, and the next round's audits check the result.",
		strings.TrimRight(cut(m.DisputeReason, 300), "."), strings.TrimRight(cut(evidence, 300), "."))
}

func numbered(options []string) string {
	if len(options) == 0 {
		return "none given"
	}
	var parts []string
	for i, o := range options {
		parts = append(parts, fmt.Sprintf("%d. %s", i+1, o))
	}
	return strings.Join(parts, "; ")
}

func mergedByID(r *state.Round) map[string]schema.MergedFinding {
	byID := map[string]schema.MergedFinding{}
	for _, m := range loadJSON[schema.ConvergenceResult](resultOf(r, state.Convergence)).Findings {
		byID[m.ID] = m
	}
	return byID
}

func skippedSet(r *state.Round) map[string]bool {
	set := map[string]bool{}
	for _, s := range r.Skipped {
		set[s.ID] = true
	}
	return set
}

// waiting lists the skipped findings the owner hasn't answered yet.
func (e *engine) waiting() []string {
	var lines []string
	for _, r := range e.st.Rounds {
		for _, s := range r.Skipped {
			if e.st.Decisions[s.ID] == "" && e.st.Rulings[s.ID] == "" {
				lines = append(lines, fmt.Sprintf("- %s (round %d): %s. To answer: `twokey decide %s <answer>`", s.ID, r.N, s.Why, s.ID))
			}
		}
	}
	return lines
}

// writePending keeps pending.md in step with what waits for the owner when
// a round ends without a pause: the findings autopilot skipped, or nothing.
func (e *engine) writePending() {
	path := filepath.Join(e.runDir, "pending.md")
	lines := e.waiting()
	if len(lines) == 0 {
		_ = os.Remove(path)
		return
	}
	text := fmt.Sprintf("# twokey run %s: waiting for you\n\n"+
		"Autopilot skipped these findings, because they need your answer. The rest was fixed.\n\n%s\n\n"+
		"**To answer:** run `twokey decide <ID> <answer>` in the repository (an option number, your own text, fix or drop). Later rounds follow it.\n",
		e.st.RunID, strings.Join(lines, "\n"))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't write pending.md: "+err.Error())
	}
	ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("%s for you: twokey skipped them (pending.md lists them).", plural(len(lines), "finding waits")))
}

// Decide records the owner's answer on a finding of the latest run ("twokey
// decide", DECISIONS.md D61), for example to replace an automatic decision
// or to answer a skipped finding. Later steps of the run follow it.
func Decide(ctx context.Context, o Options, id, answer string) int {
	e := newEngine(o)
	dir, err := FindRun(e.runRoot, "")
	if err == nil {
		e.st, err = state.Load(dir)
	}
	if err != nil {
		ui.Line(o.Out, ui.Fail, ui.Sentence(err.Error()))
		return ExitError
	}
	e.setRun(e.st.RunID)
	lk, err := lock.Acquire(e.runRoot, lock.Info{PID: os.Getpid(), RunID: e.st.RunID, Started: e.o.Now().UTC()})
	if err != nil {
		e.lockFailed(err)
		return ExitError
	}
	defer e.release(lk)
	if e.st.Status == state.Converged {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("Run %s has converged, so an answer can't change it anymore.", e.st.RunID))
		ui.Next(o.Out, "twokey round starts a new run once the branch moves.")
		return ExitError
	}
	r, m, found := e.finding(id)
	if !found {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("Run %s has no finding %s.", e.st.RunID, id))
		ui.Next(o.Out, "report.md in "+e.runDir+" lists the findings.")
		return ExitError
	}
	kind, value, problem := e.ownerAnswer(m, answer)
	if problem != "" {
		ui.Line(o.Out, ui.Fail, problem)
		return ExitError
	}
	detail := "Your answer"
	if prev, ok := e.st.Automatic[m.ID]; ok {
		detail += ". It replaces the automatic choice: " + prev
	}
	if !e.record(r, m, kind, value, detail, false) {
		return e.exitCode()
	}
	e.update(func() {
		for _, rr := range e.st.Rounds {
			rr.Skipped = slices.DeleteFunc(rr.Skipped, func(s state.Skip) bool { return s.ID == m.ID })
		}
	})
	if e.st.Status == state.RoundDone {
		e.writePending()
	}
	ui.Detail(o.Out, "Later steps of run "+e.st.RunID+" follow it.")
	if e.st.Status == state.Paused {
		ui.Next(o.Out, "twokey resume, or twokey round")
	} else {
		ui.Next(o.Out, "twokey round")
	}
	return ExitDone
}

// finding finds a merged finding in the run's convergence answers.
func (e *engine) finding(id string) (*state.Round, schema.MergedFinding, bool) {
	for _, r := range e.st.Rounds {
		if m, ok := mergedByID(r)[id]; ok {
			return r, m, true
		}
	}
	return nil, schema.MergedFinding{}, false
}

// ownerAnswer reads a "twokey decide" answer. A finding that was disputed,
// that keeps coming back, or that isn't a decision takes fix or drop; a
// decision takes an option number, the owner's own text, or drop.
func (e *engine) ownerAnswer(m schema.MergedFinding, answer string) (kind, value, problem string) {
	a := strings.TrimSpace(answer)
	if m.Status == "disputed" || e.lineage()[m.ID] >= 2 || e.st.Rulings[m.ID] != "" || m.Kind != "needs_decision" {
		if a == gate.RuleFix || a == gate.RuleDrop {
			return "ruling", a, ""
		}
		return "", "", fmt.Sprintf("%s takes fix or drop (got %q).", m.ID, a)
	}
	if n, err := strconv.Atoi(a); err == nil {
		if n < 1 || n > len(m.Options) {
			return "", "", fmt.Sprintf("%s has options 1 to %d (got %d): %s.", m.ID, len(m.Options), n, numbered(m.Options))
		}
		return "decision", m.Options[n-1], ""
	}
	if a == "" {
		return "", "", fmt.Sprintf("Give %s an option number, your own answer, or drop. Its options: %s.", m.ID, numbered(m.Options))
	}
	return "decision", a, ""
}
