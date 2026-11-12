package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

// fakeRunner runs git for real and plays recorded answers for everything
// else. Tests never call a real agent CLI or Docker.
type fakeRunner struct {
	paths map[string]string      // LookPath name -> fake path
	runs  map[string]proc.Result // "path arg arg" -> result
	// sandbox answers "codex sandbox" calls by the command they run.
	sandbox func(cmd string) proc.Result
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if name == "git" {
		return exec.LookPath(name)
	}
	if p, ok := f.paths[name]; ok {
		return p, nil
	}
	return "", exec.ErrNotFound
}

func (f *fakeRunner) Run(ctx context.Context, c proc.Cmd) proc.Result {
	if strings.TrimSuffix(filepath.Base(c.Name), ".exe") == "git" {
		return proc.OS{}.Run(ctx, c)
	}
	key := strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	if r, ok := f.runs[key]; ok {
		return r
	}
	if _, cmd, ok := strings.Cut(key, " -- "); ok && strings.HasPrefix(key, "/fake/codex sandbox ") && f.sandbox != nil {
		return f.sandbox(cmd)
	}
	return proc.Result{ExitCode: 1, Err: fmt.Errorf("fake: no answer for %q", key)}
}

func ok(stdout string) proc.Result { return proc.Result{Stdout: stdout} }

func healthyRunner(t *testing.T) *fakeRunner {
	return &fakeRunner{
		paths: map[string]string{
			"codex": "/fake/codex", "claude": "/fake/claude",
			"docker": "/fake/docker", "powershell.exe": "/fake/powershell.exe",
		},
		runs: map[string]proc.Result{
			"/fake/codex --version":                             ok("codex-cli 0.165.0\n"),
			"/fake/codex exec --help":                           ok(fixture(t, "codex-0.165.0-exec-help.txt")),
			"/fake/codex exec resume --help":                    ok(fixture(t, "codex-0.165.0-exec-resume-help.txt")),
			"/fake/claude --version":                            ok("2.1.311 (Claude Code)\n"),
			"/fake/claude --help":                               ok(fixture(t, "claude-2.1.311-help.txt")),
			"/fake/docker info --format {{.ServerVersion}}":     ok("28.0.1\n"),
			"/fake/docker version --format {{.Server.Version}}": ok("28.0.1\n"),
		},
		sandbox: func(string) proc.Result { return ok("") },
	}
}

const marker = "<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->"

// project is a healthy project: config, requirements and log are
// git-ignored, and the branch matches origin.
type project struct {
	testutil.Repo
	runner *fakeRunner
	goos   string
}

func newProject(t *testing.T) *project {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, ".gitignore", "local-only_*\n")
	r.Commit(t, "ignore local files")
	testutil.Git(t, r.Dir, "push", "--quiet", "origin", "feature")
	r.WriteFile(t, "local-only_req.md", "# Requirements\n")
	r.WriteFile(t, "local-only_logs.md", "# Log\n\n"+marker+"\n")
	p := &project{Repo: r, runner: healthyRunner(t), goos: "windows"}
	p.writeConfig(t, "")
	return p
}

func (p *project) writeConfig(t *testing.T, extra string) {
	p.WriteFile(t, "local-only_twokey.toml", `
[project]
branch = "feature"
`+extra+`
[checkpoint]
label = "Milestone 1"
requirements_file = "local-only_req.md"
[verify]
commands = ["go test ./..."]
`)
}

func (p *project) doctor(t *testing.T) (string, int) {
	var out bytes.Buffer
	code := Run(t.Context(), Options{
		Dir:         p.Dir,
		Runner:      p.runner,
		Out:         &out,
		StagingRoot: filepath.Join(t.TempDir(), "staging"),
		ScratchRoot: filepath.Join(t.TempDir(), "scratch"),
		GOOS:        p.goos,
	})
	return out.String(), code
}

