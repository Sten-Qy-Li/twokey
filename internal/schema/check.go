package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Who wrote a finding ID: R{round}-{who}-{nn}.
const (
	CodexID       = "CX"
	ClaudeID      = "CC"
	ConvergenceID = "M"
)

// IDPrefix returns the ID prefix for a round, for example "R2-CX".
func IDPrefix(round int, who string) string { return fmt.Sprintf("R%d-%s", round, who) }

var (
	idRe  = regexp.MustCompile(`^R[0-9]+-(CX|CC|M)-[0-9]{2,}$`)
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// CheckAudit checks the facts behind a blind audit's answer: the commit,
// the IDs and the options.
func CheckAudit(a AuditResult, commit, prefix string) []string {
	var p []string
	if a.AuditedCommit != commit {
		p = append(p, fmt.Sprintf("audited_commit is %q, but the commit to audit is %s", a.AuditedCommit, commit))
	}
	seen := map[string]bool{}
	for i, f := range a.Findings {
		p = append(p, checkFinding(f, fmt.Sprintf("findings[%d]", i), prefix, seen)...)
	}
	return p
}

func checkFinding(f Finding, path, prefix string, seen map[string]bool) []string {
	var p []string
	add := func(format string, args ...any) { p = append(p, path+": "+fmt.Sprintf(format, args...)) }
	switch {
	case !idRe.MatchString(f.ID) || !strings.HasPrefix(f.ID, prefix+"-"):
		add("id %q must look like %s-01", f.ID, prefix)
	case seen[f.ID]:
		add("id %q is used twice", f.ID)
	}
	seen[f.ID] = true
	if strings.TrimSpace(f.Title) == "" {
		add("title is empty")
	}
	if f.Kind == "needs_decision" {
		if len(f.Options) < 2 || len(f.Options) > 4 {
			add("a needs_decision finding needs 2 to 4 options (has %d)", len(f.Options))
		}
		if f.RecommendedOption != "" && !slices.Contains(f.Options, f.RecommendedOption) {
			add("recommended_option %q is not one of the options", f.RecommendedOption)
		}
	} else if len(f.Options) > 0 || f.RecommendedOption != "" {
		add("options and recommended_option are only for needs_decision findings; use [] and \"\"")
	}
	return p
}

// CheckConvergence checks the convergence answer: the commit, the IDs,
// merged_from, dispute reasons, recurs_of, and the coverage rule (SPEC
// section 6): every blocking finding from either blind audit appears in
// merged_from of exactly one merged finding, which is confirmed (and still
// blocking) or disputed. priorIDs holds merged IDs from earlier rounds.
func CheckConvergence(c ConvergenceResult, commit, prefix string, codex, claude AuditResult, priorIDs map[string]bool) []string {
	var p []string
	if c.AuditedCommit != commit {
		p = append(p, fmt.Sprintf("audited_commit is %q, but the commit to check is %s", c.AuditedCommit, commit))
	}

	sources := map[string]Finding{}
	for _, f := range append(slices.Clone(codex.Findings), claude.Findings...) {
		sources[f.ID] = f
	}
	coveredBy := map[string][]string{}

	seen := map[string]bool{}
	for i, m := range c.Findings {
		path := fmt.Sprintf("findings[%d]", i)
		p = append(p, checkFinding(m.Finding, path, prefix, seen)...)
		for _, id := range m.MergedFrom {
			if _, ok := sources[id]; !ok {
				p = append(p, fmt.Sprintf("%s: merged_from names %q, which neither blind audit has", path, id))
				continue
			}
			coveredBy[id] = append(coveredBy[id], m.ID)
		}
		switch {
		case m.Status == "disputed" && strings.TrimSpace(m.DisputeReason) == "":
			p = append(p, fmt.Sprintf("%s: dispute_reason is required when status is disputed", path))
		case m.Status != "disputed" && m.DisputeReason != "":
			p = append(p, fmt.Sprintf("%s: dispute_reason must be \"\" unless status is disputed", path))
		}
		if m.RecursOf != "" && !priorIDs[m.RecursOf] {
			p = append(p, fmt.Sprintf("%s: recurs_of %q is not a merged finding from an earlier round of this run", path, m.RecursOf))
		}
	}

	merged := map[string]MergedFinding{}
	for _, m := range c.Findings {
		merged[m.ID] = m
	}
	for _, f := range append(slices.Clone(codex.Findings), claude.Findings...) {
		covers := coveredBy[f.ID]
		if len(covers) > 1 {
			p = append(p, fmt.Sprintf("%s appears in merged_from of %s; it may appear in only one", f.ID, strings.Join(covers, " and ")))
		}
		if !f.Blocking() {
			continue
		}
		if len(covers) == 0 {
			p = append(p, fmt.Sprintf("%s is a blocking finding, but no merged finding lists it in merged_from", f.ID))
			continue
		}
		m := merged[covers[0]]
		if m.Status != "disputed" && (m.Status != "confirmed" || !m.Blocking()) {
			p = append(p, fmt.Sprintf("%s is a blocking finding; %s, which covers it, must be confirmed (still P1/P2, repo_fix or needs_decision) or disputed", f.ID, m.ID))
		}
	}
	return p
}

// CheckDispute checks that Codex rechecked exactly the disputed findings.
func CheckDispute(d DisputeResult, disputed []string) []string {
	var p []string
	seen := map[string]bool{}
	for i, r := range d.Rechecks {
		switch {
		case !slices.Contains(disputed, r.ID):
			p = append(p, fmt.Sprintf("rechecks[%d]: %q was not one of the disputed findings (%s)", i, r.ID, strings.Join(disputed, ", ")))
		case seen[r.ID]:
			p = append(p, fmt.Sprintf("rechecks[%d]: %q is rechecked twice", i, r.ID))
		}
		seen[r.ID] = true
		if strings.TrimSpace(r.Evidence) == "" {
			p = append(p, fmt.Sprintf("rechecks[%d]: evidence is empty", i))
		}
	}
	for _, id := range disputed {
		if !seen[id] {
			p = append(p, fmt.Sprintf("there is no recheck for %s", id))
		}
	}
	return p
}

// CheckFix checks the fix answer's own facts: the base commit, the commit
// SHAs' format, and that every finding to fix appears once in done or
// not_done. twokey checks the commits themselves with git.
func CheckFix(f FixResult, base string, fixIDs []string) []string {
	var p []string
	if f.BaseCommit != base {
		p = append(p, fmt.Sprintf("base_commit is %q, but the fix had to start from %s", f.BaseCommit, base))
	}
	for i, c := range f.Commits {
		if !shaRe.MatchString(c) {
			p = append(p, fmt.Sprintf("commits[%d]: %q is not a full commit SHA (40 hex characters)", i, c))
		}
	}
	count := map[string]int{}
	for _, d := range f.Done {
		count[d.ID]++
	}
	for _, n := range f.NotDone {
		count[n.ID]++
	}
	for _, id := range fixIDs {
		switch count[id] {
		case 0:
			p = append(p, fmt.Sprintf("%s is in neither done nor not_done", id))
		case 1:
		default:
			p = append(p, fmt.Sprintf("%s appears %d times in done and not_done; it must appear once", id, count[id]))
		}
	}
	var unknown []string
	for id := range count {
		if !slices.Contains(fixIDs, id) {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	for _, id := range unknown {
		p = append(p, fmt.Sprintf("%s was not in the list to fix", id))
	}
	return p
}
