// Package config loads and checks a project's twokey config file
// (SPEC section 11).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the config file's default name, in the repository root.
const FileName = "local-only_twokey.toml"

// Config is one project's twokey config.
type Config struct {
	Project    Project           `toml:"project"`
	Checkpoint Checkpoint        `toml:"checkpoint"`
	Loop       Loop              `toml:"loop"`
	Autopilot  Autopilot         `toml:"autopilot"`
	Verify     Verify            `toml:"verify"`
	Codex      Codex             `toml:"codex"`
	Claude     Claude            `toml:"claude"`
	ModelNames map[string]string `toml:"model_names"`
	Timeouts   Timeouts          `toml:"timeouts"`
	Limits     Limits            `toml:"limits"`
	Notify     Notify            `toml:"notify"`
}

// Project is the [project] section.
type Project struct {
	Branch            string   `toml:"branch"`
	Remote            string   `toml:"remote"`
	ProtectedBranches []string `toml:"protected_branches"`
	Upstream          string   `toml:"upstream"` // "" = off
	LogFile           string   `toml:"log_file"`
	LogMarker         string   `toml:"log_marker"`
	RunDir            string   `toml:"run_dir"`
	ExtraReadDirs     []string `toml:"extra_read_dirs"`
}

// Checkpoint is the [checkpoint] section.
type Checkpoint struct {
	Label            string `toml:"label"`
	RequirementsFile string `toml:"requirements_file"`
}

// Loop is the [loop] section.
type Loop struct {
	Mode      string `toml:"mode"`
	MaxRounds int    `toml:"max_rounds"`
	FixP3     bool   `toml:"fix_p3"`
}

// Autopilot is the [autopilot] section: how unattended rounds decide.
type Autopilot struct {
	Decide bool `toml:"decide"` // take the recommended option, fix what Codex maintained (D61)
}

// Verify is the [verify] section.
type Verify struct {
	Commands []string `toml:"commands"`
}

// Codex is the [codex] section.
type Codex struct {
	Command      string   `toml:"command"`
	LabelPrefix  string   `toml:"label_prefix"`
	Label        string   `toml:"label"`
	Model        string   `toml:"model"`
	Sandbox      string   `toml:"sandbox"`
	Network      bool     `toml:"network"`
	WebSearch    string   `toml:"web_search"` // "" = Codex's default
	DesktopTools bool     `toml:"desktop_tools"`
	ExtraArgs    []string `toml:"extra_args"`
	Ports        string   `toml:"ports"`
}

// Claude is the [claude] section.
type Claude struct {
	Command        string   `toml:"command"`
	LabelPrefix    string   `toml:"label_prefix"`
	Label          string   `toml:"label"`
	Model          string   `toml:"model"`
	PermissionMode string   `toml:"permission_mode"`
	DesktopTools   bool     `toml:"desktop_tools"`
	MaxBudgetUSD   float64  `toml:"max_budget_usd"`
	ExtraArgs      []string `toml:"extra_args"`
	Ports          string   `toml:"ports"`
}

// Timeouts is the [timeouts] section, in minutes.
type Timeouts struct {
	Audit       int `toml:"audit"`
	Convergence int `toml:"convergence"`
	Dispute     int `toml:"dispute"`
	Fix         int `toml:"fix"`
	Verify      int `toml:"verify"`
}

// Limits is the [limits] section.
type Limits struct {
	MaxWaitHours       int      `toml:"max_wait_hours"`
	UsageLimitPatterns []string `toml:"usage_limit_patterns"`
}

// Notify is the [notify] section.
type Notify struct {
	Toast      bool   `toml:"toast"`
	NtfyServer string `toml:"ntfy_server"`
	NtfyTopic  string `toml:"ntfy_topic"`
}

// Default returns the values used for keys the file leaves out. The keys
// in required have no default.
func Default() *Config {
	return &Config{
		Project: Project{
			Remote:            "origin",
			ProtectedBranches: []string{"main"},
			LogFile:           "local-only_logs.md",
			LogMarker:         "<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->",
			RunDir:            "local-only_twokey",
			ExtraReadDirs:     []string{},
		},
		Loop:      Loop{Mode: "stepper", MaxRounds: 4},
		Autopilot: Autopilot{Decide: true},
		Codex: Codex{
			Command:     "codex",
			LabelPrefix: "Codex",
			Label:       "Codex - GPT-6.1-Sol",
			Sandbox:     "workspace-write",
			Network:     true,
			ExtraArgs:   []string{},
			Ports:       "56000-56499",
		},
		Claude: Claude{
			Command:        "claude",
			LabelPrefix:    "Claude Code",
			Label:          "Claude Code - Claude Opus 5.5",
			PermissionMode: "auto",
			ExtraArgs:      []string{},
			Ports:          "56500-56999",
		},
		ModelNames: map[string]string{
			"gpt-6.1-sol":     "GPT-6.1-Sol",
			"claude-opus-5-5": "Claude Opus 5.5",
		},
		Timeouts: Timeouts{Audit: 90, Convergence: 75, Dispute: 30, Fix: 120, Verify: 20},
		Limits: Limits{
			MaxWaitHours:       5,
			UsageLimitPatterns: []string{"hit your", "usage limit", "rate limit", "try again"},
		},
		Notify: Notify{Toast: true, NtfyServer: "https://ntfy.sh"},
	}
}

