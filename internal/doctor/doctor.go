// Package doctor implements "twokey doctor": check that everything twokey
// needs is in place, and say what to do about each problem (SPEC section 12).
// Only --smoke calls the agents.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/initcmd"
	"github.com/Sten-Qy-Li/twokey/internal/logfile"
	"github.com/Sten-Qy-Li/twokey/internal/paths"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/upstream"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

// Options for Run.
type Options struct {
	Dir         string // the folder twokey was started in
	ConfigPath  string // "" = <repo root>/local-only_twokey.toml
	Runner      proc.Runner
	Out         io.Writer
	StagingRoot string // "" = paths.StagingRoot()
	ScratchRoot string // "" = paths.ScratchRoot()
	GOOS        string // "" = runtime.GOOS
	// Smoke makes small real calls to both agents (doctor --smoke).
	Smoke  bool
	Launch func(context.Context, proc.TreeCmd) proc.TreeResult // "" = proc.RunTree; tests pass a fake
}

// The options twokey passes to each CLI (SPEC section 9, DECISIONS.md).
var (
	codexExecFlags   = []string{"--json", "--output-schema", "--output-last-message", "--sandbox", "--cd", "--add-dir", "--config", "--model"}
	codexResumeFlags = []string{"--json", "--output-schema", "--output-last-message", "--config", "--model"}
	claudeFlags      = []string{"--print", "--output-format", "--json-schema", "--permission-mode", "--permission-prompts",
		"--add-dir", "--resume", "--session-id", "--model", "--no-chrome", "--max-budget-usd"}
)

// The CLI versions twokey was last checked against (DECISIONS.md D3).
const (
	checkedCodex  = "codex-cli 0.165.0"
	checkedClaude = "Claude Code 2.1.311"
)

const (
	shortTimeout = 30 * time.Second
	fetchTimeout = 2 * time.Minute
)

type doctor struct {
	Options
	ctx          context.Context
	fails, warns int
	found        map[string]bool // the agent CLIs found on PATH
}

// Run runs every check and returns the exit code: 1 if any check failed.
func Run(ctx context.Context, o Options) int {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	d := &doctor{Options: o, ctx: ctx, found: map[string]bool{}}
	fmt.Fprintf(d.Out, "twokey doctor (twokey %s, %s/%s)\n", version.String(), runtime.GOOS, runtime.GOARCH)

	d.section("Tools")
	root := ""
	if d.checkGit() {
		root = d.findRoot()
	}
	cfg := d.loadConfig(root)
	cmds := cfg
	if cmds == nil {
		cmds = config.Default() // still check the CLIs
	}
	d.checkCodex(cmds)
	d.checkClaude(cmds)

	if cfg != nil && root != "" {
		d.section("Repository")
		d.checkRepo(root, cfg)
	}

	d.section("Folders and services")
	d.checkFolders()
	d.checkDocker()
	d.checkNotify(cmds)

	if o.Smoke {
		switch {
		case cfg == nil || root == "":
			d.section("Smoke test (real calls)")
			d.fail("The smoke test needs the repository and a valid config.", "fix the [FAIL] lines above, then run twokey doctor --smoke again.")
		case !d.found["codex"] || !d.found["claude"]:
			d.section("Smoke test (real calls)")
			d.fail("The smoke test needs both agent CLIs.", "fix the [FAIL] lines above, then run twokey doctor --smoke again.")
		default:
			d.smoke(root, cfg)
		}
	}

	fmt.Fprintln(d.Out)
	switch {
	case d.fails == 0 && d.warns == 0:
		fmt.Fprintln(d.Out, "All checks passed.")
	case d.fails == 0:
		fmt.Fprintf(d.Out, "No problems, %s.\n", plural(d.warns, "warning"))
	default:
		fmt.Fprintf(d.Out, "%s, %s. Fix the [FAIL] lines, then run twokey doctor again.\n",
			plural(d.fails, "problem"), plural(d.warns, "warning"))
	}
	if d.fails > 0 {
		return 1
	}
	return 0
}

func (d *doctor) section(title string) { fmt.Fprintf(d.Out, "\n%s\n", title) }

func (d *doctor) ok(msg string) { ui.Line(d.Out, ui.OK, msg) }

func (d *doctor) warn(msg string, next ...string) {
	d.warns++
	ui.Line(d.Out, ui.Warn, msg)
	for _, n := range next {
		ui.Next(d.Out, n)
	}
}

