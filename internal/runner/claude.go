package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
)

// ClaudeParams are the per-step parts of a claude -p command line.
type ClaudeParams struct {
	Schema     string // the minified schema: --json-schema takes inline JSON only
	SessionID  string // a new session's ID (D8)
	Resume     string // or the session to resume
	ScratchDir string
	ReadDirs   []string // [project] extra_read_dirs (D11)
}

// ClaudeArgs builds the arguments of claude -p (SPEC section 9). Options
// that take several values are written as --add-dir=<folder>, so they
// can't swallow the next argument (D8). The prompt comes on stdin.
func ClaudeArgs(c config.Claude, p ClaudeParams) []string {
	args := []string{"-p", "--output-format", "json", "--json-schema", p.Schema,
		"--permission-mode", c.PermissionMode, "--permission-prompts", "none"}
	if p.Resume != "" {
		args = append(args, "--resume", p.Resume)
	} else {
		args = append(args, "--session-id", p.SessionID)
	}
	args = append(args, "--add-dir="+p.ScratchDir)
	for _, d := range p.ReadDirs {
		args = append(args, "--add-dir="+d)
	}
	if !c.DesktopTools {
		args = append(args, "--no-chrome")
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(c.MaxBudgetUSD, 'f', -1, 64))
	}
	return append(args, c.ExtraArgs...)
}

// ClaudeResult is the JSON object claude -p --output-format json prints.
type ClaudeResult struct {
	Subtype   string   `json:"subtype"`
	IsError   bool     `json:"is_error"`
	Result    string   `json:"result"`
	SessionID string   `json:"session_id"`
	CostUSD   float64  `json:"total_cost_usd"`
	Errors    []string `json:"errors"`
	Usage     struct {
		InputTokens   int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		OutputTokens  int64 `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		CostUSD       float64 `json:"costUSD"`
		InputTokens   int64   `json:"inputTokens"`
		CacheRead     int64   `json:"cacheReadInputTokens"`
		CacheCreation int64   `json:"cacheCreationInputTokens"`
		OutputTokens  int64   `json:"outputTokens"`
	} `json:"modelUsage"`
	PermissionDenials []struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	} `json:"permission_denials"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// Model returns the model that did most of the work: the modelUsage entry
// with the highest cost, then the most output tokens. Claude Code may use a
// small model for side tasks.
func (c ClaudeResult) Model() string {
	names := make([]string, 0, len(c.ModelUsage))
	for n := range c.ModelUsage {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := c.ModelUsage[names[i]], c.ModelUsage[names[j]]
		if a.CostUSD != b.CostUSD {
			return a.CostUSD > b.CostUSD
		}
		if a.OutputTokens != b.OutputTokens {
			return a.OutputTokens > b.OutputTokens
		}
		return names[i] < names[j]
	})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// Totals returns the tokens and cost Claude reports. usage covers this call
// alone. modelUsage and total_cost_usd cover the session: on a resume,
// Claude usually restores the session's earlier totals and adds this call
// to them (D53). Input counts cached tokens too.
func (c ClaudeResult) Totals() Usage {
	call := c.Usage.InputTokens + c.Usage.CacheCreation + c.Usage.CacheRead
	u := Usage{InputTokens: call, OutputTokens: c.Usage.OutputTokens, CostUSD: c.CostUSD, CallInputTokens: call}
	if len(c.ModelUsage) > 0 {
		u.InputTokens, u.OutputTokens = 0, 0
		for _, m := range c.ModelUsage {
			u.InputTokens += m.InputTokens + m.CacheRead + m.CacheCreation
			u.OutputTokens += m.OutputTokens
		}
	}
	return u
}

// Denials lists the permission denials as "<tool>: <what>".
func (c ClaudeResult) Denials() []string {
	var out []string
	for _, d := range c.PermissionDenials {
		what := ""
		for _, k := range []string{"command", "file_path", "path", "url", "pattern"} {
			if v, ok := d.ToolInput[k].(string); ok && v != "" {
				what = v
				break
			}
		}
		if what == "" {
			b, _ := json.Marshal(d.ToolInput)
			what = string(b)
		}
		if r := []rune(what); len(r) > 200 {
			what = string(r[:197]) + "..."
		}
		out = append(out, d.ToolName+": "+what)
	}
	return out
}

// Answer returns the structured answer; nil if there is none. The docs say
// to treat a run as failed when is_error is true or structured_output is
// missing.
func (c ClaudeResult) Answer() []byte {
	if c.IsError || len(c.StructuredOutput) == 0 || bytes.Equal(bytes.TrimSpace(c.StructuredOutput), []byte("null")) {
		return nil
	}
	return c.StructuredOutput
}

// claude runs one Claude step.
func (x *Real) claude(ctx context.Context, req Request) Result {
	c := x.Cfg.Claude
	res := Result{SessionID: req.SessionID}
	if req.Resume != "" {
		res.SessionID = req.Resume
	}
	sch, err := schema.Minified(req.Step)
	if err != nil {
		res.Problem = err.Error()
		return res
	}
	p := ClaudeParams{Schema: sch, SessionID: req.SessionID, Resume: req.Resume,
		ScratchDir: req.ScratchDir, ReadDirs: x.Cfg.Project.ExtraReadDirs}
	stdout, stderr := req.OutBase+".stdout.json", req.OutBase+".stderr.log"
	tr := x.Launch(ctx, proc.TreeCmd{Dir: req.RepoDir, Name: x.claudePath(), Args: ClaudeArgs(c, p),
		Stdin: req.Prompt, Stdout: stdout, Stderr: stderr})
	if tr.Killed {
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		res.Problem = "Claude was stopped: " + ctx.Err().Error()
		return res
	}
	data, _ := os.ReadFile(stdout)
	var out ClaudeResult
	if err := json.Unmarshal(bytes.TrimSpace(data), &out); err != nil {
		text := tail(stderr)
		if res.Limit = FindLimit(string(data)+"\n"+text, x.Cfg.Limits.UsageLimitPatterns, x.now()); res.Limit != nil {
			res.Problem = "Claude hit a limit: " + res.Limit.Message
			return res
		}
		res.Problem = "Claude printed no result: " + firstNonEmpty(nil, tr.Err, text)
		return res
	}
	if out.SessionID != "" {
		res.SessionID = out.SessionID
	}
	res.Model, res.Denials, res.Usage = out.Model(), out.Denials(), out.Totals()
	if res.Answer = out.Answer(); res.Answer != nil {
		return res
	}
	text := strings.Join(append([]string{out.Result}, out.Errors...), "\n")
	if out.IsError || tr.Err != nil {
		if res.Limit = FindLimit(text+"\n"+tail(stderr), x.Cfg.Limits.UsageLimitPatterns, x.now()); res.Limit != nil {
			res.Problem = "Claude hit a limit: " + res.Limit.Message
			return res
		}
	}
	res.Problem = fmt.Sprintf("Claude gave no structured answer (%s): %s", out.Subtype, firstNonEmpty(append([]string{out.Result}, out.Errors...), tr.Err, tail(stderr)))
	return res
}
