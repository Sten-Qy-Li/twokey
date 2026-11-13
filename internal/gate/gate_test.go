package gate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/schema"
)

func f(id, sev, kind string) schema.Finding {
	return schema.Finding{ID: id, Title: "t", Severity: sev, Kind: kind}
}

func m(id, sev, kind, status string, from ...string) schema.MergedFinding {
	return schema.MergedFinding{Finding: f(id, sev, kind), MergedFrom: from, Status: status}
}

func audit(fs ...schema.Finding) schema.AuditResult {
	return schema.AuditResult{Verdict: "not_ready", Findings: fs}
}

func conv(ms ...schema.MergedFinding) schema.ConvergenceResult {
	return schema.ConvergenceResult{Verdict: "not_ready", Findings: ms}
}

func dispute(pairs ...string) *schema.DisputeResult {
	d := &schema.DisputeResult{}
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Rechecks = append(d.Rechecks, schema.Recheck{ID: pairs[i], Decision: pairs[i+1], Evidence: "e"})
	}
	return d
}

var (
	cx1 = f("R1-CX-01", "P1", "repo_fix")
	cx2 = f("R1-CX-02", "P2", "needs_decision")
	cc1 = f("R1-CC-01", "P2", "repo_fix")
)

// One case per row of SPEC section 4, plus combinations.
func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Action
		ids  []string
	}{
		// Row 1
		{"invariant failed wins over everything", Input{InvariantFailed: true, Codex: audit(cx1),
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, PauseInvariant, nil},
		// Row 2
		{"Codex finding disputed: run dispute", Input{Codex: audit(cx1),
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, RunDispute, []string{"R1-M-01"}},
		{"raised by both, disputed: Codex re-checks (D19)", Input{Codex: audit(cx1), Claude: audit(cc1),
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CC-01", "R1-CX-01"))}, RunDispute, []string{"R1-M-01"}},
		{"Claude-only finding disputed: withdrawn (case b)", Input{Claude: audit(cc1),
			Convergence: conv(m("R1-M-01", "P2", "repo_fix", "disputed", "R1-CC-01"))}, StopConverged, nil},
		{"Codex P3 disputed: no dispute step", Input{Codex: audit(f("R1-CX-01", "P3", "repo_fix")),
			Convergence: conv(m("R1-M-01", "P3", "repo_fix", "disputed", "R1-CX-01"))}, StopConverged, nil},
		{"dispute withdrawn (case a)", Input{Codex: audit(cx1), Dispute: dispute("R1-M-01", "withdraw"),
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, StopConverged, nil},
		// Row 3
		{"dispute maintained: adjudicate", Input{Codex: audit(cx1), Dispute: dispute("R1-M-01", "maintain"), MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, PauseAdjudicate, []string{"R1-M-01"}},
		{"maintained, ruled fix", Input{Codex: audit(cx1), Dispute: dispute("R1-M-01", "maintain"), Rulings: map[string]string{"R1-M-01": "fix"},
			Round: 1, MaxRounds: 4, Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, Fix, []string{"R1-M-01"}},
		{"maintained, ruled drop (case c)", Input{Codex: audit(cx1), Dispute: dispute("R1-M-01", "maintain"), Rulings: map[string]string{"R1-M-01": "drop"},
			Convergence: conv(m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"))}, StopConverged, nil},
		// Row 4
		{"needs a decision", Input{Codex: audit(cx2), Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-02"))}, PauseDecision, []string{"R1-M-01"}},
		{"decided: fix it", Input{Codex: audit(cx2), Decisions: map[string]string{"R1-M-01": "option B"}, Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-02"))}, Fix, []string{"R1-M-01"}},
		{"decided: drop it", Input{Codex: audit(cx2), Decisions: map[string]string{"R1-M-01": "drop"}, Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-02"))}, StopConverged, nil},
		// Row 5
		{"recurred twice", Input{Codex: audit(cx1), Lineage: map[string]int{"R3-M-01": 2}, Round: 3, MaxRounds: 4,
			Convergence: conv(m("R3-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))}, PauseRecurring, []string{"R3-M-01"}},
		{"recurred once: fix again", Input{Codex: audit(cx1), Lineage: map[string]int{"R2-M-01": 1}, Round: 2, MaxRounds: 4,
			Convergence: conv(m("R2-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))}, Fix, []string{"R2-M-01"}},
		{"recurred twice, ruled fix", Input{Codex: audit(cx1), Lineage: map[string]int{"R3-M-01": 2}, Rulings: map[string]string{"R3-M-01": "fix"}, Round: 3, MaxRounds: 4,
			Convergence: conv(m("R3-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))}, Fix, []string{"R3-M-01"}},
		// Row 6
		{"nothing found", Input{Round: 1, MaxRounds: 4}, StopConverged, nil},
		{"P3 only: converged", Input{Codex: audit(f("R1-CX-01", "P3", "repo_fix")), Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P3", "repo_fix", "noted", "R1-CX-01"))}, StopConverged, nil},
		{"P1 external_evidence only: converged", Input{Codex: audit(f("R1-CX-01", "P1", "external_evidence")), Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P1", "external_evidence", "noted", "R1-CX-01"))}, StopConverged, nil},
		{"P3 only with fix_p3: still converged", Input{FixP3: true, Round: 1, MaxRounds: 4,
			Convergence: conv(m("R1-M-01", "P3", "repo_fix", "noted"))}, StopConverged, nil},
		// Row 7
		{"last round: stop, no fix", Input{Codex: audit(cx1), Round: 4, MaxRounds: 4,
			Convergence: conv(m("R4-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))}, StopMaxRounds, []string{"R4-M-01"}},
		{"one round (twokey round): fix after the last round too", Input{Codex: audit(cx1), Round: 5, MaxRounds: 4, SingleRound: true,
			Convergence: conv(m("R5-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))}, Fix, []string{"R5-M-01"}},
		// Row 8
		{"fix confirmed blocking findings", Input{Codex: audit(cx1), Claude: audit(cc1), Round: 1, MaxRounds: 4,
			Convergence: conv(
				m("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"),
				m("R1-M-02", "P2", "repo_fix", "confirmed", "R1-CC-01"),
				m("R1-M-03", "P3", "repo_fix", "noted"),
				m("R1-M-04", "P2", "repo_fix", "confirmed"), // found by convergence itself
			)}, Fix, []string{"R1-M-01", "R1-M-02", "R1-M-04"}},
		{"fix_p3 adds P3 repo_fix items", Input{FixP3: true, Codex: audit(cx1), Round: 1, MaxRounds: 4,
			Convergence: conv(
				m("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"),
				m("R1-M-02", "P3", "repo_fix", "noted"),
				m("R1-M-03", "P3", "external_evidence", "noted"),
				m("R1-M-04", "P3", "repo_fix", "disputed"),
			)}, Fix, []string{"R1-M-01", "R1-M-02"}},
		// Order: dispute comes before decisions.
		{"dispute before decision", Input{Codex: audit(cx1, cx2), Round: 1, MaxRounds: 4,
			Convergence: conv(
				m("R1-M-01", "P1", "repo_fix", "disputed", "R1-CX-01"),
				m("R1-M-02", "P2", "needs_decision", "confirmed", "R1-CX-02"),
			)}, RunDispute, []string{"R1-M-01"}},
		{"decision before recurrence", Input{Codex: audit(cx1, cx2), Lineage: map[string]int{"R1-M-01": 2}, Round: 1, MaxRounds: 4,
			Convergence: conv(
				m("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"),
				m("R1-M-02", "P2", "needs_decision", "confirmed", "R1-CX-02"),
			)}, PauseDecision, []string{"R1-M-02"}},
		// Skipped by autopilot (D61): not fixed, not converged.
		{"skipped: the rest is fixed", Input{Codex: audit(cx1, cx2), Round: 1, MaxRounds: 4, Skipped: map[string]bool{"R1-M-02": true},
			Convergence: conv(
				m("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"),
				m("R1-M-02", "P2", "needs_decision", "confirmed", "R1-CX-02"),
			)}, Fix, []string{"R1-M-01"}},
		{"only skipped findings left: they wait for the owner", Input{Codex: audit(cx2), Round: 1, MaxRounds: 4, Skipped: map[string]bool{"R1-M-01": true},
			Convergence: conv(m("R1-M-01", "P2", "needs_decision", "confirmed", "R1-CX-02"))}, PauseSkipped, []string{"R1-M-01"}},
		{"a skipped recurring finding doesn't pause", Input{Codex: audit(cx1), Lineage: map[string]int{"R3-M-01": 2, "R3-M-02": 0}, Round: 3, MaxRounds: 4,
			Skipped: map[string]bool{"R3-M-01": true},
			Convergence: conv(
				m("R3-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"),
				m("R3-M-02", "P2", "repo_fix", "confirmed"),
			)}, Fix, []string{"R3-M-02"}},
		{"a decision is needed even in the last round", Input{Codex: audit(cx2), Round: 4, MaxRounds: 4,
			Convergence: conv(m("R4-M-01", "P2", "needs_decision", "confirmed", "R1-CX-02"))}, PauseDecision, []string{"R4-M-01"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.in)
			if got.Action != c.want || !reflect.DeepEqual(got.IDs, c.ids) {
				t.Errorf("got %s %v, want %s %v", got.Action, got.IDs, c.want, c.ids)
			}
		})
	}
}

func TestVerdictMismatchIsNoted(t *testing.T) {
	ready := audit(cx1)
	ready.Verdict = "ready_for_human_review"
	c := conv(m("R1-M-01", "P1", "repo_fix", "confirmed", "R1-CX-01"))
	c.Verdict = "ready_for_human_review"
	got := Decide(Input{Codex: ready, Convergence: c, Round: 1, MaxRounds: 4})
	if got.Action != Fix {
		t.Errorf("the findings must win over the verdict: got %s", got.Action)
	}
	notes := strings.Join(got.Notes, "\n")
	for _, want := range []string{
		"Codex's audit said ready_for_human_review but raised blocking findings.",
		"The convergence step said ready_for_human_review but confirmed blocking findings.",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q: %q", want, got.Notes)
		}
	}
	if clean := Decide(Input{Round: 1, MaxRounds: 4}); clean.Notes != nil {
		t.Errorf("no mismatch, but notes %q", clean.Notes)
	}
}

func TestLineage(t *testing.T) {
	recurs := map[string]string{"R3-M-01": "R2-M-04", "R2-M-04": "R1-M-02", "R1-M-02": "", "X": "Y", "Y": "X"}
	for id, want := range map[string]int{"R3-M-01": 2, "R2-M-04": 1, "R1-M-02": 0, "new": 0, "X": 1} {
		if got := Lineage(id, recurs); got != want {
			t.Errorf("Lineage(%s) = %d, want %d", id, got, want)
		}
	}
}