func (d *doctor) fail(msg string, next ...string) {
	d.fails++
	ui.Line(d.Out, ui.Fail, msg)
	for _, n := range next {
		ui.Next(d.Out, n)
	}
}

func (d *doctor) run(timeout time.Duration, name string, args ...string) proc.Result {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	return d.Runner.Run(ctx, proc.Cmd{Dir: d.Dir, Name: name, Args: args})
}

// git returns a context with a timeout and the repo to run git in.
func (d *doctor) git(dir string, timeout time.Duration) (context.Context, context.CancelFunc, gitx.Repo) {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	return ctx, cancel, gitx.Repo{Dir: dir, R: d.Runner}
}

func (d *doctor) checkGit() bool {
	if _, err := d.Runner.LookPath("git"); err != nil {
		d.fail("git is not installed, or not on PATH.", "install Git (git-scm.com) and open a new terminal.")
		return false
	}
	ctx, cancel, repo := d.git(d.Dir, shortTimeout)
	defer cancel()
	v, err := repo.Version(ctx)
	if err != nil {
		d.fail("git doesn't run: " + err.Error())
		return false
	}
	d.ok(v)
	return true
}

func (d *doctor) findRoot() string {
	ctx, cancel, repo := d.git(d.Dir, shortTimeout)
	defer cancel()
	root, err := repo.TopLevel(ctx)
	if err != nil {
		d.fail("This folder is not inside a git repository.", "run twokey doctor in the project's repository.")
		return ""
	}
	return root
}

func (d *doctor) loadConfig(root string) *config.Config {
	if root == "" && d.ConfigPath == "" {
		return nil
	}
	path := initcmd.ConfigPath(d.Dir, root, d.ConfigPath)
	cfg, err := config.Load(path)
	var notFound *config.NotFoundError
	var probs config.Problems
	switch {
	case errors.As(err, &notFound):
		d.fail("No config file: "+path, "run twokey init. The repository checks need a config file.")
	case errors.As(err, &probs):
		d.fail(fmt.Sprintf("The config file has %s: %s", plural(len(probs), "problem"), path))
		for _, p := range probs {
			ui.Detail(d.Out, "- "+p)
		}
		ui.Next(d.Out, "fix these in the config file. The repository checks run once it loads.")
	case err != nil:
		d.fail("Can't read the config file: " + err.Error())
	default:
		d.ok("Config file is valid: " + path)
		return cfg
	}
	return nil
}

func (d *doctor) checkCodex(cfg *config.Config) {
	path, ok := d.findCLI("Codex CLI", cfg.Codex.Command, "[codex] command")
	d.found["codex"] = ok
	if !ok {
		return
	}
	exec := d.run(shortTimeout, path, "exec", "--help")
	help := exec.Stdout + exec.Stderr
	if miss := Missing(help, codexExecFlags); exec.Err != nil || len(miss) > 0 {
		d.fail(fmt.Sprintf("codex exec lacks options twokey needs: %s", strings.Join(miss, ", ")),
			fmt.Sprintf("update Codex CLI. twokey was checked with %s.", checkedCodex))
	} else {
		d.ok("codex exec has every option twokey uses")
	}
	if modes := Choices(help, "--sandbox"); modes != nil && !slices.Contains(modes, cfg.Codex.Sandbox) {
		d.fail(fmt.Sprintf("Codex doesn't know the sandbox %q from [codex] sandbox. It knows: %s.",
			cfg.Codex.Sandbox, strings.Join(modes, ", ")))
	}

	resume := d.run(shortTimeout, path, "exec", "resume", "--help")
	if miss := Missing(resume.Stdout+resume.Stderr, codexResumeFlags); resume.Err != nil || len(miss) > 0 {
		d.fail(fmt.Sprintf("codex exec resume lacks options twokey needs: %s", strings.Join(miss, ", ")),
			fmt.Sprintf("update Codex CLI. twokey was checked with %s.", checkedCodex))
	} else {
		d.ok("codex exec resume has every option twokey uses")
	}
}

