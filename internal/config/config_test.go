package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey"
)

// The shipped template must load, and every key it sets (other than the
// required ones, which have no default) must equal the default.
func TestTemplateMatchesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(twokey.ConfigTemplate))
	if err != nil {
		t.Fatalf("template doesn't load: %v", err)
	}
	want := Default()
	want.Project.Branch = "my-branch"
	want.Checkpoint = Checkpoint{Label: "Checkpoint 3", RequirementsFile: "local-only_checkpoint-3-instructions.md"}
	want.Verify.Commands = []string{"npm run lint", "npm run format:check", "npm run typecheck", "npm test", "npm run build"}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("template and defaults differ:\n got %+v\nwant %+v", cfg, want)
	}
}

// minimal is the smallest valid file: only the required keys.
const minimal = `
[project]
branch = "feature"
[checkpoint]
label = "Milestone 1"
requirements_file = "requirements.md"
[verify]
commands = []
`

func TestMinimalUsesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.MaxRounds != 4 || cfg.Codex.Sandbox != "workspace-write" || cfg.Claude.PermissionMode != "auto" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.ModelNames["claude-opus-5-5"] != "Claude Opus 5.5" {
		t.Errorf("default model names missing: %v", cfg.ModelNames)
	}
}

func TestModelNamesAreAddedToDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal + "[model_names]\n\"gpt-7\" = \"GPT-7\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelNames["gpt-7"] != "GPT-7" || cfg.ModelNames["gpt-6.1-sol"] != "GPT-6.1-Sol" {
		t.Errorf("model names = %v", cfg.ModelNames)
	}
}

