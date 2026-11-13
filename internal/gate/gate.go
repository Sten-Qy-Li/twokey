// Package gate decides what happens after a round's agent steps (SPEC
// section 4). It is a pure function: no I/O, fully table-tested.
package gate

import (
	"fmt"

	"github.com/Sten-Qy-Li/twokey/internal/schema"
)

// Action is the gate's result.
type Action string

// The gate's results, in the order the checks run.
const (
	PauseInvariant  Action = "PAUSE_INVARIANT"
	RunDispute      Action = "RUN_DISPUTE"
	PauseAdjudicate Action = "PAUSE_ADJUDICATE"
	PauseDecision   Action = "PAUSE_DECISION"
	PauseRecurring  Action = "PAUSE_RECURRING"
	StopConverged   Action = "STOP_CONVERGED"
	StopMaxRounds   Action = "STOP_MAX_ROUNDS"
	Fix             Action = "FIX"
)

// Owner answers, as stored in Rulings and Decisions.
const (
	RuleFix  = "fix"
	RuleDrop = "drop"
)

// Input is everything the gate looks at.
type Input struct {
	Round, MaxRounds int
	// SingleRound means "twokey round": the round always gets its fix, so
	// max_rounds doesn't stop it (DECISIONS.md D58).
	SingleRound     bool
	FixP3           bool
	InvariantFailed bool

	Codex, Claude schema.AuditResult
	Convergence   schema.ConvergenceResult
	Dispute       *schema.DisputeResult // nil until the dispute step has run

	// Rulings holds the owner's "fix" or "drop" per merged finding ID, for
	// maintained disputes and for recurring findings.
	Rulings map[string]string
	// Decisions holds the owner's choice per needs_decision finding: an
	// option, their own text, or "drop".
	Decisions map[string]string
	// Lineage holds how often each merged finding has recurred (D21).
	Lineage map[string]int
}

// Result is the gate's decision.
type Result struct {
	Action Action
	IDs    []string // the merged findings the action is about
	Notes  []string // for the report, such as a verdict that contradicts the findings
}

// Decide runs the checks of SPEC section 4 in order; the first match wins.
func Decide(in Input) Result {
	res := Result{Notes: verdictNotes(in)}
	if in.InvariantFailed {
		res.Action = PauseInvariant
		return res
	}

	codexBlocking := map[string]bool{}
	for _, f := range in.Codex.Findings {
		if f.Blocking() {
			codexBlocking[f.ID] = true
		}
	}
	rechecks := map[string]string{}
	if in.Dispute != nil {
		for _, r := range in.Dispute.Rechecks {
			rechecks[r.ID] = r.Decision
		}
	}
	dropped := func(id string) bool { return in.Rulings[id] == RuleDrop || in.Decisions[id] == RuleDrop }

	var toDispute, maintained, confirmed []string
	byID := map[string]schema.MergedFinding{}
	for _, m := range in.Convergence.Findings {
		byID[m.ID] = m
		switch m.Status {
		case "confirmed":
			if m.Blocking() && !dropped(m.ID) {
				confirmed = append(confirmed, m.ID)
			}
		case "disputed":
			if !anyIn(m.MergedFrom, codexBlocking) {
				continue // only Claude raised it: Claude withdrew its own finding (case b)
			}
			switch {
			case in.Dispute == nil:
				toDispute = append(toDispute, m.ID)
			case rechecks[m.ID] == "withdraw":
				// Codex withdrew it (case a).
			case in.Rulings[m.ID] == RuleFix:
				confirmed = append(confirmed, m.ID)
			case in.Rulings[m.ID] == RuleDrop:
				// The owner dropped it (case c).
			default:
				maintained = append(maintained, m.ID)
			}
		}
	}

	var needDecision, recurring []string
	for _, id := range confirmed {
		m := byID[id]
		if m.Kind == "needs_decision" && in.Decisions[id] == "" {
			needDecision = append(needDecision, id)
		}
		if in.Lineage[id] >= 2 && in.Rulings[id] == "" {
			recurring = append(recurring, id)
		}
	}

	switch {
	case len(toDispute) > 0:
		res.Action, res.IDs = RunDispute, toDispute
	case len(maintained) > 0:
		res.Action, res.IDs = PauseAdjudicate, maintained
	case len(needDecision) > 0:
		res.Action, res.IDs = PauseDecision, needDecision
	case len(recurring) > 0:
		res.Action, res.IDs = PauseRecurring, recurring
	case len(confirmed) == 0:
		res.Action = StopConverged
	case in.Round >= in.MaxRounds && !in.SingleRound:
		res.Action, res.IDs = StopMaxRounds, confirmed
	default:
		res.Action, res.IDs = Fix, confirmed
		if in.FixP3 {
			res.IDs = append(res.IDs, p3Fixes(in.Convergence, dropped)...)
		}
	}
	return res
}

// p3Fixes returns the P3 repo_fix items to fix when fix_p3 is on. The
// convergence step marks P3 items "noted", so noted and confirmed both
// count; disputed ones don't.
func p3Fixes(c schema.ConvergenceResult, dropped func(string) bool) []string {
	var ids []string
	for _, m := range c.Findings {
		if m.Severity == "P3" && m.Kind == "repo_fix" && m.Status != "disputed" && !dropped(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// verdictNotes reports verdicts that say "ready" although blocking
// findings exist. The findings win (SPEC section 4).
func verdictNotes(in Input) []string {
	var notes []string
	for _, a := range []struct {
		who string
		r   schema.AuditResult
	}{{"Codex's audit", in.Codex}, {"Claude's audit", in.Claude}} {
		if a.r.Verdict == "ready_for_human_review" && anyBlocking(a.r.Findings) {
			notes = append(notes, fmt.Sprintf("%s said ready_for_human_review but raised blocking findings.", a.who))
		}
	}
	if in.Convergence.Verdict == "ready_for_human_review" {
		for _, m := range in.Convergence.Findings {
			if m.Status == "confirmed" && m.Blocking() {
				notes = append(notes, "The convergence step said ready_for_human_review but confirmed blocking findings.")
				break
			}
		}
	}
	return notes
}

func anyBlocking(fs []schema.Finding) bool {
	for _, f := range fs {
		if f.Blocking() {
			return true
		}
	}
	return false
}

func anyIn(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}

// Lineage counts how often a merged finding has recurred: if X recurs_of Y
// and Y recurs_of Z, X's lineage is 2 (D21). recursOf maps every merged ID
// of the run to its recurs_of value.
func Lineage(id string, recursOf map[string]string) int {
	n := 0
	seen := map[string]bool{id: true}
	for cur := recursOf[id]; cur != "" && !seen[cur]; cur = recursOf[cur] {
		seen[cur] = true
		n++
	}
	return n
}
