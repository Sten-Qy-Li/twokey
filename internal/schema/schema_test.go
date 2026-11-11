package schema

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
)

var steps = []string{Audit, Convergence, Dispute, Fix}

// Codex's --output-schema needs strict schemas (SPEC section 6), and the
// schemas use only the keywords in DECISIONS.md D18.
func TestSchemasAreStrict(t *testing.T) {
	allowed := []string{"type", "properties", "required", "additionalProperties", "enum", "items"}
	for _, step := range steps {
		raw, err := Raw(step)
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		var walk func(path string, n map[string]any)
		walk = func(path string, n map[string]any) {
			for k := range n {
				if !slices.Contains(allowed, k) {
					t.Errorf("%s %s: keyword %q is not in the allowed set", step, path, k)
				}
			}
			if n["type"] == "object" {
				if n["additionalProperties"] != false {
					t.Errorf("%s %s: additionalProperties must be false", step, path)
				}
				props, _ := n["properties"].(map[string]any)
				var req []string
				for _, r := range n["required"].([]any) {
					req = append(req, r.(string))
				}
				var names []string
				for k := range props {
					names = append(names, k)
				}
				sort.Strings(req)
				sort.Strings(names)
				if !slices.Equal(req, names) {
					t.Errorf("%s %s: required %v must list every property %v", step, path, req, names)
				}
				for k, sub := range props {
					walk(path+"."+k, sub.(map[string]any))
				}
			}
			if items, ok := n["items"].(map[string]any); ok {
				walk(path+"[]", items)
			}
		}
		walk("$", s)
	}
}

func TestMinified(t *testing.T) {
	m, err := Minified(Audit)
	if err != nil || strings.ContainsAny(m, " \n") || !strings.HasPrefix(m, `{"type":"object"`) {
		t.Errorf("Minified = %.60q..., %v", m, err)
	}
}

const sha = "0123456789abcdef0123456789abcdef01234567"

func auditJSON(findings string) string {
	return `{"audited_commit":"` + sha + `","verdict":"not_ready","summary":"s","findings":[` + findings + `],"log_entry_markdown":"entry"}`
}

const finding = `{"id":"R1-CX-01","title":"t","severity":"P1","kind":"repo_fix","evidence":"e","fix_hint":"h","options":[],"recommended_option":""}`

func TestDecode(t *testing.T) {
	var a AuditResult
	if probs := Decode(Audit, []byte(auditJSON(finding)), &a); probs != nil {
		t.Fatalf("valid answer refused: %v", probs)
	}
	if a.AuditedCommit != sha || len(a.Findings) != 1 || !a.Findings[0].Blocking() || a.LogEntry != "entry" {
		t.Errorf("decoded %+v", a)
	}

	cases := []struct{ name, data, want string }{
		{"not JSON", "{", "the answer is not valid JSON"},
		{"text after", auditJSON("") + " and more", "the answer has more text after the JSON object"},
		{"not an object", "[]", "answer: must be an object, not a list"},
		{"missing", `{"verdict":"not_ready","summary":"s","findings":[],"log_entry_markdown":""}`, "answer: audited_commit is missing"},
		{"extra", strings.Replace(auditJSON(""), `"summary"`, `"extra":1,"summary"`, 1), "answer: extra is not allowed"},
		{"enum", strings.Replace(auditJSON(""), "not_ready", "ready", 1), `answer.verdict: must be one of not_ready, ready_for_human_review (got "ready")`},
		{"nested enum", auditJSON(strings.Replace(finding, `"P1"`, `"P0"`, 1)), `answer.findings[0].severity: must be one of P1, P2, P3 (got "P0")`},
		{"nested type", auditJSON(strings.Replace(finding, `"options":[]`, `"options":"none"`, 1)), `answer.findings[0].options: must be a list, not "none"`},
		{"null", auditJSON(strings.Replace(finding, `"title":"t"`, `"title":null`, 1)), "answer.findings[0].title: must be text, not null"},
	}
	for _, c := range cases {
		var a AuditResult
		probs := Decode(Audit, []byte(c.data), &a)
		if !slices.ContainsFunc(probs, func(p string) bool { return strings.Contains(p, c.want) }) {
			t.Errorf("%s: got %q, want %q", c.name, probs, c.want)
		}
	}
}

func TestValidateExtraKeywords(t *testing.T) {
	s := map[string]any{"type": "array", "minItems": 2.0, "maxItems": 3.0, "items": map[string]any{"type": "string", "pattern": "^R[0-9]+$"}}
	if p := Validate(s, []byte(`["R1","R2"]`)); p != nil {
		t.Errorf("valid: %v", p)
	}
	if p := Validate(s, []byte(`["X"]`)); len(p) != 2 {
		t.Errorf("want too few items and a pattern miss, got %v", p)
	}
	if p := Validate(map[string]any{"type": "integer"}, []byte(`1.5`)); len(p) != 1 {
		t.Errorf("1.5 is not an integer: %v", p)
	}
}

