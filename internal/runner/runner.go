// Package runner runs one agent step. The interface is small, so another
// agent could be added later (SPEC section 2, D5). Real runs the Codex and
// Claude CLIs; Fake plays scripted answers for tests.
package runner

import (
	"context"
	"time"
)

// Request is one agent step to run.
type Request struct {
	Agent      string // "codex" or "claude"
	Name       string // the step's name in state.json, e.g. "codex-audit"
	Step       string // the schema step: audit, convergence, dispute, fix
	Round      int
	Attempt    int    // counts every try of the step, from 1
	Prompt     string // the whole prompt, sent on stdin
	RepoDir    string
	ScratchDir string
	Commit     string // the commit the step works on
	Resume     string // the session to resume; "" starts a fresh session
	// SessionID is the new session's ID when twokey picks it (Claude, D8),
	// so state.json holds it before the step starts.
	SessionID string
	// OutBase is where the CLI's raw output goes, outside the repository:
	// OutBase + ".stdout.json", ".events.jsonl", ".stderr.log" and so on.
	OutBase string
}

// Result is what the step produced.
type Result struct {
	Answer    []byte // the final JSON answer; nil if there is none
	SessionID string
	Model     string // the model that ran, if the CLI says or twokey chose it
	Problem   string // why there is no answer, if there is none
	TimedOut  bool   // the step ran out of time and was killed
	Limit     *Limit // the agent hit a usage or rate limit
	Usage     Usage
	// Denials are what the agent wasn't allowed to do: Claude's permission
	// denials, Codex's declined commands.
	Denials []string
	Notes   []string // for the report, e.g. a model reroute
}

// Limit is a usage or rate limit the agent hit.
type Limit struct {
	Message string
	Reset   time.Time // zero if the message doesn't say when the limit resets
}

// Usage is what a run of the CLI reports about its cost.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64 // Claude's estimate; Codex reports none
	// CallInputTokens is the input of this call alone, when the CLI also
	// reports totals that may include earlier calls (Claude, D53).
	CallInputTokens int64
}

// Runner runs agent steps.
type Runner interface {
	Run(ctx context.Context, req Request) Result
}
