package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// Real runs the Codex and Claude CLIs (SPEC section 9).
type Real struct {
	Cfg    *config.Config
	Proc   proc.Runner                                         // finds the CLIs; runs codex mcp list
	Launch func(context.Context, proc.TreeCmd) proc.TreeResult // proc.RunTree, or a fake in tests
	Now    func() time.Time

	offOnce sync.Once
	off     []string
	offErr  error
}

// NewReal returns a runner for real agent steps.
func NewReal(cfg *config.Config, r proc.Runner) *Real {
	return &Real{Cfg: cfg, Proc: r, Launch: proc.RunTree, Now: time.Now}
}

// Run implements Runner.
func (x *Real) Run(ctx context.Context, req Request) Result {
	if req.Agent == "codex" {
		return x.codex(ctx, req)
	}
	return x.claude(ctx, req)
}

// CodexOff returns the -c overrides that turn off Codex's screen and
// browser control (D43). It asks codex mcp list once per run.
func (x *Real) codexOff(ctx context.Context) ([]string, error) {
	x.offOnce.Do(func() {
		off := CodexPluginsOff()
		args := []string{"mcp", "list", "--json"}
		for _, o := range off {
			args = append(args, "-c", o)
		}
		r := x.Proc.Run(ctx, proc.Cmd{Name: x.codexPath(), Args: args})
		if r.Err != nil {
			x.offErr = fmt.Errorf("can't list Codex's MCP servers, to turn off screen and browser control: %s", firstNonEmpty(nil, r.Err, r.Stderr))
			return
		}
		servers, err := CodexServersOff([]byte(r.Stdout))
		if err != nil {
			x.offErr = err
			return
		}
		x.off = append(off, servers...)
	})
	return x.off, x.offErr
}

func (x *Real) codexPath() string  { return x.find(x.Cfg.Codex.Command) }
func (x *Real) claudePath() string { return x.find(x.Cfg.Claude.Command) }

func (x *Real) find(command string) string {
	if p, err := x.Proc.LookPath(command); err == nil {
		return p
	}
	return command
}

func (x *Real) now() time.Time {
	if x.Now == nil {
		return time.Now()
	}
	return x.Now()
}

// tail returns the last lines of a file, for error messages.
func tail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// firstNonEmpty picks the first useful text for an error message.
func firstNonEmpty(texts []string, err error, fallback string) string {
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	if err != nil {
		return err.Error()
	}
	return "no details"
}

// tomlString quotes s as a TOML string for a -c override.
func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'" // a literal string: backslashes in Windows paths stay as they are
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}