func f(id, sev, kind string) Finding {
	return Finding{ID: id, Title: "t", Severity: sev, Kind: kind, Options: []string{}}
}

func decision(id string, opts ...string) Finding {
	x := f(id, "P2", "needs_decision")
	x.Options = opts
	return x
}

func TestCheckAudit(t *testing.T) {
	ok := AuditResult{AuditedCommit: sha, Findings: []Finding{f("R1-CX-01", "P1", "repo_fix"), decision("R1-CX-02", "a", "b")}}
	if p := CheckAudit(ok, sha, "R1-CX"); p != nil {
		t.Errorf("valid audit: %v", p)
	}
	withRec := decision("R1-CX-01", "a", "b")
	withRec.RecommendedOption = "c"
	withOpts := f("R1-CX-01", "P1", "repo_fix")
	withOpts.Options = []string{"a", "b"}
	cases := []struct {
		name string
		a    AuditResult
		want string
	}{
		{"wrong commit", AuditResult{AuditedCommit: "abc"}, `audited_commit is "abc", but the commit to audit is ` + sha},
		{"wrong prefix", AuditResult{AuditedCommit: sha, Findings: []Finding{f("R1-CC-01", "P1", "repo_fix")}}, `findings[0]: id "R1-CC-01" must look like R1-CX-01`},
		{"wrong round", AuditResult{AuditedCommit: sha, Findings: []Finding{f("R2-CX-01", "P1", "repo_fix")}}, `id "R2-CX-01" must look like R1-CX-01`},
		{"one digit", AuditResult{AuditedCommit: sha, Findings: []Finding{f("R1-CX-1", "P1", "repo_fix")}}, `id "R1-CX-1" must look like`},
		{"duplicate", AuditResult{AuditedCommit: sha, Findings: []Finding{f("R1-CX-01", "P1", "repo_fix"), f("R1-CX-01", "P2", "repo_fix")}}, `findings[1]: id "R1-CX-01" is used twice`},
		{"one option", AuditResult{AuditedCommit: sha, Findings: []Finding{decision("R1-CX-01", "a")}}, "needs 2 to 4 options (has 1)"},
		{"five options", AuditResult{AuditedCommit: sha, Findings: []Finding{decision("R1-CX-01", "a", "b", "c", "d", "e")}}, "needs 2 to 4 options (has 5)"},
		{"bad recommendation", AuditResult{AuditedCommit: sha, Findings: []Finding{withRec}}, `recommended_option "c" is not one of the options`},
		{"options on repo_fix", AuditResult{AuditedCommit: sha, Findings: []Finding{withOpts}}, "options and recommended_option are only for needs_decision findings"},
	}
	for _, c := range cases {
		p := CheckAudit(c.a, sha, "R1-CX")
		if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("%s: got %q, want %q", c.name, p, c.want)
		}
	}
}

func merged(id, sev, kind, status string, from ...string) MergedFinding {
	m := MergedFinding{Finding: f(id, sev, kind), Sources: []string{"codex"}, MergedFrom: from, Status: status}
	if status == "disputed" {
		m.DisputeReason = "because"
	}
	return m
}

