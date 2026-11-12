package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// The output fixtures follow the documented formats (D43): codex exec
// --json events from the source at tag rust-v0.165.0, and Claude Code's
// result object from its headless and Agent SDK docs. doctor --smoke checks
// them against the real CLIs.

func TestCodexArgs(t *testing.T) {
	c := config.Default().Codex
	p := CodexParams{SchemaFile: "s.json", AnswerFile: "a.json", RepoDir: `C:\repo`, ScratchDir: `C:\tmp\scratch`,
		Off: []string{"plugins.browser@openai-bundled.enabled=false"}}
	want := []string{"exec", "--json", "--output-schema", "s.json", "-o", "a.json", "--sandbox", "workspace-write",
		"--add-dir", `C:\tmp\scratch`, "-c", "notify=[]", "-c", "sandbox_workspace_write.network_access=true",
		"-c", "plugins.browser@openai-bundled.enabled=false", "-C", `C:\repo`, "-"}
	if got := CodexArgs(c, p); !reflect.DeepEqual(got, want) {
		t.Errorf("new session:\n got %q\nwant %q", got, want)
	}

	c.Model, c.Network, c.ExtraArgs = "gpt-x", false, []string{"--strict-config"}
	p.Resume, p.Off = "thread-1", nil
	want = []string{"exec", "resume", "--json", "--output-schema", "s.json", "-o", "a.json",
		"-c", "sandbox_mode='workspace-write'", "-c", `sandbox_workspace_write.writable_roots=['C:\tmp\scratch']`,
		"-c", "notify=[]", "-m", "gpt-x", "--strict-config", "thread-1", "-"}
	if got := CodexArgs(c, p); !reflect.DeepEqual(got, want) {
		t.Errorf("resume:\n got %q\nwant %q", got, want)
	}
}

func TestClaudeArgs(t *testing.T) {
	c := config.Default().Claude
	p := ClaudeParams{Schema: `{"type":"object"}`, SessionID: "uuid-1", ScratchDir: `C:\tmp\s`, ReadDirs: []string{`D:\slides`}}
	want := []string{"-p", "--output-format", "json", "--json-schema", `{"type":"object"}`, "--permission-mode", "auto",
		"--permission-prompts", "none", "--session-id", "uuid-1", `--add-dir=C:\tmp\s`, `--add-dir=D:\slides`, "--no-chrome"}
	if got := ClaudeArgs(c, p); !reflect.DeepEqual(got, want) {
		t.Errorf("new session:\n got %q\nwant %q", got, want)
	}
	c.DesktopTools, c.Model, c.MaxBudgetUSD = true, "opus", 2.5
	p.Resume, p.ReadDirs = "uuid-1", nil
	want = []string{"-p", "--output-format", "json", "--json-schema", `{"type":"object"}`, "--permission-mode", "auto",
		"--permission-prompts", "none", "--resume", "uuid-1", `--add-dir=C:\tmp\s`, "--model", "opus", "--max-budget-usd", "2.5"}
	if got := ClaudeArgs(c, p); !reflect.DeepEqual(got, want) {
		t.Errorf("resume:\n got %q\nwant %q", got, want)
	}
}

func TestTomlString(t *testing.T) {
	for in, want := range map[string]string{`C:\a b`: `'C:\a b'`, `it's`: `"it's"`, `a"b'c\d`: `"a\"b'c\\d"`} {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
		}
	}
}

const codexEvents = `{"type":"thread.started","thread_id":"0199a213-81c0-7800-8aa1-bbab2a035a53"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"Looking"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"git push","aggregated_output":"","exit_code":null,"status":"declined"}}
{"type":"item.completed","item":{"id":"item_2","type":"error","message":"model rerouted: gpt-6.1-sol -> gpt-6.1-mini (load)"}}
not json
{"type":"error","message":"Reconnecting... 1/5"}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"{\"ok\":true}"}}
{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"output_tokens":122,"reasoning_output_tokens":64}}
`

func TestParseCodexEvents(t *testing.T) {
	ev := ParseCodexEvents(strings.NewReader(codexEvents))
	want := CodexEvents{ThreadID: "0199a213-81c0-7800-8aa1-bbab2a035a53", Errors: []string{"Reconnecting... 1/5"},
		Usage: Usage{InputTokens: 24763, OutputTokens: 122}, Declined: []string{"command: git push"}, Reroute: "gpt-6.1-mini"}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("got  %+v\nwant %+v", ev, want)
	}
	failed := ParseCodexEvents(strings.NewReader(`{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again later."}}`))
	if !failed.Failed || len(failed.Errors) != 1 {
		t.Errorf("turn.failed: %+v", failed)
	}
}

