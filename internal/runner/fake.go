package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/render"
)

// Fake plays scripted answers from a scenario folder ("twokey run
// --fake-agents <folder>"). Per round there is a folder r<N> with, for each
// step name (codex-audit, claude-audit, convergence, dispute, fix):
//
//	<name>.json                 the answer
//	<name>.actions.json         what the fake agent does before it answers
//	<name>.<attempt>.json       the answer for a later attempt (a resumed session)
//	<name>.<attempt>.actions.json
//
// In answers, ${commit} is the step's commit and ${new_commits} is a JSON
// list of the commits the actions made. A fake fix makes real commits.
type Fake struct {
	Dir string
	Git proc.Runner // runs git for the actions
}

// Actions is what a fake agent does before it answers.
type Actions struct {
	Write    map[string]string `json:"write"`     // files to write, relative to the repo
	Commit   string            `json:"commit"`    // commit everything with this message
	Append   map[string]string `json:"append"`    // text to append to files (to break rules)
	Push     bool              `json:"push"`      // push the branch (to break rules)
	NoAnswer string            `json:"no_answer"` // end without an answer, with this problem
	// TeammatePush makes a commit with this message in a separate clone of
	// the remote and pushes it to TeammateBranch (default: the current
	// branch): someone else pushed.
	TeammatePush   string `json:"teammate_push"`
	TeammateBranch string `json:"teammate_branch"`
	// Timeout ends the step as if it ran out of time.
	Timeout bool `json:"timeout"`
	// Limit ends the step with a usage limit and this message.
	// LimitResetIn (a Go duration such as "30m") says when it resets.
	Limit        string `json:"limit"`
	LimitResetIn string `json:"limit_reset_in"`
}

// Run plays one step.
func (f Fake) Run(ctx context.Context, req Request) Result {
	base := filepath.Join(f.Dir, fmt.Sprintf("r%d", req.Round), req.Name)
	if req.Attempt > 1 {
		base += fmt.Sprintf(".%d", req.Attempt)
	}
	res := Result{SessionID: fmt.Sprintf("fake-%s-r%d", req.Name, req.Round)}

	var acts Actions
	if data, err := os.ReadFile(base + ".actions.json"); err == nil {
		if err := json.Unmarshal(data, &acts); err != nil {
			res.Problem = fmt.Sprintf("bad scenario file %s.actions.json: %v", base, err)
			return res
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		res.Problem = err.Error()
		return res
	}
	commits, err := f.act(ctx, req.RepoDir, acts)
	if err != nil {
		res.Problem = "the fake agent's actions failed: " + err.Error()
		return res
	}
	switch {
	case acts.NoAnswer != "":
		res.Problem = acts.NoAnswer
		return res
	case acts.Timeout:
		res.TimedOut, res.Problem = true, "the fake agent ran out of time"
		return res
	case acts.Limit != "":
		res.Limit = &Limit{Message: acts.Limit}
		if d, err := time.ParseDuration(acts.LimitResetIn); err == nil {
			res.Limit.Reset = time.Now().Add(d)
		}
		res.Problem = "the fake agent hit a limit: " + acts.Limit
		return res
	}

	tmpl, err := os.ReadFile(base + ".json")
	if err != nil {
		res.Problem = fmt.Sprintf("the scenario has no answer %s.json", base)
		return res
	}
	list, _ := json.Marshal(commits)
	answer, err := render.Expand(string(tmpl), map[string]string{"commit": req.Commit, "new_commits": string(list)})
	if err != nil {
		res.Problem = fmt.Sprintf("bad scenario file %s.json: %v", base, err)
		return res
	}
	res.Answer = []byte(answer)
	return res
}

// act carries out the actions and returns the commits it made.
func (f Fake) act(ctx context.Context, dir string, a Actions) ([]string, error) {
	repo := gitx.Repo{Dir: dir, R: f.Git}
	for _, rel := range sortedKeys(a.Write) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(a.Write[rel]), 0o644); err != nil {
			return nil, err
		}
	}
	for _, rel := range sortedKeys(a.Append) {
		fh, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, werr := fh.WriteString(a.Append[rel])
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, werr
		}
	}
	var commits []string
	if a.Commit != "" {
		before, err := repo.RevParse(ctx, "HEAD")
		if err != nil {
			return nil, err
		}
		if _, err := repo.Output(ctx, "add", "--all"); err != nil {
			return nil, err
		}
		if _, err := repo.Output(ctx, "commit", "--quiet", "-m", a.Commit); err != nil {
			return nil, err
		}
		if commits, err = repo.NewCommits(ctx, before, "HEAD"); err != nil {
			return nil, err
		}
	}
	if a.Push {
		if _, err := repo.Output(ctx, "push", "--quiet"); err != nil {
			return nil, err
		}
	}
	if a.TeammatePush != "" {
		if err := teammatePush(ctx, repo, f.Git, a.TeammatePush, a.TeammateBranch); err != nil {
			return nil, fmt.Errorf("teammate push: %w", err)
		}
	}
	if commits == nil {
		commits = []string{}
	}
	return commits, nil
}

// teammatePush pushes a new commit to a branch (default: the current one)
// from a separate clone of origin.
func teammatePush(ctx context.Context, repo gitx.Repo, git proc.Runner, msg, branch string) error {
	url, err := repo.RemoteURL(ctx, "origin")
	if err != nil {
		return err
	}
	if branch == "" {
		if branch, err = repo.CurrentBranch(ctx); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "twokey-teammate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	other := gitx.Repo{Dir: dir, R: git}
	if _, err := other.Output(ctx, "clone", "--quiet", "--branch", branch, url, "."); err != nil {
		return err
	}
	if _, err := other.Output(ctx, "commit", "--quiet", "--allow-empty", "-m", msg); err != nil {
		return err
	}
	_, err = other.Output(ctx, "push", "--quiet", "origin", branch)
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Describe names a scenario folder in messages.
func (f Fake) Describe() string { return "fake agents from " + strings.TrimSpace(f.Dir) }
