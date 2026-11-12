package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allVars has a value for every placeholder in SPEC section 14.
func allVars() map[string]string {
	v := map[string]string{}
	for _, k := range []string{
		"run_id", "round", "max_rounds", "agent_name", "other_agent", "repo_path", "branch", "protected_branches",
		"checkpoint_label", "requirements_path", "extra_read_dirs", "log_file", "commit", "upstream_status", "sandbox_limits", "scratch_dir",
		"container_prefix", "port_range", "history", "decisions", "id_prefix",
		"codex_audit_path", "claude_audit_path", "convergence_path", "prior_merged_paths", "fix_list", "p3_rule",
		"verify_commands", "disputes",
	} {
		v[k] = "<" + k + ">"
	}
	return v
}

// Every default template renders with the SPEC's placeholders, and uses
// no others.
func TestDefaultTemplatesRender(t *testing.T) {
	tm := Templates{}
	for _, step := range []string{"audit", "convergence", "dispute", "fix"} {
		out, err := tm.Prompt(step, allVars())
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if strings.Contains(out, "${") {
			t.Errorf("%s: unexpanded placeholder left", step)
		}
		if !strings.HasPrefix(out, "# Standing rules (twokey run <run_id>, round <round> of at most <max_rounds>)") {
			t.Errorf("%s: doesn't start with the preamble", step)
		}
	}
	fix, _ := tm.Prompt("fix", allVars())
	if !strings.Contains(fix, "2. You may edit files and commit to <branch>. Do not push.") {
		t.Error("fix step: wrong write rule, or ${branch} inside it not expanded")
	}
	audit, _ := tm.Prompt("audit", allVars())
	if !strings.Contains(audit, "2. This step must not change the repository") {
		t.Error("audit step: wrong write rule")
	}
}

func TestOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "audit.md"), []byte("Custom audit of ${commit} with $HOME\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm := Templates{OverrideDir: dir}
	out, err := tm.Prompt("audit", allVars())
	if err != nil || !strings.HasSuffix(out, "\n\nCustom audit of <commit> with $HOME\n") {
		t.Errorf("override not used: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(dir, "dispute.md"), []byte("${typo}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = tm.Prompt("dispute", allVars())
	if err == nil || !strings.Contains(err.Error(), "dispute.md: the template uses placeholders that have no value: ${typo}") {
		t.Errorf("a missing placeholder must name the file: %v", err)
	}
}

func TestCopyDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err := CopyDefaults(dir)
	if err != nil || len(wrote) != len(TemplateNames)-1 {
		t.Fatalf("wrote %v, %v", wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "audit.md")); string(b) != "mine" {
		t.Error("an existing template was overwritten")
	}
}