func TestCodexServersOff(t *testing.T) {
	list := `[{"name":"codex_app","enabled":false},{"name":"node_repl","enabled":true},{"name":"docs","enabled":true}]`
	off, err := CodexServersOff([]byte(list))
	if err != nil || !reflect.DeepEqual(off, []string{"mcp_servers.node_repl.enabled=false"}) {
		t.Errorf("got %v, %v", off, err)
	}
	if _, err := CodexServersOff([]byte("oops")); err == nil {
		t.Error("bad JSON must be an error")
	}
	if got := CodexPluginsOff(); len(got) != 3 || got[0] != "plugins.browser@openai-bundled.enabled=false" {
		t.Errorf("plugins: %v", got)
	}
}

const claudeOK = `{"type":"result","subtype":"success","is_error":false,"duration_ms":91234,"num_turns":12,
"result":"Done.","session_id":"6f1c-uuid","total_cost_usd":1.25,
"usage":{"input_tokens":100,"cache_creation_input_tokens":2000,"cache_read_input_tokens":30000,"output_tokens":4000},
"modelUsage":{"claude-haiku-5-5":{"costUSD":0.01,"inputTokens":10,"outputTokens":50},"claude-opus-5-5":{"costUSD":1.24,"inputTokens":90,"outputTokens":3950}},
"permission_denials":[{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"git push origin HEAD"}},{"tool_name":"WebFetch","tool_use_id":"t2","tool_input":{"url":"https://example.com","prompt":"x"}}],
"structured_output":{"ok":true}}`

func TestClaudeResult(t *testing.T) {
	var r ClaudeResult
	if err := jsonUnmarshal(claudeOK, &r); err != nil {
		t.Fatal(err)
	}
	if r.Model() != "claude-opus-5-5" || string(r.Answer()) != `{"ok":true}` {
		t.Errorf("model %q, answer %s", r.Model(), r.Answer())
	}
	if want := []string{"Bash: git push origin HEAD", "WebFetch: https://example.com"}; !reflect.DeepEqual(r.Denials(), want) {
		t.Errorf("denials %v", r.Denials())
	}
	r.IsError = true
	if r.Answer() != nil {
		t.Error("is_error means no answer")
	}
}

// fakeCLI plays a CLI run: it writes the files the CLI would write.
type fakeCLI struct {
	stdout, stderr, last string
	exit                 int
	block                bool // wait for ctx, like a hung agent
	got                  proc.TreeCmd
}

func (f *fakeCLI) launch(ctx context.Context, c proc.TreeCmd) proc.TreeResult {
	f.got = c
	_ = os.WriteFile(c.Stdout, []byte(f.stdout), 0o600)
	_ = os.WriteFile(c.Stderr, []byte(f.stderr), 0o600)
	if f.last != "" {
		for i, a := range c.Args {
			if a == "-o" {
				_ = os.WriteFile(c.Args[i+1], []byte(f.last), 0o600)
			}
		}
	}
	if f.block {
		<-ctx.Done()
		return proc.TreeResult{ExitCode: -1, Killed: true, Err: ctx.Err()}
	}
	if f.exit != 0 {
		return proc.TreeResult{ExitCode: f.exit, Err: errors.New("exit")}
	}
	return proc.TreeResult{}
}

type noProc struct{}

func (noProc) LookPath(file string) (string, error) { return "/fake/" + file, nil }
func (noProc) Run(context.Context, proc.Cmd) proc.Result {
	return proc.Result{Stdout: `[{"name":"node_repl","enabled":true}]`}
}

func run(t *testing.T, agent string, f *fakeCLI, timeout time.Duration) Result {
	t.Helper()
	cfg := config.Default()
	cfg.Project.ExtraReadDirs = []string{`D:\slides`}
	x := &Real{Cfg: cfg, Proc: noProc{}, Launch: f.launch, Now: func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.Local) }}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	dir := t.TempDir()
	return x.Run(ctx, Request{Agent: agent, Name: agent + "-audit", Step: "audit", Round: 1, Attempt: 1, Prompt: "the prompt",
		RepoDir: dir, ScratchDir: filepath.Join(dir, "scratch"), SessionID: "uuid-1", OutBase: filepath.Join(dir, "r1-x.1")})
}

