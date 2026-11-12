package runner

import (
	"context"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// Sandbox check results.
const (
	SandboxOK       = "ok"       // works inside Codex's sandbox
	SandboxLimited  = "limited"  // works outside, fails inside
	SandboxUntested = "untested" // fails outside too, so the sandbox isn't to blame
)

// SandboxResult is one command tried inside Codex's sandbox.
type SandboxResult struct {
	Name   string // "git ls-remote", "docker"
	Status string
	Detail string // why it failed, or why it wasn't tested
}

// CodexSandboxArgs builds "codex sandbox" arguments that run one command
// in the sandbox a Codex step gets, without a model call (D51).
func CodexSandboxArgs(c config.Codex, repo string, cmd []string) []string {
	args := []string{"sandbox", "-c", "sandbox_mode=" + tomlString(c.Sandbox), "-c", "notify=[]"}
	if c.Sandbox == "workspace-write" && c.Network {
		args = append(args, "-c", "sandbox_workspace_write.network_access=true")
	}
	args = append(args, "-C", repo, "--")
	return append(args, cmd...)
}

// sandboxEnv keeps git from waiting for a login prompt nobody can answer.
var sandboxEnv = []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}

// ProbeCodexSandbox tries the commands that Codex audits use and that may
// fail under the sandbox's restricted token: git ls-remote against the
// project's remote, and docker version. Each runs outside the sandbox
// first, so a failure is blamed on the sandbox only if it works outside.
// It returns nil when there is no sandbox to test (danger-full-access).
func ProbeCodexSandbox(ctx context.Context, r proc.Runner, cfg *config.Config, repo string) []SandboxResult {
	if cfg.Codex.Sandbox == "danger-full-access" {
		return nil
	}
	codex, err := r.LookPath(cfg.Codex.Command)
	if err != nil {
		return nil
	}
	find := func(name string) string {
		if p, err := r.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	checks := []struct {
		name string
		exe  string
		args []string
	}{
		{"git ls-remote", find("git"), []string{"ls-remote", "--heads", cfg.Project.Remote, "refs/heads/" + cfg.Project.Branch}},
		{"docker", find("docker"), []string{"version", "--format", "{{.Server.Version}}"}},
	}
	var out []SandboxResult
	for _, c := range checks {
		res := SandboxResult{Name: c.name, Status: SandboxUntested}
		if c.exe == "" {
			res.Detail = "it isn't installed"
			out = append(out, res)
			continue
		}
		run := func(name string, args []string) proc.Result {
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			return r.Run(cctx, proc.Cmd{Dir: repo, Name: name, Args: args, Env: sandboxEnv})
		}
		if o := run(c.exe, c.args); o.Err != nil {
			res.Detail = "it doesn't work outside the sandbox either: " + firstNonEmpty(nil, o.Err, lastLine(o.Stderr))
			out = append(out, res)
			continue
		}
		if in := run(codex, CodexSandboxArgs(cfg.Codex, repo, append([]string{c.exe}, c.args...))); in.Err != nil {
			res.Status, res.Detail = SandboxLimited, firstNonEmpty(nil, in.Err, lastLine(in.Stderr+"\n"+in.Stdout))
		} else {
			res.Status = SandboxOK
		}
		out = append(out, res)
	}
	return out
}

// Limited returns the results of the checks that fail only in the sandbox.
func Limited(results []SandboxResult) []SandboxResult {
	var out []SandboxResult
	for _, r := range results {
		if r.Status == SandboxLimited {
			out = append(out, r)
		}
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