func TestHealthyProject(t *testing.T) {
	out, code := newProject(t).doctor(t)
	if code != 0 || strings.Contains(out, "[FAIL]") || strings.Contains(out, "[WARN]") {
		t.Fatalf("exit %d, want a clean pass:\n%s", code, out)
	}
	for _, want := range []string{
		"[OK]    Codex CLI codex-cli 0.165.0",
		"[OK]    codex exec has every option twokey uses",
		"[OK]    codex exec resume has every option twokey uses",
		"[OK]    Claude Code 2.1.311 (/fake/claude)",
		"[OK]    claude has every option twokey uses",
		`[OK]    Claude permission mode "auto" is supported`,
		"[OK]    On branch feature",
		"[OK]    The working tree is clean",
		"[OK]    HEAD matches origin/feature after git fetch",
		"[OK]    Protected branches on origin: main",
		"[OK]    Codex's sandbox can run git ls-remote",
		"[OK]    Codex's sandbox can run docker",
		"[OK]    Log file local-only_logs.md: marker on line 3, UTF-8 without BOM, LF line endings",
		"[OK]    git ignores local-only_twokey.toml",
		"[OK]    git ignores local-only_twokey",
		"[OK]    Docker is running (server 28.0.1)",
		"All checks passed.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestProblems(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, p *project)
		want  string
		code  int
	}{
		{
			name:  "wrong branch",
			setup: func(t *testing.T, p *project) { testutil.Git(t, p.Dir, "checkout", "--quiet", "main") },
			want:  "[FAIL]  The repository is on main, but [project] branch is feature.",
			code:  1,
		},
		{
			name:  "dirty tree",
			setup: func(t *testing.T, p *project) { p.WriteFile(t, "notes.txt", "x") },
			want:  "[FAIL]  The working tree has 1 changed file that git doesn't ignore:",
			code:  1,
		},
		{
			name: "ahead of origin",
			setup: func(t *testing.T, p *project) {
				p.WriteFile(t, "a.txt", "a")
				p.Commit(t, "local only")
			},
			want: "[FAIL]  Your branch is 1 commit ahead of origin/feature.",
			code: 1,
		},
		{
			name: "behind origin",
			setup: func(t *testing.T, p *project) {
				p.WriteFile(t, "a.txt", "a")
				p.Commit(t, "pushed")
				testutil.Git(t, p.Dir, "push", "--quiet", "origin", "feature")
				testutil.Git(t, p.Dir, "reset", "--quiet", "--hard", "HEAD~1")
			},
			want: "[FAIL]  Your branch is 1 commit behind origin/feature.",
			code: 1,
		},
		{
			name: "branch not pushed",
			setup: func(t *testing.T, p *project) {
				testutil.Git(t, p.Dir, "checkout", "--quiet", "-b", "fresh")
				p.WriteFile(t, "local-only_twokey.toml", strings.Replace(readFile(t, p, "local-only_twokey.toml"), `"feature"`, `"fresh"`, 1))
			},
			want: "[FAIL]  origin has no branch fresh yet.",
			code: 1,
		},
		{
			name:  "upstream ahead",
			setup: func(t *testing.T, p *project) { p.writeConfig(t, "upstream = \"main\"\n") },
			want:  "[OK]    origin/feature is 1 commit ahead of origin/main.",
			code:  0,
		},
		{
			name: "upstream behind",
			setup: func(t *testing.T, p *project) {
				p.writeConfig(t, "upstream = \"main\"\n")
				dir := p.Clone(t)
				testutil.Git(t, dir, "merge", "--quiet", "--no-ff", "--no-edit", "origin/feature")
				testutil.Git(t, dir, "push", "--quiet", "origin", "main")
			},
			want: "[OK]    origin/feature is 1 commit behind origin/main. twokey fast-forwards it when a round starts.",
			code: 0,
		},
		{
			name: "upstream diverged",
			setup: func(t *testing.T, p *project) {
				p.writeConfig(t, "upstream = \"main\"\n")
				dir := p.Clone(t)
				testutil.Git(t, dir, "commit", "--quiet", "--allow-empty", "-m", "teammate")
				testutil.Git(t, dir, "push", "--quiet", "origin", "main")
			},
			want: "[WARN]  origin/feature and origin/main have diverged",
			code: 0,
		},
		{
			name:  "upstream missing",
			setup: func(t *testing.T, p *project) { p.writeConfig(t, "upstream = \"nope\"\n") },
			want:  "[FAIL]  origin/nope doesn't exist, but [project] upstream names it.",
			code:  1,
		},
		{
			name:  "no config",
			setup: func(t *testing.T, p *project) { removeFile(t, p, "local-only_twokey.toml") },
			want:  "[FAIL]  No config file:",
			code:  1,
		},
		{
			name:  "invalid config",
			setup: func(t *testing.T, p *project) { p.writeConfig(t, "remote = \"\"\n") },
			want:  `- [project] remote must be a remote name such as "origin" (got "").`,
			code:  1,
		},
		{
			name:  "missing requirements",
			setup: func(t *testing.T, p *project) { removeFile(t, p, "local-only_req.md") },
			want:  "[FAIL]  The requirements file is missing: local-only_req.md",
			code:  1,
		},
		{
			name:  "missing marker",
			setup: func(t *testing.T, p *project) { p.WriteFile(t, "local-only_logs.md", "# Log\n") },
			want:  "[FAIL]  The marker line is missing in local-only_logs.md.",
			code:  1,
		},
		{
			name:  "duplicate marker",
			setup: func(t *testing.T, p *project) { p.WriteFile(t, "local-only_logs.md", marker+"\n\n"+marker+"\n") },
			want:  "[FAIL]  The marker line appears 2 times in local-only_logs.md (lines 1, 3). It must appear once.",
			code:  1,
		},
		{
			name: "run dir not ignored",
			setup: func(t *testing.T, p *project) {
				p.WriteFile(t, ".gitignore", "local-only_twokey.toml\nlocal-only_req.md\nlocal-only_logs.md\n")
				p.Commit(t, "narrow ignore rules")
				testutil.Git(t, p.Dir, "push", "--quiet", "origin", "feature")
			},
			want: "[FAIL]  git does not ignore local-only_twokey. It must never be committed.",
			code: 1,
		},
		{
			name:  "lock exists",
			setup: func(t *testing.T, p *project) { p.WriteFile(t, "local-only_twokey/lock", "123") },
			want:  "[FAIL]  A twokey lock file exists:",
			code:  1,
		},
		{
			name: "extra read dir missing",
			setup: func(t *testing.T, p *project) {
				p.writeConfig(t, "extra_read_dirs = ['"+filepath.Join(t.TempDir(), "gone")+"']\n")
			},
			want: "[FAIL]  Extra read folder not found:",
			code: 1,
		},
		{
			name: "codex lacks a flag",
			setup: func(t *testing.T, p *project) {
				help := strings.ReplaceAll(fixture(t, "codex-0.165.0-exec-help.txt"), "--add-dir", "--add-folder")
				p.runner.runs["/fake/codex exec --help"] = ok(help)
			},
			want: "[FAIL]  codex exec lacks options twokey needs: --add-dir",
			code: 1,
		},
		{
			name:  "codex missing",
			setup: func(t *testing.T, p *project) { delete(p.runner.paths, "codex") },
			want:  `[FAIL]  Codex CLI ("codex") is not on PATH.`,
			code:  1,
		},
		{
			name: "unknown permission mode",
			setup: func(t *testing.T, p *project) {
				p.WriteFile(t, "local-only_twokey.toml", readFile(t, p, "local-only_twokey.toml")+"[claude]\npermission_mode = \"yolo\"\n")
			},
			want: `[FAIL]  Claude Code doesn't know the permission mode "yolo"`,
			code: 1,
		},
		{
			name: "docker not running is only a warning",
			setup: func(t *testing.T, p *project) {
				p.runner.runs["/fake/docker info --format {{.ServerVersion}}"] = proc.Result{ExitCode: 1, Err: fmt.Errorf("exit 1")}
			},
			want: "[WARN]  Docker is installed but not running.",
			code: 0,
		},
		{
			name: "codex sandbox blocks git and docker",
			setup: func(t *testing.T, p *project) {
				p.runner.sandbox = func(string) proc.Result {
					return proc.Result{ExitCode: 1, Stderr: "Access is denied.", Err: fmt.Errorf("exit 1")}
				}
			},
			want: "[WARN]  Codex can't run these in its sandbox: git ls-remote, docker.",
			code: 0,
		},
		{
			name:  "docker missing is only a warning",
			setup: func(t *testing.T, p *project) { delete(p.runner.paths, "docker") },
			want:  "[WARN]  Docker is not installed, or not on PATH.",
			code:  0,
		},
		{
			name:  "toast off Windows",
			setup: func(t *testing.T, p *project) { p.goos = "linux" },
			want:  "[WARN]  Toast notifications work only on Windows.",
			code:  0,
		},
		{
			name: "empty verify commands",
			setup: func(t *testing.T, p *project) {
				p.WriteFile(t, "local-only_twokey.toml", strings.Replace(readFile(t, p, "local-only_twokey.toml"), `["go test ./..."]`, "[]", 1))
			},
			want: "[WARN]  [verify] commands is empty.",
			code: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t)
			c.setup(t, p)
			out, code := p.doctor(t)
			if !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
			if code != c.code {
				t.Errorf("exit code %d, want %d:\n%s", code, c.code, out)
			}
		})
	}
}

func TestNotARepo(t *testing.T) {
	testutil.IsolateGit(t)
	var out bytes.Buffer
	code := Run(t.Context(), Options{
		Dir: t.TempDir(), Runner: healthyRunner(t), Out: &out,
		StagingRoot: t.TempDir(), ScratchRoot: t.TempDir(), GOOS: "windows",
	})
	if code != 1 || !strings.Contains(out.String(), "[FAIL]  This folder is not inside a git repository.") {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
	// The CLIs are still checked without a config.
	if !strings.Contains(out.String(), "[OK]    claude has every option twokey uses") {
		t.Errorf("CLI checks skipped:\n%s", out.String())
	}
}

func readFile(t *testing.T, p *project, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.Dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func removeFile(t *testing.T, p *project, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(p.Dir, rel)); err != nil {
		t.Fatal(err)
	}
}
