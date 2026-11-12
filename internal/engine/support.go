package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/gate"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

// vars returns the placeholders for a step's prompt (SPEC section 14).
func (e *engine) vars(r *state.Round, s *state.Step, step string) map[string]string {
	cfg := e.cfg
	who, ports, short := schema.CodexID, cfg.Codex.Ports, "cx"
	if s.Agent == "claude" {
		who, ports, short = schema.ClaudeID, cfg.Claude.Ports, "cc"
	}
	prefix := schema.IDPrefix(r.N, schema.ConvergenceID)
	if step == schema.Audit {
		prefix = schema.IDPrefix(r.N, who)
	}
	req := cfg.Checkpoint.RequirementsFile
	if !filepath.IsAbs(req) {
		req = filepath.Join(e.o.Root, req)
	}
	extra := "None."
	if len(cfg.Project.ExtraReadDirs) > 0 {
		extra = "\n- " + strings.Join(cfg.Project.ExtraReadDirs, "\n- ")
	}
	verify := "None (the project has no checks configured)."
	if len(cfg.Verify.Commands) > 0 {
		verify = "`" + strings.Join(cfg.Verify.Commands, "`, `") + "`"
	}
	result := func(name string) string {
		if st := r.Step(name); st != nil {
			return st.Result
		}
		return ""
	}
	var prior []string
	for _, pr := range e.st.Rounds {
		if pr.N < r.N && pr.IsDone(state.Convergence) {
			prior = append(prior, pr.Step(state.Convergence).Result)
		}
	}
	priorText := "None (this is the first round)."
	if len(prior) > 0 {
		priorText = strings.Join(prior, ", ")
	}
	protected := strings.Join(cfg.Project.ProtectedBranches, ", ")
	if protected == "" {
		protected = "(none)"
	}
	return map[string]string{
		"run_id":             e.st.RunID,
		"round":              strconv.Itoa(r.N),
		"max_rounds":         strconv.Itoa(e.st.MaxRounds),
		"agent_name":         agentNames[s.Agent],
		"other_agent":        otherAgent(s.Agent),
		"repo_path":          e.o.Root,
		"branch":             cfg.Project.Branch,
		"protected_branches": protected,
		"checkpoint_label":   cfg.Checkpoint.Label,
		"requirements_path":  req,
		"extra_read_dirs":    extra,
		"log_file":           e.logPath,
		"commit":             r.Commit,
		"scratch_dir":        filepath.Join(e.o.ScratchRoot, e.st.RunID, fmt.Sprintf("r%d", r.N), s.Name),
		"container_prefix":   containerPrefix(e.st.RunID, r.N, step, short),
		"port_range":         ports,
		"history":            e.history(s),
		"decisions":          e.decisionsText(),
		"id_prefix":          prefix,
		"codex_audit_path":   result(state.CodexAudit),
		"claude_audit_path":  result(state.ClaudeAudit),
		"convergence_path":   result(state.Convergence),
		"prior_merged_paths": priorText,
		"fix_list":           "",
		"p3_rule":            "",
		"verify_commands":    verify,
		"disputes":           "",
	}
}

// containerPrefix is the step's Docker name prefix (DECISIONS.md D12):
// tk-<run>-r<round>-<step>-<agent>-, lowercase, where <run> is the run's
// time stamp.
func containerPrefix(runID string, round int, step, agent string) string {
	stamp, _, _ := strings.Cut(runID, "-")
	return fmt.Sprintf("tk-%s-r%d-%s-%s-", strings.ToLower(stamp), round, step, agent)
}

// fixVars lists the findings to fix, with the owner's decisions.
func (e *engine) fixVars(r *state.Round, ids []string) map[string]string {
	conv := loadJSON[schema.ConvergenceResult](r.Step(state.Convergence).Result)
	var b strings.Builder
	for _, id := range ids {
		for _, m := range conv.Findings {
			if m.ID != id {
				continue
			}
			fmt.Fprintf(&b, "- %s [%s %s] %s\n", m.ID, m.Severity, m.Kind, m.Title)
			if m.Evidence != "" {
				fmt.Fprintf(&b, "  Evidence: %s\n", m.Evidence)
			}
			if m.FixHint != "" {
				fmt.Fprintf(&b, "  Fix hint: %s\n", m.FixHint)
			}
			if d := e.st.Decisions[id]; d != "" {
				fmt.Fprintf(&b, "  The owner's decision: %s\n", d)
			}
			if e.st.Rulings[id] == gate.RuleFix {
				fmt.Fprintf(&b, "  The owner ruled: fix it.\n")
			}
		}
	}
	p3 := "Don't fix P3 findings; they go into the final report."
	if e.cfg.Loop.FixP3 {
		p3 = "P3 repo_fix findings are in the list because fix_p3 is on; fix them too."
	}
	return map[string]string{"fix_list": strings.TrimRight(b.String(), "\n"), "p3_rule": p3}
}

