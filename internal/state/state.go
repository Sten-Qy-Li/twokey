// Package state keeps a run's state.json (SPEC section 10, "Resume"). It
// is written atomically after every change, so a crash never leaves half a
// file.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileName is the state file's name in the run folder.
const FileName = "state.json"

// Version is the state file format.
const Version = 1

// Run statuses.
const (
	Running   = "running"
	Paused    = "paused"
	Converged = "converged"
	MaxRounds = "max_rounds"
	Stopped   = "stopped" // the owner quit; resume continues
)

// Step statuses.
const (
	Pending = "pending"
	Active  = "running"
	Done    = "done"
	Failed  = "failed"
)

// Step names. Audits are "<agent>-audit".
const (
	CodexAudit  = "codex-audit"
	ClaudeAudit = "claude-audit"
	Convergence = "convergence"
	Dispute     = "dispute"
	Fix         = "fix"
)

// Step is one agent step of a round.
type Step struct {
	Name      string    `json:"name"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	SessionID string    `json:"session_id,omitempty"`
	Model     string    `json:"model,omitempty"`
	Prompt    string    `json:"prompt,omitempty"` // the prompt file that was sent
	Result    string    `json:"result,omitempty"` // the published answer
	Heading   string    `json:"heading,omitempty"`
	Started   time.Time `json:"started,omitzero"`
	Finished  time.Time `json:"finished,omitzero"`
	Error     string    `json:"error,omitempty"`
	Usage     Usage     `json:"usage,omitzero"` // all tries together
	// SessionUsage is what the CLI last reported for the current session.
	// Claude reports a resumed session's whole total, so a resume replaces
	// it instead of adding to it.
	SessionUsage Usage    `json:"session_usage,omitzero"`
	Denials      []string `json:"denials,omitempty"` // permission denials, refused commands
	Notes        []string `json:"notes,omitempty"`
}

// Usage is tokens and the estimated cost of agent runs.
type Usage struct {
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// Plus adds two usages.
func (u Usage) Plus(o Usage) Usage {
	return Usage{u.InputTokens + o.InputTokens, u.OutputTokens + o.OutputTokens, u.CostUSD + o.CostUSD}
}

// Minus subtracts o from u.
func (u Usage) Minus(o Usage) Usage {
	return Usage{u.InputTokens - o.InputTokens, u.OutputTokens - o.OutputTokens, u.CostUSD - o.CostUSD}
}

// Gate is the last gate result of a round.
type Gate struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

// Round is one pair of blind audits of one commit, plus what follows.
type Round struct {
	N         int    `json:"n"`
	Commit    string `json:"commit"`     // the commit both audits audit
	RemoteSHA string `json:"remote_sha"` // <remote>/<branch> when the round started
	Synced    bool   `json:"synced"`     // the round's start caught up with [project] upstream (SPEC section 3)
	// At the sync: <remote>/<upstream>, and how many commits Commit is
	// ahead of it (DECISIONS.md D42).
	UpstreamSHA   string  `json:"upstream_sha,omitempty"`
	UpstreamAhead int     `json:"upstream_ahead,omitempty"`
	Steps         []*Step `json:"steps"`
	Published     bool    `json:"published"` // audits copied out of staging and logged
	Gate          *Gate   `json:"gate,omitempty"`
	Verified      bool    `json:"verified"`
	// VerifyRetries counts the fix session's extra tries after a failed
	// check (at most one, SPEC section 8).
	VerifyRetries int  `json:"verify_retries"`
	Pushed        bool `json:"pushed"`
}

// Pause says why the run waits for the owner.
type Pause struct {
	Reason  string   `json:"reason"` // PAUSE_*
	Message string   `json:"message"`
	Step    string   `json:"step,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	// Protected holds the protected branches that moved on the remote
	// during the step, with their new SHAs. Resume asks the owner to
	// confirm them (DECISIONS.md D40).
	Protected map[string]string `json:"protected,omitempty"`
}

// State is a whole run.
type State struct {
	Version       int               `json:"version"`
	RunID         string            `json:"run_id"`
	Started       time.Time         `json:"started"`
	Mode          string            `json:"mode"`
	MaxRounds     int               `json:"max_rounds"`
	Branch        string            `json:"branch"`
	Remote        string            `json:"remote"`
	Status        string            `json:"status"`
	Pause         *Pause            `json:"pause,omitempty"`
	ProtectedSHAs map[string]string `json:"protected_shas"` // the SHAs twokey last accepted (D40)
	LogHash       string            `json:"log_hash"`       // what the log file must hash to now
	Rounds        []*Round          `json:"rounds"`
	Decisions     map[string]string `json:"decisions"`
	Rulings       map[string]string `json:"rulings"`
	Notes         []string          `json:"notes,omitempty"` // for the report
}

// Current returns the round in progress, or nil before round 1.
func (s *State) Current() *Round {
	if len(s.Rounds) == 0 {
		return nil
	}
	return s.Rounds[len(s.Rounds)-1]
}

// NewRound starts a round on a commit.
func (s *State) NewRound(commit, remoteSHA string) *Round {
	r := &Round{N: len(s.Rounds) + 1, Commit: commit, RemoteSHA: remoteSHA}
	s.Rounds = append(s.Rounds, r)
	return r
}

// Step returns the round's step with this name, or nil.
func (r *Round) Step(name string) *Step {
	for _, st := range r.Steps {
		if st.Name == name {
			return st
		}
	}
	return nil
}

// Ensure returns the step, adding it as pending if the round has none.
func (r *Round) Ensure(name, agent string) *Step {
	if st := r.Step(name); st != nil {
		return st
	}
	st := &Step{Name: name, Agent: agent, Status: Pending}
	r.Steps = append(r.Steps, st)
	return st
}

// IsDone reports whether the round's step finished: it answered, and
// twokey published and logged the answer. A step can answer and then pause
// at the check that follows; the run publishes and logs the answer when it
// resumes (DECISIONS.md D41).
func (r *Round) IsDone(name string) bool {
	st := r.Step(name)
	return st != nil && st.Status == Done && st.Result != "" && st.Heading != ""
}

// ReadOnly reports whether a step may not change the repository.
func ReadOnly(step string) bool { return step != Fix }

// Interrupted returns the steps a crash or Ctrl+C left running. Read-only
// steps go back to pending, so they rerun; a fix stays as it is, because
// it may have changed the tree, and the caller must pause (SPEC section 10).
func (s *State) Interrupted() (rerun []string, fix *Step) {
	r := s.Current()
	if r == nil {
		return nil, nil
	}
	for _, st := range r.Steps {
		if st.Status != Active {
			continue
		}
		if ReadOnly(st.Name) {
			st.Status = Pending
			rerun = append(rerun, st.Name)
		} else {
			fix = st
		}
	}
	return rerun, fix
}

// Save writes the state atomically: a temp file in the same folder, then
// a rename.
func Save(dir string, s *State) error {
	s.Version = Version
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, FileName))
}

// Load reads a run's state.
func Load(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", filepath.Join(dir, FileName), err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("%s has format version %d; this twokey reads version %d", filepath.Join(dir, FileName), s.Version, Version)
	}
	if s.Decisions == nil {
		s.Decisions = map[string]string{}
	}
	if s.Rulings == nil {
		s.Rulings = map[string]string{}
	}
	return &s, nil
}
