package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
)

// CodexParams are the per-step parts of a codex exec command line.
type CodexParams struct {
	SchemaFile string
	AnswerFile string // -o: the final message
	RepoDir    string
	ScratchDir string
	Resume     string   // the session to resume; "" for a new one
	Off        []string // -c overrides that turn off screen and browser control
}

// CodexArgs builds the arguments of codex exec (SPEC section 9). A new
// session gets --sandbox, --add-dir and -C; exec resume has none of those,
// so a resumed one gets the same sandbox through -c and runs in the
// repository folder (D5). The prompt comes on stdin ("-", D6).
func CodexArgs(c config.Codex, p CodexParams) []string {
	args := []string{"exec"}
	if p.Resume != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--output-schema", p.SchemaFile, "-o", p.AnswerFile)
	if p.Resume == "" {
		args = append(args, "--sandbox", c.Sandbox, "--add-dir", p.ScratchDir)
	} else {
		args = append(args, "-c", "sandbox_mode="+tomlString(c.Sandbox),
			"-c", "sandbox_workspace_write.writable_roots=["+tomlString(p.ScratchDir)+"]")
	}
	args = append(args, "-c", "notify=[]")
	if c.Sandbox == "workspace-write" && c.Network {
		args = append(args, "-c", "sandbox_workspace_write.network_access=true")
	}
	if c.WebSearch != "" {
		args = append(args, "-c", "web_search="+tomlString(c.WebSearch))
	}
	for _, o := range p.Off {
		args = append(args, "-c", o)
	}
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	args = append(args, c.ExtraArgs...)
	if p.Resume == "" {
		return append(args, "-C", p.RepoDir, "-")
	}
	return append(args, p.Resume, "-")
}

// The bundled Codex plugins that control the screen or the browser, and the
// MCP servers that do (D43). Plugin overrides are harmless when the plugin
// isn't installed; an MCP server override is not, so it is added only for
// servers that "codex mcp list" shows.
var (
	codexDesktopPlugins = []string{"browser@openai-bundled", "computer-use@openai-bundled", "unified-computer-use@openai-bundled"}
	codexDesktopServers = []string{"node_repl", "cua_repl"}
)

// CodexPluginsOff are the -c overrides that turn the desktop plugins off.
// Codex splits -c keys on dots and doesn't strip quotes, so the plugin IDs
// are written without quotes.
func CodexPluginsOff() []string {
	var off []string
	for _, p := range codexDesktopPlugins {
		off = append(off, "plugins."+p+".enabled=false")
	}
	return off
}

// CodexServersOff reads "codex mcp list --json" (run with CodexPluginsOff)
// and returns the overrides that turn off the desktop MCP servers it shows
// as enabled.
func CodexServersOff(list []byte) ([]string, error) {
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(list, &servers); err != nil {
		return nil, fmt.Errorf("can't read codex mcp list --json: %w", err)
	}
	var off []string
	for _, s := range servers {
		for _, d := range codexDesktopServers {
			if s.Enabled && s.Name == d {
				off = append(off, "mcp_servers."+s.Name+".enabled=false")
			}
		}
	}
	return off, nil
}

// CodexEvents is what twokey reads from codex exec --json.
type CodexEvents struct {
	ThreadID string
	Failed   bool     // a turn.failed event
	Errors   []string // the messages of turn.failed and error events
	Usage    Usage
	Declined []string // commands the sandbox or approval policy refused
	Reroute  string   // the model Codex switched to, if it did
}

// ParseCodexEvents reads the JSONL event stream. Lines it can't read are
// skipped.
func ParseCodexEvents(r io.Reader) CodexEvents {
	var ev CodexEvents
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			ev.add(line)
		}
		if err != nil {
			return ev
		}
	}
}

func (ev *CodexEvents) add(line []byte) {
	var e struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Item *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "thread.started":
		ev.ThreadID = e.ThreadID
	case "turn.completed":
		if e.Usage != nil {
			ev.Usage.InputTokens += e.Usage.InputTokens
			ev.Usage.OutputTokens += e.Usage.OutputTokens
		}
	case "turn.failed":
		ev.Failed = true
		if e.Error != nil {
			ev.Errors = append(ev.Errors, e.Error.Message)
		}
	case "error":
		ev.Errors = append(ev.Errors, e.Message)
	case "item.completed":
		if e.Item == nil {
			return
		}
		switch {
		case e.Item.Type == "command_execution" && e.Item.Status == "declined":
			ev.Declined = append(ev.Declined, "command: "+e.Item.Command)
		case e.Item.Type == "error" && strings.HasPrefix(e.Item.Message, "model rerouted: "):
			if _, to, ok := strings.Cut(e.Item.Message, " -> "); ok {
				ev.Reroute, _, _ = strings.Cut(to, " ")
			}
		}
	}
}

// codex runs one Codex step.
func (x *Real) codex(ctx context.Context, req Request) Result {
	c := x.Cfg.Codex
	res := Result{Model: c.Model}
	raw, err := schema.Raw(req.Step)
	if err != nil {
		res.Problem = err.Error()
		return res
	}
	p := CodexParams{SchemaFile: req.OutBase + ".schema.json", AnswerFile: req.OutBase + ".last.json",
		RepoDir: req.RepoDir, ScratchDir: req.ScratchDir, Resume: req.Resume}
	if err := os.WriteFile(p.SchemaFile, raw, 0o600); err != nil {
		res.Problem = "can't write the schema file: " + err.Error()
		return res
	}
	_ = os.Remove(p.AnswerFile)
	if !c.DesktopTools {
		if p.Off, err = x.codexOff(ctx); err != nil {
			res.Problem = err.Error()
			return res
		}
	}
	events, stderr := req.OutBase+".events.jsonl", req.OutBase+".stderr.log"
	tr := x.Launch(ctx, proc.TreeCmd{Dir: req.RepoDir, Name: x.codexPath(), Args: CodexArgs(c, p),
		Stdin: req.Prompt, Stdout: events, Stderr: stderr})

	var ev CodexEvents
	if f, err := os.Open(events); err == nil {
		ev = ParseCodexEvents(f)
		_ = f.Close()
	}
	res.SessionID, res.Usage, res.Denials = ev.ThreadID, ev.Usage, ev.Declined
	if ev.Reroute != "" {
		res.Notes = append(res.Notes, fmt.Sprintf("Codex rerouted the step to the model %s.", ev.Reroute))
		res.Model = ev.Reroute
	}
	if tr.Killed {
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		res.Problem = "Codex was stopped: " + ctx.Err().Error()
		return res
	}
	if ev.Failed || tr.Err != nil {
		text := strings.Join(append(ev.Errors, tail(stderr)), "\n")
		if res.Limit = FindLimit(text, x.Cfg.Limits.UsageLimitPatterns, x.now()); res.Limit != nil {
			res.Problem = "Codex hit a limit: " + res.Limit.Message
			return res
		}
		res.Problem = "Codex failed: " + firstNonEmpty(ev.Errors, tr.Err, tail(stderr))
		return res
	}
	answer, err := os.ReadFile(p.AnswerFile)
	if err != nil || len(bytes.TrimSpace(answer)) == 0 {
		res.Problem = "Codex finished without a final answer"
		return res
	}
	res.Answer = answer
	return res
}