// history lists the run's finished steps: log heading and published answer.
func (e *engine) history(current *state.Step) string {
	var lines []string
	for _, r := range e.st.Rounds {
		for _, s := range r.Steps {
			if s == current || s.Status != state.Done || s.Result == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- Round %d, %s: log entry \"%s\", answer %s", r.N, s.Name, s.Heading, s.Result))
		}
	}
	if len(lines) == 0 {
		return "Nothing yet: this is the first step of the run."
	}
	return strings.Join(lines, "\n")
}

func (e *engine) decisionsText() string {
	data, err := os.ReadFile(filepath.Join(e.runDir, "decisions.md"))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return "None yet."
	}
	return strings.TrimSpace(string(data))
}

// label is the log heading label for an agent step (DECISIONS.md D14).
func (e *engine) label(agent, model string) string {
	return Label(e.cfg, agent, model)
}

// Label returns "<label_prefix> - <model name>" when the CLI reported the
// model, else the configured label.
func Label(cfg *config.Config, agent, model string) string {
	prefix, label := cfg.Codex.LabelPrefix, cfg.Codex.Label
	if agent == "claude" {
		prefix, label = cfg.Claude.LabelPrefix, cfg.Claude.Label
	}
	if model == "" {
		return label
	}
	if name := cfg.ModelNames[model]; name != "" {
		model = name
	}
	return prefix + " - " + model
}

func (e *engine) twokeyLabel() string { return "twokey " + version.String() }

// decide runs the gate on the round's published answers.
func (e *engine) decide(r *state.Round) gate.Result {
	in := gate.Input{
		Round: r.N, MaxRounds: e.st.MaxRounds, FixP3: e.cfg.Loop.FixP3,
		Codex:       loadJSON[schema.AuditResult](r.Step(state.CodexAudit).Result),
		Claude:      loadJSON[schema.AuditResult](r.Step(state.ClaudeAudit).Result),
		Convergence: loadJSON[schema.ConvergenceResult](r.Step(state.Convergence).Result),
		Rulings:     e.st.Rulings, Decisions: e.st.Decisions, Lineage: e.lineage(),
	}
	if r.IsDone(state.Dispute) {
		d := loadJSON[schema.DisputeResult](r.Step(state.Dispute).Result)
		in.Dispute = &d
	}
	return gate.Decide(in)
}

// lineage computes every merged finding's recurrence count (D21).
func (e *engine) lineage() map[string]int {
	recursOf := map[string]string{}
	for _, r := range e.st.Rounds {
		if r.IsDone(state.Convergence) {
			for _, m := range loadJSON[schema.ConvergenceResult](r.Step(state.Convergence).Result).Findings {
				recursOf[m.ID] = m.RecursOf
			}
		}
	}
	out := map[string]int{}
	for id := range recursOf {
		out[id] = gate.Lineage(id, recursOf)
	}
	return out
}

// priorMergedIDs are the merged IDs of rounds before r, for recurs_of.
func (e *engine) priorMergedIDs(r *state.Round) map[string]bool {
	ids := map[string]bool{}
	for _, pr := range e.st.Rounds {
		if pr.N < r.N && pr.IsDone(state.Convergence) {
			for _, m := range loadJSON[schema.ConvergenceResult](pr.Step(state.Convergence).Result).Findings {
				ids[m.ID] = true
			}
		}
	}
	return ids
}

func auditTable(fs []schema.Finding) string {
	if len(fs) == 0 {
		return "  (no findings)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s %-4s %-18s %s\n", "ID", "Sev", "Kind", "Title")
	for _, f := range fs {
		fmt.Fprintf(&b, "  %-10s %-4s %-18s %s\n", f.ID, f.Severity, f.Kind, cut(f.Title, 70))
	}
	return strings.TrimRight(b.String(), "\n")
}

