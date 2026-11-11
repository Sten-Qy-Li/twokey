package doctor

import (
	"os"
	"reflect"
	"slices"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOptionLine(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"  -s, --sandbox <SANDBOX_MODE>", []string{"-s", "--sandbox"}},
		{"      --json", []string{"--json"}},
		{"  -o, --output-last-message <FILE>", []string{"-o", "--output-last-message"}},
		{"  -p, --print                           Print response and exit", []string{"-p", "--print"}},
		{"  --allowedTools, --allowed-tools <tools...>", []string{"--allowedTools", "--allowed-tools"}},
		{"  -d, --debug [filter]                  Enable debug mode", []string{"-d", "--debug"}},
		{"          Examples: - `-c model=\"o3\"`", nil},
		{"                                        (choices: \"text\", \"json\")", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := optionLine(c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("optionLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// The installed CLIs have every option twokey uses (DECISIONS.md D3-D8).
func TestCheckedVersionsHaveTheFlags(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"codex-0.165.0-exec-help.txt", codexExecFlags},
		{"codex-0.165.0-exec-resume-help.txt", codexResumeFlags},
		{"claude-2.1.311-help.txt", append(slices.Clone(claudeFlags), "--max-budget-usd")},
	} {
		if miss := Missing(fixture(t, c.file), c.want); len(miss) > 0 {
			t.Errorf("%s lacks %v", c.file, miss)
		}
	}
}

// codex exec resume has no --sandbox, --cd or --add-dir (DECISIONS.md D5).
func TestCodexResumeLacksSandboxFlags(t *testing.T) {
	help := fixture(t, "codex-0.165.0-exec-resume-help.txt")
	got := Missing(help, []string{"--sandbox", "--cd", "--add-dir"})
	if !reflect.DeepEqual(got, []string{"--sandbox", "--cd", "--add-dir"}) {
		t.Errorf("Missing = %v", got)
	}
}

func TestChoices(t *testing.T) {
	codex := fixture(t, "codex-0.165.0-exec-help.txt")
	if got := Choices(codex, "--sandbox"); !reflect.DeepEqual(got, []string{"read-only", "workspace-write", "danger-full-access"}) {
		t.Errorf("codex --sandbox choices = %q", got)
	}
	claude := fixture(t, "claude-2.1.311-help.txt")
	modes := Choices(claude, "--permission-mode")
	for _, m := range []string{"acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan"} {
		if !slices.Contains(modes, m) {
			t.Errorf("claude --permission-mode choices %q lack %q", modes, m)
		}
	}
	if got := Choices(claude, "--permission-prompts"); !slices.Contains(got, "none") {
		t.Errorf("claude --permission-prompts choices = %q", got)
	}
	if got := Choices(claude, "--no-such-flag"); got != nil {
		t.Errorf("unknown flag: got %q", got)
	}
	if got := Choices(claude, "--model"); got != nil {
		t.Errorf("--model has no list: got %q", got)
	}
}