func TestCheckConvergence(t *testing.T) {
	codex := AuditResult{AuditedCommit: sha, Findings: []Finding{f("R2-CX-01", "P1", "repo_fix"), f("R2-CX-02", "P3", "repo_fix")}}
	claude := AuditResult{AuditedCommit: sha, Findings: []Finding{f("R2-CC-01", "P2", "repo_fix")}}
	prior := map[string]bool{"R1-M-01": true}
	conv := func(ms ...MergedFinding) ConvergenceResult {
		return ConvergenceResult{AuditedCommit: sha, Findings: ms}
	}

	good := conv(
		merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01"),
		merged("R2-M-02", "P3", "repo_fix", "noted", "R2-CX-02"),
	)
	good.Findings[0].RecursOf = "R1-M-01"
	if p := CheckConvergence(good, sha, "R2-M", codex, claude, prior); p != nil {
		t.Errorf("valid convergence: %v", p)
	}
	disputed := conv(merged("R2-M-01", "P1", "repo_fix", "disputed", "R2-CX-01"), merged("R2-M-02", "P2", "repo_fix", "disputed", "R2-CC-01"))
	if p := CheckConvergence(disputed, sha, "R2-M", codex, claude, prior); p != nil {
		t.Errorf("disputing blocking findings is allowed: %v", p)
	}

	noReason := merged("R2-M-01", "P1", "repo_fix", "disputed", "R2-CX-01", "R2-CC-01")
	noReason.DisputeReason = ""
	strayReason := merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01")
	strayReason.DisputeReason = "x"
	badRecur := merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01")
	badRecur.RecursOf = "R2-M-05"

	cases := []struct {
		name string
		c    ConvergenceResult
		want string
	}{
		{"uncovered", conv(merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01")), "R2-CC-01 is a blocking finding, but no merged finding lists it in merged_from"},
		{"covered by noted", conv(merged("R2-M-01", "P1", "repo_fix", "noted", "R2-CX-01", "R2-CC-01")), "R2-CX-01 is a blocking finding; R2-M-01, which covers it, must be confirmed"},
		{"lowered to P3", conv(merged("R2-M-01", "P3", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01")), "R2-CX-01 is a blocking finding; R2-M-01, which covers it, must be confirmed"},
		{"made external", conv(merged("R2-M-01", "P1", "external_evidence", "confirmed", "R2-CX-01", "R2-CC-01")), "R2-CC-01 is a blocking finding; R2-M-01"},
		{"covered twice", conv(merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01"), merged("R2-M-02", "P1", "repo_fix", "confirmed", "R2-CX-01")), "R2-CX-01 appears in merged_from of R2-M-01 and R2-M-02"},
		{"unknown source", conv(merged("R2-M-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01", "R2-CX-09")), `merged_from names "R2-CX-09", which neither blind audit has`},
		{"no reason", conv(noReason), "dispute_reason is required when status is disputed"},
		{"stray reason", conv(strayReason), `dispute_reason must be "" unless status is disputed`},
		{"bad recurs_of", conv(badRecur), `recurs_of "R2-M-05" is not a merged finding from an earlier round`},
		{"wrong prefix", conv(merged("R2-CX-01", "P1", "repo_fix", "confirmed", "R2-CX-01", "R2-CC-01")), `id "R2-CX-01" must look like R2-M-01`},
		{"wrong commit", ConvergenceResult{AuditedCommit: "x"}, `audited_commit is "x"`},
	}
	for _, c := range cases {
		p := CheckConvergence(c.c, sha, "R2-M", codex, claude, prior)
		if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("%s: got %q, want %q", c.name, p, c.want)
		}
	}
}

func TestFromCodex(t *testing.T) {
	if !merged("R1-M-01", "P1", "repo_fix", "disputed", "R1-CC-02", "R1-CX-01").FromCodex() {
		t.Error("a finding both audits raised came from Codex too (D19)")
	}
	if merged("R1-M-01", "P1", "repo_fix", "disputed", "R1-CC-02").FromCodex() {
		t.Error("a Claude-only finding is not from Codex")
	}
}

func TestCheckDispute(t *testing.T) {
	disputed := []string{"R1-M-01", "R1-M-03"}
	ok := DisputeResult{Rechecks: []Recheck{{"R1-M-03", "withdraw", "e"}, {"R1-M-01", "maintain", "e"}}}
	if p := CheckDispute(ok, disputed); p != nil {
		t.Errorf("valid: %v", p)
	}
	bad := DisputeResult{Rechecks: []Recheck{{"R1-M-01", "maintain", ""}, {"R1-M-01", "withdraw", "e"}, {"R1-M-02", "withdraw", "e"}}}
	got := strings.Join(CheckDispute(bad, disputed), "\n")
	for _, want := range []string{
		"rechecks[0]: evidence is empty",
		`rechecks[1]: "R1-M-01" is rechecked twice`,
		`rechecks[2]: "R1-M-02" was not one of the disputed findings (R1-M-01, R1-M-03)`,
		"there is no recheck for R1-M-03",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

func TestCheckFix(t *testing.T) {
	ids := []string{"R1-M-01", "R1-M-02"}
	ok := FixResult{BaseCommit: sha, Commits: []string{sha}, Done: []Done{{ID: "R1-M-01"}}, NotDone: []NotDone{{ID: "R1-M-02"}}}
	if p := CheckFix(ok, sha, ids); p != nil {
		t.Errorf("valid: %v", p)
	}
	bad := FixResult{BaseCommit: "abc", Commits: []string{"abc123"}, Done: []Done{{ID: "R1-M-01"}, {ID: "R1-M-09"}}, NotDone: []NotDone{{ID: "R1-M-01"}}}
	got := strings.Join(CheckFix(bad, sha, ids), "\n")
	for _, want := range []string{
		`base_commit is "abc", but the fix had to start from ` + sha,
		`commits[0]: "abc123" is not a full commit SHA`,
		"R1-M-01 appears 2 times in done and not_done",
		"R1-M-02 is in neither done nor not_done",
		"R1-M-09 was not in the list to fix",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}