func (d *doctor) checkClaude(cfg *config.Config) {
	path, ok := d.findCLI("Claude Code", cfg.Claude.Command, "[claude] command")
	d.found["claude"] = ok
	if !ok {
		return
	}
	r := d.run(shortTimeout, path, "--help")
	help := r.Stdout + r.Stderr
	want := claudeFlags
	if cfg.Claude.MaxBudgetUSD > 0 {
		want = append(slices.Clone(want), "--max-budget-usd")
	}
	if miss := Missing(help, want); r.Err != nil || len(miss) > 0 {
		d.fail(fmt.Sprintf("claude lacks options twokey needs: %s", strings.Join(miss, ", ")),
			fmt.Sprintf("update Claude Code. twokey was checked with %s.", checkedClaude))
		return
	}
	d.ok("claude has every option twokey uses")

	mode := cfg.Claude.PermissionMode
	switch modes := Choices(help, "--permission-mode"); {
	case modes == nil:
		d.warn("Can't read the permission modes from claude --help.")
	case !slices.Contains(modes, mode):
		d.fail(fmt.Sprintf("Claude Code doesn't know the permission mode %q from [claude] permission_mode. It knows: %s.",
			mode, strings.Join(modes, ", ")))
	case mode == "bypassPermissions":
		d.warn(`[claude] permission_mode "bypassPermissions" lets Claude run anything without checks.`,
			`use "auto" unless you really need this.`)
	default:
		d.ok(fmt.Sprintf("Claude permission mode %q is supported", mode))
	}
	if !slices.Contains(Choices(help, "--permission-prompts"), "none") {
		d.fail(`claude --permission-prompts doesn't offer "none". Unattended runs need it.`,
			fmt.Sprintf("update Claude Code. twokey was checked with %s.", checkedClaude))
	}
}

// findCLI finds an agent CLI and prints its version.
func (d *doctor) findCLI(name, command, key string) (string, bool) {
	path, err := d.Runner.LookPath(command)
	if err != nil {
		d.fail(fmt.Sprintf("%s (%q) is not on PATH.", name, command),
			fmt.Sprintf("install it, or set %s to its full path.", key))
		return "", false
	}
	r := d.run(shortTimeout, path, "--version")
	v := firstLine(r.Stdout)
	if r.Err != nil || v == "" {
		d.fail(fmt.Sprintf("%s doesn't run: %s", name, path))
		return "", false
	}
	// claude --version prints "2.1.311 (Claude Code)"; don't name it twice.
	v = strings.TrimSuffix(v, " ("+name+")")
	d.ok(fmt.Sprintf("%s %s (%s)", name, v, path))
	return path, true
}

func (d *doctor) checkRepo(root string, cfg *config.Config) {
	p := cfg.Project
	ctx, cancel, repo := d.git(root, shortTimeout)
	defer cancel()

	cur, err := repo.CurrentBranch(ctx)
	switch {
	case err != nil:
		d.fail("HEAD is not on a branch.", "git switch "+p.Branch)
	case cur != p.Branch:
		d.fail(fmt.Sprintf("The repository is on %s, but [project] branch is %s.", cur, p.Branch), "git switch "+p.Branch)
	default:
		d.ok("On branch " + p.Branch)
	}

	if st, err := repo.Status(ctx); err != nil {
		d.fail("git status failed: " + err.Error())
	} else if st != "" {
		lines := strings.Split(st, "\n")
		d.fail(fmt.Sprintf("The working tree has %s that git doesn't ignore:", plural(len(lines), "changed file")))
		for i, l := range lines {
			if i == 5 {
				ui.Detail(d.Out, fmt.Sprintf("... and %d more", len(lines)-5))
				break
			}
			ui.Detail(d.Out, l)
		}
		ui.Next(d.Out, "commit them, or make git ignore them. twokey starts only from a clean tree.")
	} else {
		d.ok("The working tree is clean")
	}

	d.checkRemote(root, p)
	d.checkFiles(root, cfg)
}

