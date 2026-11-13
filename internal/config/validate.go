package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Validate checks the values. It returns one sentence per problem, each
// naming the key.
func (c *Config) Validate() Problems {
	var p Problems
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }

	// [project]
	if err := checkBranch(c.Project.Branch); err != nil {
		add("[project] branch %s", err)
	}
	if slices.Contains(c.Project.ProtectedBranches, c.Project.Branch) {
		add("[project] branch %q is also in protected_branches. twokey must never push to a protected branch.", c.Project.Branch)
	}
	if c.Project.Remote == "" || strings.ContainsAny(c.Project.Remote, " \t\r\n") {
		add("[project] remote must be a remote name such as \"origin\" (got %q).", c.Project.Remote)
	}
	for _, b := range c.Project.ProtectedBranches {
		if err := checkBranch(b); err != nil {
			add("[project] protected_branches: %q %s", b, err)
		}
	}
	if u := c.Project.Upstream; u != "" {
		if err := checkBranch(u); err != nil {
			add("[project] upstream %s", err)
		} else if u == c.Project.Branch {
			add("[project] upstream must be another branch than branch (both are %q), or \"\" for off.", u)
		}
	}
	if !localPath(c.Project.LogFile) {
		add("[project] log_file must be a path inside the repository, such as \"local-only_logs.md\" (got %q).", c.Project.LogFile)
	}
	if !oneLine(c.Project.LogMarker) {
		add("[project] log_marker must be one line of text (got %q).", c.Project.LogMarker)
	}
	if !localPath(c.Project.RunDir) {
		add("[project] run_dir must be a folder inside the repository, such as \"local-only_twokey\" (got %q).", c.Project.RunDir)
	}
	for _, d := range c.Project.ExtraReadDirs {
		if !filepath.IsAbs(d) {
			add("[project] extra_read_dirs: %q must be a full path, such as \"C:\\\\Courses\\\\slides\".", d)
		}
	}

	// [checkpoint]
	if !oneLine(c.Checkpoint.Label) {
		add("[checkpoint] label must be one line of text (got %q).", c.Checkpoint.Label)
	}
	if req := c.Checkpoint.RequirementsFile; strings.TrimSpace(req) == "" || (!filepath.IsAbs(req) && !localPath(req)) {
		add("[checkpoint] requirements_file must be a file in the repository or a full path (got %q).", req)
	}

	// [loop]
	if c.Loop.Mode != "stepper" && c.Loop.Mode != "autopilot" {
		add("[loop] mode must be \"stepper\" or \"autopilot\" (got %q).", c.Loop.Mode)
	}
	if c.Loop.MaxRounds < 1 || c.Loop.MaxRounds > 20 {
		add("[loop] max_rounds must be a whole number from 1 to 20 (got %d).", c.Loop.MaxRounds)
	}

	// [verify]
	for i, cmd := range c.Verify.Commands {
		if !oneLine(cmd) {
			add("[verify] commands: entry %d must be one line of text (got %q).", i+1, cmd)
		}
	}

	// [codex] and [claude]
	p = append(p, checkAgent("codex", c.Codex.Command, c.Codex.LabelPrefix, c.Codex.Label, c.Codex.ExtraArgs, codexArgs)...)
	if !slices.Contains([]string{"read-only", "workspace-write", "danger-full-access"}, c.Codex.Sandbox) {
		add("[codex] sandbox must be \"read-only\", \"workspace-write\" or \"danger-full-access\" (got %q).", c.Codex.Sandbox)
	}
	if !slices.Contains([]string{"", "disabled", "cached", "indexed", "live"}, c.Codex.WebSearch) {
		add("[codex] web_search must be \"\" (Codex's default), \"disabled\", \"cached\", \"indexed\" or \"live\" (got %q).", c.Codex.WebSearch)
	}
	p = append(p, checkAgent("claude", c.Claude.Command, c.Claude.LabelPrefix, c.Claude.Label, c.Claude.ExtraArgs, claudeArgs)...)
	if c.Claude.PermissionMode == "" || strings.ContainsAny(c.Claude.PermissionMode, " \t\r\n") {
		add("[claude] permission_mode must be one word, such as \"auto\" (got %q).", c.Claude.PermissionMode)
	}
	if c.Claude.MaxBudgetUSD < 0 {
		add("[claude] max_budget_usd must be 0 (no limit) or more (got %g).", c.Claude.MaxBudgetUSD)
	}
	cxLo, cxHi, cxErr := ParsePorts(c.Codex.Ports)
	if cxErr != nil {
		add("[codex] ports %s", cxErr)
	}
	clLo, clHi, clErr := ParsePorts(c.Claude.Ports)
	if clErr != nil {
		add("[claude] ports %s", clErr)
	}
	if cxErr == nil && clErr == nil && cxLo <= clHi && clLo <= cxHi {
		add("[codex] ports and [claude] ports overlap (%s and %s). Each agent needs its own range.", c.Codex.Ports, c.Claude.Ports)
	}

	// [model_names]
	for id, name := range c.ModelNames {
		if !oneLine(id) || !oneLine(name) {
			add("[model_names] %q = %q: both sides must be one line of text.", id, name)
		}
	}

	// [timeouts]
	for _, t := range []struct {
		key string
		val int
	}{
		{"audit", c.Timeouts.Audit},
		{"convergence", c.Timeouts.Convergence},
		{"dispute", c.Timeouts.Dispute},
		{"fix", c.Timeouts.Fix},
		{"verify", c.Timeouts.Verify},
	} {
		if t.val < 1 || t.val > 1440 {
			add("[timeouts] %s must be a number of minutes from 1 to 1440 (got %d).", t.key, t.val)
		}
	}

	// [limits]
	if c.Limits.MaxWaitHours < 0 || c.Limits.MaxWaitHours > 24 {
		add("[limits] max_wait_hours must be a whole number from 0 to 24 (got %d).", c.Limits.MaxWaitHours)
	}
	for i, pat := range c.Limits.UsageLimitPatterns {
		if !oneLine(pat) {
			add("[limits] usage_limit_patterns: entry %d must be one line of text (got %q).", i+1, pat)
		}
	}

	// [notify]
	if u, err := url.Parse(c.Notify.NtfyServer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		add("[notify] ntfy_server must be a web address such as \"https://ntfy.sh\" (got %q).", c.Notify.NtfyServer)
	}
	if t := c.Notify.NtfyTopic; t != "" && !ntfyTopic.MatchString(t) {
		add("[notify] ntfy_topic may use only letters, digits, - and _ (up to 64), or be \"\" for off (got %q).", t)
	}
	return p
}

var ntfyTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ParsePorts reads a port range such as "56000-56499".
func ParsePorts(s string) (lo, hi int, err error) {
	bad := fmt.Errorf("must look like \"56000-56499\": two port numbers from 1024 to 65535, the smaller one first (got %q)", s)
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, bad
	}
	lo, errA := strconv.Atoi(a)
	hi, errB := strconv.Atoi(b)
	if errA != nil || errB != nil || lo < 1024 || hi > 65535 || lo > hi {
		return 0, 0, bad
	}
	return lo, hi, nil
}

// checkBranch applies the main rules of "git check-ref-format --branch".
func checkBranch(b string) error {
	switch {
	case b == "":
		return errors.New("must not be empty")
	case strings.ContainsAny(b, " \t\r\n~^:?*[\\"),
		strings.HasPrefix(b, "-"), strings.HasPrefix(b, "/"), strings.HasSuffix(b, "/"),
		strings.HasSuffix(b, "."), strings.HasSuffix(b, ".lock"),
		strings.Contains(b, ".."), strings.Contains(b, "//"), strings.Contains(b, "@{"):
		return fmt.Errorf("is not a valid branch name (got %q)", b)
	}
	return nil
}

func localPath(p string) bool {
	return strings.TrimSpace(p) != "" && filepath.IsLocal(p)
}

func oneLine(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\r\n")
}

// argRule says why an extra argument is refused.
type argRule struct {
	flags  []string
	reason string
}

var codexArgs = []argRule{
	{[]string{"--dangerously-bypass-approvals-and-sandbox", "--full-auto", "--yolo"},
		"twokey never removes Codex's sandbox this way."},
	{[]string{"--worktree", "--ephemeral"},
		"twokey runs Codex in the main checkout and needs sessions it can resume."},
	{[]string{"--json", "--output-schema", "-o", "--output-last-message", "-s", "--sandbox", "-C", "--cd",
		"--add-dir", "-m", "--model", "--approve-for-me"},
		"twokey sets this itself. Use the matching setting in [codex]."},
}

var claudeArgs = []argRule{
	{[]string{"--bare"},
		"it skips the subscription login and the project's settings."},
	{[]string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions"},
		"twokey never turns off permission checks this way."},
	{[]string{"-w", "--worktree", "--tmux", "--no-session-persistence", "--bg", "--background",
		"--cloud", "--remote-control", "--teleport", "--desktop"},
		"twokey runs Claude in the main checkout, in the foreground, with sessions it can resume."},
	{[]string{"-p", "--print", "--output-format", "--input-format", "--json-schema", "--permission-mode",
		"--permission-prompts", "--add-dir", "-r", "--resume", "-c", "--continue", "--fork-session",
		"--session-id", "--model", "--max-budget-usd", "--chrome", "--no-chrome"},
		"twokey sets this itself. Use the matching setting in [claude]."},
}

func checkAgent(section, command, labelPrefix, label string, extraArgs []string, rules []argRule) Problems {
	var p Problems
	if strings.TrimSpace(command) == "" {
		p = append(p, fmt.Sprintf("[%s] command must name the program, such as %q.", section, section))
	}
	if !oneLine(labelPrefix) {
		p = append(p, fmt.Sprintf("[%s] label_prefix must be one line of text (got %q).", section, labelPrefix))
	}
	if !oneLine(label) {
		p = append(p, fmt.Sprintf("[%s] label must be one line of text (got %q).", section, label))
	}
	for _, arg := range extraArgs {
		for _, r := range rules {
			if matchesFlag(arg, r.flags) {
				p = append(p, fmt.Sprintf("[%s] extra_args: %q is not allowed: %s", section, arg, r.reason))
			}
		}
	}
	return p
}

func matchesFlag(arg string, flags []string) bool {
	for _, f := range flags {
		if arg == f || strings.HasPrefix(arg, f+"=") {
			return true
		}
	}
	return false
}