func TestRealCodex(t *testing.T) {
	f := &fakeCLI{stdout: codexEvents, last: `{"ok":true}`}
	r := run(t, "codex", f, time.Minute)
	if r.Problem != "" || string(r.Answer) != `{"ok":true}` || r.SessionID != "0199a213-81c0-7800-8aa1-bbab2a035a53" {
		t.Fatalf("result %+v", r)
	}
	if r.Model != "gpt-6.1-mini" || len(r.Notes) != 1 || r.Denials[0] != "command: git push" {
		t.Errorf("model %q, notes %v, denials %v", r.Model, r.Notes, r.Denials)
	}
	if f.got.Stdin != "the prompt" || f.got.Name != "/fake/codex" || !strings.Contains(strings.Join(f.got.Args, " "), "-c mcp_servers.node_repl.enabled=false") {
		t.Errorf("command %+v", f.got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.got.Stdout), "r1-x.1.schema.json")); err != nil {
		t.Error("the schema file is missing")
	}
}

func TestRealCodexFailures(t *testing.T) {
	limit := run(t, "codex", &fakeCLI{stdout: `{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again at 3:45 PM."}}`, exit: 1}, time.Minute)
	if limit.Limit == nil || limit.Limit.Reset.Hour() != 15 || limit.Answer != nil {
		t.Errorf("limit: %+v", limit)
	}
	failed := run(t, "codex", &fakeCLI{stdout: `{"type":"turn.failed","error":{"message":"stream disconnected"}}`, exit: 1}, time.Minute)
	if failed.Limit != nil || failed.Problem != "Codex failed: stream disconnected" {
		t.Errorf("failed: %+v", failed)
	}
	empty := run(t, "codex", &fakeCLI{stdout: `{"type":"turn.completed"}`}, time.Minute)
	if empty.Problem != "Codex finished without a final answer" {
		t.Errorf("no answer: %+v", empty)
	}
	slow := run(t, "codex", &fakeCLI{block: true}, 200*time.Millisecond)
	if !slow.TimedOut || slow.Answer != nil {
		t.Errorf("timeout: %+v", slow)
	}
}

func TestRealClaude(t *testing.T) {
	f := &fakeCLI{stdout: claudeOK}
	r := run(t, "claude", f, time.Minute)
	if r.Problem != "" || string(r.Answer) != `{"ok":true}` || r.SessionID != "6f1c-uuid" || r.Model != "claude-opus-5-5" {
		t.Fatalf("result %+v", r)
	}
	if r.Usage.CostUSD != 1.25 || r.Usage.OutputTokens != 4000 || r.Usage.InputTokens != 32100 || len(r.Denials) != 2 {
		t.Errorf("usage %+v, denials %v", r.Usage, r.Denials)
	}
	args := strings.Join(f.got.Args, " ")
	if !strings.Contains(args, "--session-id uuid-1") || !strings.Contains(args, `--add-dir=D:\slides`) || f.got.Name != "/fake/claude" {
		t.Errorf("command %q", args)
	}
}

func TestRealClaudeFailures(t *testing.T) {
	limit := run(t, "claude", &fakeCLI{exit: 1, stdout: `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit · resets 3:45pm","session_id":"s"}`}, time.Minute)
	if limit.Limit == nil || limit.Limit.Reset.Minute() != 45 {
		t.Errorf("limit: %+v", limit)
	}
	noSchema := run(t, "claude", &fakeCLI{exit: 1, stdout: `{"type":"result","subtype":"error_max_structured_output_retries","is_error":true,"errors":["no valid output"],"session_id":"s"}`}, time.Minute)
	if noSchema.Limit != nil || noSchema.Problem != "Claude gave no structured answer (error_max_structured_output_retries): no valid output" {
		t.Errorf("no answer: %+v", noSchema)
	}
	garbage := run(t, "claude", &fakeCLI{exit: 1, stderr: "Error: --json-schema is not a valid JSON Schema"}, time.Minute)
	if garbage.Problem != "Claude printed no result: Error: --json-schema is not a valid JSON Schema" {
		t.Errorf("no JSON: %+v", garbage)
	}
	slow := run(t, "claude", &fakeCLI{block: true}, 200*time.Millisecond)
	if !slow.TimedOut {
		t.Errorf("timeout: %+v", slow)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