func (d *doctor) checkRemote(root string, p config.Project) {
	ctx, cancel, repo := d.git(root, fetchTimeout)
	defer cancel()
	if _, err := repo.RemoteURL(ctx, p.Remote); err != nil {
		d.fail(fmt.Sprintf("There is no remote named %q.", p.Remote), "fix [project] remote, or add the remote.")
		return
	}
	if err := repo.Fetch(ctx, p.Remote); err != nil {
		d.fail(fmt.Sprintf("git fetch from %s failed: %v", p.Remote, err), "check your network and login, then try again.")
		return
	}

	tracking := p.Remote + "/" + p.Branch
	remoteSHA, err := repo.RevParse(ctx, "refs/remotes/"+tracking)
	if err != nil {
		d.fail(fmt.Sprintf("%s has no branch %s yet.", p.Remote, p.Branch), fmt.Sprintf("git push -u %s %s", p.Remote, p.Branch))
	} else if head, err := repo.RevParse(ctx, "HEAD"); err != nil {
		d.fail("The repository has no commits yet.")
	} else if head == remoteSHA {
		d.ok(fmt.Sprintf("HEAD matches %s after git fetch (%s)", tracking, head[:12]))
	} else {
		ahead, behind, err := repo.AheadBehind(ctx, "HEAD", "refs/remotes/"+tracking)
		switch {
		case err != nil:
			d.fail(fmt.Sprintf("HEAD differs from %s.", tracking))
		case behind == 0:
			d.fail(fmt.Sprintf("Your branch is %s ahead of %s.", plural(ahead, "commit"), tracking), "git push")
		case ahead == 0:
			d.fail(fmt.Sprintf("Your branch is %s behind %s.", plural(behind, "commit"), tracking), "git pull --ff-only")
		default:
			d.fail(fmt.Sprintf("Your branch and %s have diverged (%d and %d different commits).", tracking, ahead, behind),
				"sort this out by hand. twokey never merges or rebases.")
		}
	}

	if p.Upstream != "" {
		d.checkUpstream(ctx, repo, p)
	}

	var found, missing []string
	for _, b := range p.ProtectedBranches {
		if _, ok, err := repo.LsRemoteBranch(ctx, p.Remote, b); err == nil && ok {
			found = append(found, b)
		} else {
			missing = append(missing, b)
		}
	}
	if len(found) > 0 {
		d.ok(fmt.Sprintf("Protected branches on %s: %s", p.Remote, strings.Join(found, ", ")))
	}
	if len(missing) > 0 {
		d.warn(fmt.Sprintf("Protected branches not found on %s: %s", p.Remote, strings.Join(missing, ", ")),
			"check [project] protected_branches.")
	}
}

// checkUpstream shows how the branch stands to [project] upstream, after
// git fetch (DECISIONS.md D39).
func (d *doctor) checkUpstream(ctx context.Context, repo gitx.Repo, p config.Project) {
	branch, up := p.Remote+"/"+p.Branch, p.Remote+"/"+p.Upstream
	if _, err := repo.RevParse(ctx, "refs/remotes/"+up); err != nil {
		d.fail(fmt.Sprintf("%s doesn't exist, but [project] upstream names it.", up), "check [project] upstream, or set it to \"\" for off.")
		return
	}
	ahead, behind, err := repo.AheadBehind(ctx, "refs/remotes/"+branch, "refs/remotes/"+up)
	if err != nil {
		return // the branch itself is missing on the remote: reported above
	}
	rel := upstream.Classify(ahead, behind)
	msg := upstream.Describe(rel, ahead, behind, branch, up)
	switch rel {
	case upstream.Behind:
		d.ok(msg + " twokey fast-forwards it when a round starts.")
	case upstream.Diverged:
		d.warn(msg+" A run pauses at its start until they no longer diverge.",
			fmt.Sprintf("merge %s into %s through a merge request with a merge commit (not squash).", p.Branch, p.Upstream))
	default:
		d.ok(msg)
	}
}

func (d *doctor) checkFiles(root string, cfg *config.Config) {
	p := cfg.Project
	inRepo := func(rel string) string {
		if filepath.IsAbs(rel) {
			return rel
		}
		return filepath.Join(root, rel)
	}

	req := cfg.Checkpoint.RequirementsFile
	if fi, err := os.Stat(inRepo(req)); err != nil || fi.IsDir() {
		d.fail("The requirements file is missing: "+req, "add it, or fix [checkpoint] requirements_file.")
	} else {
		d.ok("Requirements file: " + req)
	}

	d.checkLog(inRepo(p.LogFile), p.LogFile, p.LogMarker)

	ctx, cancel, repo := d.git(root, shortTimeout)
	defer cancel()
	ignoreChecks := []string{p.RunDir}
	if rel, err := filepath.Rel(root, initcmd.ConfigPath(d.Dir, root, d.ConfigPath)); err == nil && filepath.IsLocal(rel) {
		ignoreChecks = append([]string{rel}, ignoreChecks...)
	}
	for _, rel := range ignoreChecks {
		name := filepath.ToSlash(rel)
		switch ignored, err := repo.IsIgnored(ctx, rel); {
		case err != nil:
			d.fail(fmt.Sprintf("Can't check whether git ignores %s: %v", name, err))
		case !ignored:
			d.fail(fmt.Sprintf("git does not ignore %s. It must never be committed.", name), "add this line to .gitignore: /"+name)
		default:
			d.ok("git ignores " + name)
		}
	}

	lock := filepath.Join(root, p.RunDir, "lock")
	if _, err := os.Stat(lock); err == nil {
		d.fail("A twokey lock file exists: "+lock+". Another run may be active.",
			"if no twokey run is active, delete the lock file.")
	} else {
		d.ok("No other twokey run holds the lock")
	}

	for _, dir := range p.ExtraReadDirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			d.fail("Extra read folder not found: "+dir, "fix [project] extra_read_dirs.")
		} else {
			d.ok("Agents may read: " + dir)
		}
	}

	if len(cfg.Verify.Commands) == 0 {
		d.warn("[verify] commands is empty. Fixes are checked by the git rules only.")
	}
}