// required lists the keys that must be in the file, with a hint for each.
var required = []struct {
	key  []string
	hint string
}{
	{[]string{"project", "branch"}, "It names the branch twokey audits, fixes and pushes."},
	{[]string{"checkpoint", "label"}, `It names the milestone, for example "Checkpoint 3".`},
	{[]string{"checkpoint", "requirements_file"}, "It names the file with the requirements."},
	{[]string{"verify", "commands"}, "List the project's checks, or write commands = [] for none."},
}

// NotFoundError means the config file doesn't exist.
type NotFoundError struct{ Path string }

func (e *NotFoundError) Error() string { return "config file not found: " + e.Path }

// Problems lists everything wrong with a config file. Each entry names the
// key, for example "[loop] max_rounds must be ...".
type Problems []string

func (p Problems) Error() string { return strings.Join(p, "\n") }

// Load reads and checks a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &NotFoundError{Path: path}
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse reads and checks config file content. Unknown keys are errors, so
// a typo can't be ignored silently.
func Parse(data []byte) (*Config, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, Problems{decodeProblem(err)}
	}

	probs := unknownKeys(md)
	for _, r := range required {
		if !md.IsDefined(r.key...) {
			probs = append(probs, fmt.Sprintf("%s is missing. %s", keyName(r.key), r.hint))
		}
	}
	if len(probs) == 0 {
		probs = cfg.Validate()
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return cfg, nil
}

// unknownKeys reports keys and sections that twokey doesn't know. Keys
// inside an unknown section aren't listed again.
func unknownKeys(md toml.MetaData) Problems {
	var probs Problems
	undecoded := md.Undecoded()
	seen := map[string]bool{}
	for _, k := range undecoded {
		seen[k.String()] = true
	}
	for _, k := range undecoded {
		if parentSeen(k, seen) {
			continue
		}
		switch {
		case md.Type(k...) == "Hash" || md.Type(k...) == "ArrayHash":
			probs = append(probs, fmt.Sprintf("[%s] is not a known section. Check the spelling.", k.String()))
		case len(k) == 1:
			probs = append(probs, fmt.Sprintf("%q is not a known setting. Settings go inside a section such as [project].", k[0]))
		default:
			probs = append(probs, fmt.Sprintf("%s is not a known setting. Check the spelling.", keyName(k)))
		}
	}
	return probs
}

func parentSeen(k toml.Key, seen map[string]bool) bool {
	for i := 1; i < len(k); i++ {
		if seen[k[:i].String()] {
			return true
		}
	}
	return false
}

// keyName writes a key the way the file shows it: "[loop] max_rounds".
func keyName(k []string) string {
	if len(k) == 1 {
		return k[0]
	}
	rest := toml.Key(k[1:]).String()
	return fmt.Sprintf("[%s] %s", k[0], rest)
}

var typeErr = regexp.MustCompile(`last key "([^"]+)"\): incompatible types: TOML value has type (\w+); destination has type (\w+)`)

// decodeProblem turns a TOML error into a plain sentence.
func decodeProblem(err error) string {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		return fmt.Sprintf("The file is not valid TOML. Line %d: %s.", pe.Position.Line, strings.TrimSuffix(pe.Message, "."))
	}
	if m := typeErr.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Sprintf("%s must be %s, not %s.", keyName(strings.Split(m[1], ".")), typeWord(m[3]), typeWord(m[2]))
	}
	return strings.TrimPrefix(err.Error(), "toml: ")
}

func typeWord(t string) string {
	switch strings.ToLower(t) {
	case "integer":
		return "a whole number"
	case "float":
		return "a number"
	case "string":
		return "text in quotes"
	case "bool":
		return "true or false"
	case "slice", "array":
		return "a list in [ ]"
	case "map", "struct", "hash", "table":
		return "a section"
	default:
		return t
	}
}
