package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
)

// Render renders a step's prompt without running it, for tuning templates
// ("twokey render"). It uses the latest run's state if there is one, else
// a preview of round 1 on the current HEAD. round 0 means the latest
// round; agent "" picks the step's usual agent (Codex for audits).
func Render(ctx context.Context, o Options, step, agent string, round int) (string, error) {
	if !slices.Contains([]string{schema.Audit, schema.Convergence, schema.Dispute, schema.Fix}, step) {
		return "", fmt.Errorf("unknown step %q: use audit, convergence, dispute or fix", step)
	}
	e := newEngine(o)
	if dir, err := FindRun(e.runRoot, ""); err == nil {
		if e.st, err = state.Load(dir); err != nil {
			return "", err
		}
	} else {
		head, err := e.repo.RevParse(ctx, "HEAD")
		if err != nil {
			return "", err
		}
		e.st = &state.State{RunID: "preview", MaxRounds: e.cfg.Loop.MaxRounds}
		e.st.NewRound(head, head)
	}
	e.setRun(e.st.RunID)

	r := e.st.Current()
	if round > 0 {
		if round > len(e.st.Rounds) {
			return "", fmt.Errorf("the run has %d rounds", len(e.st.Rounds))
		}
		r = e.st.Rounds[round-1]
	}

	name := step
	switch {
	case step == schema.Audit && agent == "claude":
		name = state.ClaudeAudit
	case step == schema.Audit:
		agent, name = "codex", state.CodexAudit
	case agent == "" && step == schema.Dispute:
		agent = "codex"
	case agent == "":
		agent = "claude"
	}
	s := &state.Step{Name: name, Agent: agent}
	vars := e.vars(r, s, step)
	switch step {
	case schema.Fix:
		if r.IsDone(state.Convergence) && r.Gate != nil {
			for k, v := range e.fixVars(r, r.Gate.IDs) {
				vars[k] = v
			}
		} else {
			vars["fix_list"] = "(twokey render: the confirmed findings to fix go here)"
			vars["p3_rule"] = "(twokey render: the rule for P3 findings goes here)"
		}
	case schema.Dispute:
		vars["disputes"] = "(twokey render: the disputed findings and the dispute reasons go here)"
	}
	return e.tmpl.Prompt(step, vars)
}