func (d *doctor) checkLog(path, rel, marker string) {
	data, err := os.ReadFile(path)
	if err != nil {
		d.fail("The log file is missing: "+rel, "create it with the marker line, or fix [project] log_file.")
		return
	}
	info := logfile.Inspect(data, marker)
	switch n := len(info.MarkerLines); n {
	case 0:
		d.fail("The marker line is missing in "+rel+".", "add this line where new entries go: "+marker)
	case 1:
		bom := "UTF-8 without BOM"
		if info.BOM {
			bom = "UTF-8 with BOM"
		}
		d.ok(fmt.Sprintf("Log file %s: marker on line %d, %s, %s line endings", rel, info.MarkerLines[0], bom, info.LineEnding))
	default:
		var nums []string
		for _, l := range info.MarkerLines {
			nums = append(nums, strconv.Itoa(l))
		}
		d.fail(fmt.Sprintf("The marker line appears %d times in %s (lines %s). It must appear once.", n, rel, strings.Join(nums, ", ")),
			"remove the extra marker lines.")
	}
	if !utf8.Valid(data) {
		d.warn("The log file is not valid UTF-8. twokey expects UTF-8.")
	}
	if info.LineEnding == logfile.Mixed {
		d.warn("The log file mixes LF and CRLF line endings. New entries will use the marker line's ending.")
	}
}

func (d *doctor) checkFolders() {
	staging := d.StagingRoot
	if staging == "" {
		s, err := paths.StagingRoot()
		if err != nil {
			d.fail("Can't find the folder for local app data: " + err.Error())
			return
		}
		staging = s
	}
	scratch := d.ScratchRoot
	if scratch == "" {
		scratch = paths.ScratchRoot()
	}
	for _, f := range []struct{ what, dir string }{
		{"Staging folder (blind audit results)", staging},
		{"Scratch folder (agent work files)", scratch},
	} {
		if err := probeWritable(f.dir); err != nil {
			d.fail(fmt.Sprintf("%s is not writable: %s (%v)", f.what, f.dir, err))
		} else {
			d.ok(fmt.Sprintf("%s: %s", f.what, f.dir))
		}
	}
}

func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

func (d *doctor) checkDocker() {
	path, err := d.Runner.LookPath("docker")
	if err != nil {
		d.warn("Docker is not installed, or not on PATH. Agents can't start containers.",
			"install Docker Desktop if the project needs containers.")
		return
	}
	r := d.run(shortTimeout, path, "info", "--format", "{{.ServerVersion}}")
	v := firstLine(r.Stdout)
	if r.Err != nil || v == "" {
		d.warn("Docker is installed but not running. Agents can't start containers, and twokey can't clean them up.",
			"start Docker Desktop before a run if the project needs containers.")
		return
	}
	d.ok("Docker is running (server " + v + ")")
}

func (d *doctor) checkNotify(cfg *config.Config) {
	switch {
	case !cfg.Notify.Toast:
		d.ok("Windows toasts: off")
	case d.GOOS != "windows":
		d.warn("Toast notifications work only on Windows.", "set [notify] toast = false.")
	default:
		if _, err := d.Runner.LookPath("powershell.exe"); err != nil {
			d.warn("PowerShell is not on PATH, so twokey can't show toasts.")
		} else {
			d.ok("Windows toasts: on (PowerShell found)")
		}
	}
	if cfg.Notify.NtfyTopic == "" {
		d.ok("Phone push (ntfy): off")
	} else {
		d.warn("Phone push (ntfy) is not built yet. twokey ignores [notify] ntfy_topic for now.")
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