func mergedTable(ms []schema.MergedFinding) string {
	if len(ms) == 0 {
		return "  (no findings)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %-9s %-4s %-18s %-10s %s\n", "ID", "Sev", "Kind", "Status", "Title")
	for _, m := range ms {
		fmt.Fprintf(&b, "  %-9s %-4s %-18s %-10s %s\n", m.ID, m.Severity, m.Kind, m.Status, cut(m.Title, 60))
	}
	return strings.TrimRight(b.String(), "\n")
}

func cut(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-3]) + "..."
}

func counts(fs []schema.Finding) string {
	n := map[string]int{}
	for _, f := range fs {
		n[f.Severity]++
	}
	return fmt.Sprintf("%d/%d/%d", n["P1"], n["P2"], n["P3"])
}

// report builds report.md (SPEC section 13). M1 has the outcome, the
// rounds, the pushed commits, external evidence, open P3 items,
// decisions, notes and the log headings; M2 and M3 add the rest.
func (e *engine) report() string {
	var b strings.Builder
	cur := e.st.Current()
	fmt.Fprintf(&b, "# twokey run %s\n\n", e.st.RunID)
	switch e.st.Status {
	case state.Converged:
		fmt.Fprintf(&b, "**Outcome:** converged in round %d. Both audits cleared commit `%s`.\n\n", cur.N, short(cur.Commit))
	case state.MaxRounds:
		fmt.Fprintf(&b, "**Outcome:** stopped after round %d, the last one, with confirmed findings left: %s.\n\n", cur.N, strings.Join(cur.Gate.IDs, ", "))
	default:
		fmt.Fprintf(&b, "**Outcome:** %s.\n\n", e.st.Status)
	}

	b.WriteString("## Rounds\n\n| Round | Commit | Codex P1/P2/P3 | Claude P1/P2/P3 | Confirmed blocking | Action |\n|---|---|---|---|---|---|\n")
	var external, p3, notes, headings []string
	for _, r := range e.st.Rounds {
		codex := loadJSON[schema.AuditResult](resultOf(r, state.CodexAudit))
		claude := loadJSON[schema.AuditResult](resultOf(r, state.ClaudeAudit))
		action, blocking := "-", "-"
		if r.Gate != nil {
			action = r.Gate.Action
			if r.Gate.Action == string(gate.Fix) || r.Gate.Action == string(gate.StopMaxRounds) {
				blocking = strings.Join(r.Gate.IDs, ", ")
			}
			notes = append(notes, r.Gate.Notes...)
		}
		fmt.Fprintf(&b, "| %d | `%s` | %s | %s | %s | %s |\n", r.N, short(r.Commit), counts(codex.Findings), counts(claude.Findings), blocking, action)
		for _, m := range loadJSON[schema.ConvergenceResult](resultOf(r, state.Convergence)).Findings {
			if m.Kind == "external_evidence" {
				external = append(external, fmt.Sprintf("- %s [%s] %s", m.ID, m.Severity, m.Title))
			} else if m.Severity == "P3" && r == cur {
				p3 = append(p3, fmt.Sprintf("- %s %s", m.ID, m.Title))
			}
		}
		for _, s := range r.Steps {
			if s.Heading != "" {
				headings = append(headings, fmt.Sprintf("- %s (round %d, %s)", s.Heading, r.N, s.Name))
			}
		}
	}

	b.WriteString("\n## Commits pushed\n\n")
	pushed := false
	for _, r := range e.st.Rounds {
		if data, err := os.ReadFile(filepath.Join(e.runDir, fmt.Sprintf("r%d", r.N), "push.log")); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n")[1:] {
				fmt.Fprintf(&b, "- `%s`\n", line)
				pushed = true
			}
		}
	}
	if !pushed {
		b.WriteString("None.\n")
	}
	section := func(title string, lines []string) {
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		if len(lines) == 0 {
			b.WriteString("None.\n")
			return
		}
		b.WriteString(strings.Join(slices.Compact(lines), "\n") + "\n")
	}
	section("For the owner and the team (external evidence)", external)
	section("P3 items not fixed", p3)
	fmt.Fprintf(&b, "\n## Decisions and rulings\n\n%s\n", e.decisionsText())
	section("Notes", append(notes, e.st.Notes...))
	section("Log entries to read for detail", headings)
	return b.String()
}

func resultOf(r *state.Round, name string) string {
	if s := r.Step(name); s != nil {
		return s.Result
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
