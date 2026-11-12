package doctor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
)

var (
	createRe = regexp.MustCompile(`(?i)create the file (.+?) containing`)
	readRe   = regexp.MustCompile(`(?m)^   - (.+)$`)
)

// fakeAgents plays both CLIs for the smoke test: they do what the prompt
// asks (unless broken says otherwise) and answer in each CLI's format.
func fakeAgents(t *testing.T, broken string) func(context.Context, proc.TreeCmd) proc.TreeResult {
	return func(_ context.Context, c proc.TreeCmd) proc.TreeResult {
		agent := "claude"
		if strings.Contains(c.Name, "codex") {
			agent = "codex"
		}
		if m := createRe.FindStringSubmatch(c.Stdin); m != nil && broken != agent+" can't write" {
			if err := os.WriteFile(m[1], []byte("ok"), 0o600); err != nil {
				t.Error(err)
			}
		}
		ans := schema.SmokeResult{Wrote: true, HTTPStatus: 200, Reads: []schema.SmokeRead{}}
		for _, m := range readRe.FindAllStringSubmatch(c.Stdin, -1) {
			b, _ := os.ReadFile(strings.TrimSpace(m[1]))
			ans.Reads = append(ans.Reads, schema.SmokeRead{Path: strings.TrimSpace(m[1]), FirstBytesHex: hex.EncodeToString(b[:4])})
		}
		answer, _ := json.Marshal(ans)
		var stdout string
		if agent == "codex" {
			stdout = `{"type":"thread.started","thread_id":"th-1"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}` + "\n"
			for i, a := range c.Args {
				if a == "-o" {
					_ = os.WriteFile(c.Args[i+1], answer, 0o600)
				}
			}
		} else {
			session := ""
			for i, a := range c.Args {
				if a == "--session-id" || a == "--resume" {
					session = c.Args[i+1]
				}
			}
			stdout = fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"total_cost_usd":0.02,`+
				`"modelUsage":{"claude-opus-5-5":{"costUSD":0.02,"outputTokens":5}},"structured_output":%s}`, session, answer)
		}
		_ = os.WriteFile(c.Stdout, []byte(stdout), 0o600)
		_ = os.WriteFile(c.Stderr, nil, 0o600)
		return proc.TreeResult{}
	}
}

func smokeProject(t *testing.T, broken string) (string, int) {
	p := newProject(t)
	slides := t.TempDir()
	if err := os.WriteFile(filepath.Join(slides, "week1.png"), []byte("\x89PNG\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.writeConfig(t, "extra_read_dirs = ['"+slides+"']\n")
	args := []string{"mcp", "list", "--json"}
	for _, o := range runner.CodexPluginsOff() {
		args = append(args, "-c", o)
	}
	p.runner.runs["/fake/codex "+strings.Join(args, " ")] = ok(`[{"name":"node_repl","enabled":true}]`)

	var out strings.Builder
	code := Run(t.Context(), Options{Dir: p.Dir, Runner: p.runner, Out: &out, GOOS: "windows", Smoke: true,
		StagingRoot: t.TempDir(), ScratchRoot: t.TempDir(), Launch: fakeAgents(t, broken)})
	return out.String(), code
}

func TestSmoke(t *testing.T) {
	out, code := smokeProject(t, "")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"[OK]    Codex answered with JSON that fits the schema",
		"[OK]    Codex wrote a file to its scratch folder",
		"[OK]    Codex reached the network (HTTP 200)",
		"[OK]    Codex can read ",
		`[OK]    Codex doesn't report its model, so log headings use [codex] label: "Codex - GPT-6.1-Sol"`,
		"[OK]    Codex accepted the options that turn off screen and browser control",
		"[OK]    A resumed Codex session keeps its sandbox",
		"[OK]    Claude Code answered with JSON that fits the schema",
		"[OK]    Claude Code wrote a file to its scratch folder",
		"[OK]    Claude Code can read ",
		`[OK]    Claude Code ran claude-opus-5-5. Log headings: "Claude Code - Claude Opus 5.5"`,
		"[OK]    A resumed Claude Code session keeps its sandbox",
		"Estimated cost of this call: $0.02",
		"[OK]    The repository is unchanged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestSmokeFindsABrokenSandbox(t *testing.T) {
	out, code := smokeProject(t, "codex can't write")
	if code != 1 || !strings.Contains(out, `[FAIL]  Codex couldn't write to its scratch folder with [codex] sandbox = "workspace-write".`) {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// Without the CLIs there is no smoke test, and no call.
func TestSmokeNeedsTheCLIs(t *testing.T) {
	p := newProject(t)
	delete(p.runner.paths, "claude")
	var out strings.Builder
	code := Run(t.Context(), Options{Dir: p.Dir, Runner: p.runner, Out: &out, GOOS: "windows", Smoke: true,
		StagingRoot: t.TempDir(), ScratchRoot: t.TempDir(),
		Launch: func(context.Context, proc.TreeCmd) proc.TreeResult {
			t.Error("no call expected")
			return proc.TreeResult{}
		}})
	if code != 1 || !strings.Contains(out.String(), "The smoke test needs both agent CLIs.") {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}
