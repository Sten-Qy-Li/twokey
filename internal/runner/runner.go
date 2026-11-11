// Package runner runs one agent step. The interface is small, so another
// agent could be added later (SPEC section 2, D5). The real Codex and
// Claude runners come in M2; Fake plays scripted answers for tests.
package runner

import "context"

// Request is one agent step to run.
type Request struct {
	Agent      string // "codex" or "claude"
	Name       string // the step's name in state.json, e.g. "codex-audit"
	Step       string // the schema step: audit, convergence, dispute, fix
	Round      int
	Attempt    int    // 1 for the first try; 2 for the resumed retry
	Prompt     string // the whole prompt, sent on stdin
	RepoDir    string
	ScratchDir string
	Commit     string // the commit the step works on
	Resume     string // the session to resume; "" starts a fresh session
}

// Result is what the step produced.
type Result struct {
	Answer    []byte // the final JSON answer; nil if there is none
	SessionID string
	Model     string // the model the CLI reports; "" if it doesn't say
	Problem   string // why there is no answer, if there is none
}

// Runner runs agent steps.
type Runner interface {
	Run(ctx context.Context, req Request) Result
}