// Each case changes the minimal file and expects one message.
func TestProblems(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "slides")
	cases := []struct {
		name  string
		extra string // appended to minimal; later keys can't repeat earlier ones
		file  string // replaces minimal when set
		want  string
	}{
		{name: "syntax error", file: "[project\nbranch = 1", want: "The file is not valid TOML. Line 2: expected '.' or ']' to end table name"},
		{name: "wrong type", extra: "[loop]\nmax_rounds = \"four\"\n", want: "[loop] max_rounds must be a whole number, not text in quotes."},
		{name: "wrong type list", extra: "[codex]\nextra_args = \"--x\"\n", want: "[codex] extra_args must be a list in [ ], not text in quotes."},
		{name: "unknown key", extra: "[codex]\nsandbx = \"read-only\"\n", want: "[codex] sandbx is not a known setting. Check the spelling."},
		{name: "unknown section", extra: "[codx]\ncommand = \"codex\"\n", want: "[codx] is not a known section. Check the spelling."},
		{name: "top-level key", file: "branch = \"x\"\n" + minimal, want: `"branch" is not a known setting. Settings go inside a section such as [project].`},
		{name: "missing branch", file: "[checkpoint]\nlabel = \"x\"\nrequirements_file = \"r.md\"\n[verify]\ncommands = []\n", want: "[project] branch is missing."},
		{name: "missing verify", file: "[project]\nbranch = \"b\"\n[checkpoint]\nlabel = \"x\"\nrequirements_file = \"r.md\"\n", want: "[verify] commands is missing."},
		{name: "bad branch", file: strings.Replace(minimal, `"feature"`, `"my branch"`, 1), want: `[project] branch is not a valid branch name (got "my branch")`},
		{name: "branch is protected", file: strings.Replace(minimal, `"feature"`, `"main"`, 1), want: `[project] branch "main" is also in protected_branches.`},
		{name: "log file outside repo", file: strings.Replace(minimal, "[project]\n", "[project]\nlog_file = \"../logs.md\"\n", 1), want: "[project] log_file must be a path inside the repository"},
		{name: "run dir absolute", file: strings.Replace(minimal, "[project]\n", "[project]\nrun_dir = "+quote(abs)+"\n", 1), want: "[project] run_dir must be a folder inside the repository"},
		{name: "marker two lines", file: strings.Replace(minimal, "[project]\n", "[project]\nlog_marker = \"a\\nb\"\n", 1), want: "[project] log_marker must be one line of text"},
		{name: "extra dir relative", file: strings.Replace(minimal, "[project]\n", "[project]\nextra_read_dirs = [\"slides\"]\n", 1), want: `[project] extra_read_dirs: "slides" must be a full path`},
		{name: "mode", extra: "[loop]\nmode = \"auto\"\n", want: `[loop] mode must be "stepper" or "autopilot" (got "auto").`},
		{name: "max rounds", extra: "[loop]\nmax_rounds = 0\n", want: "[loop] max_rounds must be a whole number from 1 to 20 (got 0)."},
		{name: "sandbox", extra: "[codex]\nsandbox = \"full\"\n", want: `[codex] sandbox must be "read-only", "workspace-write" or "danger-full-access" (got "full").`},
		{name: "unsafe codex arg", extra: "[codex]\nextra_args = [\"--dangerously-bypass-approvals-and-sandbox\"]\n", want: `[codex] extra_args: "--dangerously-bypass-approvals-and-sandbox" is not allowed: twokey never removes Codex's sandbox this way.`},
		{name: "managed codex arg", extra: "[codex]\nextra_args = [\"--model=gpt-x\"]\n", want: `[codex] extra_args: "--model=gpt-x" is not allowed: twokey sets this itself. Use the matching setting in [codex].`},
		{name: "bare", extra: "[claude]\nextra_args = [\"--bare\"]\n", want: `[claude] extra_args: "--bare" is not allowed: it skips the subscription login`},
		{name: "managed claude arg", extra: "[claude]\nextra_args = [\"--add-dir\", \"x\"]\n", want: `[claude] extra_args: "--add-dir" is not allowed: twokey sets this itself.`},
		{name: "permission mode", extra: "[claude]\npermission_mode = \"\"\n", want: `[claude] permission_mode must be one word, such as "auto" (got "").`},
		{name: "budget", extra: "[claude]\nmax_budget_usd = -1\n", want: "[claude] max_budget_usd must be 0 (no limit) or more (got -1)."},
		{name: "ports format", extra: "[codex]\nports = \"56000\"\n", want: `[codex] ports must look like "56000-56499"`},
		{name: "ports order", extra: "[claude]\nports = \"57000-56000\"\n", want: `[claude] ports must look like "56000-56499"`},
		{name: "ports overlap", extra: "[claude]\nports = \"56400-56999\"\n", want: "[codex] ports and [claude] ports overlap (56000-56499 and 56400-56999)."},
		{name: "empty label", extra: "[claude]\nlabel = \"\"\n", want: `[claude] label must be one line of text (got "").`},
		{name: "timeout", extra: "[timeouts]\nfix = 0\n", want: "[timeouts] fix must be a number of minutes from 1 to 1440 (got 0)."},
		{name: "wait hours", extra: "[limits]\nmax_wait_hours = 48\n", want: "[limits] max_wait_hours must be a whole number from 0 to 24 (got 48)."},
		{name: "ntfy topic", extra: "[notify]\nntfy_topic = \"my topic\"\n", want: `[notify] ntfy_topic may use only letters, digits, - and _`},
		{name: "verify entry", file: strings.Replace(minimal, "commands = []", `commands = ["npm test", " "]`, 1), want: `[verify] commands: entry 2 must be one line of text (got " ").`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := c.file
			if file == "" {
				file = minimal + c.extra
			}
			_, err := Parse([]byte(file))
			if err == nil {
				t.Fatalf("no error, want %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("got:\n%v\nwant it to contain:\n%s", err, c.want)
			}
		})
	}
}

func TestUnknownSectionListedOnce(t *testing.T) {
	_, err := Parse([]byte(minimal + "[codx]\ncommand = \"codex\"\nlabel = \"x\"\n"))
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 1 {
		t.Fatalf("want exactly one problem, got %#v", err)
	}
}

func TestAllProblemsReported(t *testing.T) {
	_, err := Parse([]byte(minimal + "[loop]\nmax_rounds = 99\nmode = \"x\"\n[timeouts]\naudit = 0\n"))
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 3 {
		t.Fatalf("want 3 problems, got %#v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want *NotFoundError, got %T %v", err, err)
	}
}

func TestParsePorts(t *testing.T) {
	lo, hi, err := ParsePorts("56000-56499")
	if err != nil || lo != 56000 || hi != 56499 {
		t.Errorf("got %d %d %v", lo, hi, err)
	}
	for _, bad := range []string{"", "56000", "a-b", "80-90", "60000-70000", "2-1"} {
		if _, _, err := ParsePorts(bad); err == nil {
			t.Errorf("ParsePorts(%q) accepted", bad)
		}
	}
}

func quote(s string) string { return `'` + s + `'` } // TOML literal string: no escapes
