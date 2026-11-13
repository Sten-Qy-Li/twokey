// Package invariant checks what an agent step changed, instead of trusting
// the agent's report (SPEC section 8). Take a snapshot before and after a
// step; the comparisons are pure functions.
package invariant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/gitx"
)

// Snapshot is the state twokey compares around a step.
type Snapshot struct {
	Head     string
	Branch   string
	Status   string            // git status --porcelain
	Refs     string            // branches, tags, stash (no remote-tracking refs)
	LogHash  string            // SHA-256 of the log file
	Remote   map[string]string // branch -> SHA on the remote ("" if missing)
	RunFiles map[string]string // twokey's run folder: relative path -> SHA-256
	// PushConfig lists git settings that would get a push past the agents'
	// push block (D62); it must stay empty.
	PushConfig string
}

// Taker takes snapshots of one repository.
type Taker struct {
	Repo     gitx.Repo
	Remote   string
	Branches []string // the working branch and the protected branches
	LogPath  string
	RunDir   string // this run's folder
	// Skip leaves run-folder files out of the manifest (relative, with
	// slashes): files twokey itself rewrites during a step, such as
	// state.json, which the caller checks another way.
	Skip func(rel string) bool
}

// Take records the repository's state now. It asks the remote too.
func (t Taker) Take(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	var err error
	if s.Head, err = t.Repo.RevParse(ctx, "HEAD"); err != nil {
		return s, err
	}
	if s.Branch, err = t.Repo.CurrentBranch(ctx); err != nil {
		s.Branch = "(detached HEAD)"
	}
	if s.Status, err = t.Repo.Status(ctx); err != nil {
		return s, err
	}
	if s.Refs, err = t.Repo.Refs(ctx); err != nil {
		return s, err
	}
	if s.LogHash, err = hashFile(t.LogPath); err != nil {
		return s, fmt.Errorf("can't read the log file: %w", err)
	}
	if s.Remote, err = t.Repo.LsRemoteBranches(ctx, t.Remote, t.Branches); err != nil {
		return s, err
	}
	if s.RunFiles, err = manifest(t.RunDir); err != nil {
		return s, err
	}
	bypass, err := t.Repo.PushBypasses(ctx)
	if err != nil {
		return s, err
	}
	s.PushConfig = strings.Join(bypass, "\n")
	if t.Skip != nil {
		for rel := range s.RunFiles {
			if t.Skip(rel) {
				delete(s.RunFiles, rel)
			}
		}
	}
	return s, nil
}

// ReadOnly lists what an audit, convergence or dispute step changed that it
// must not change. newFiles are the run-folder files twokey itself writes
// during the step (relative paths); any other new file is a problem.
func ReadOnly(before, after Snapshot, newFiles []string) []string {
	var p []string
	if after.Head != before.Head {
		p = append(p, fmt.Sprintf("HEAD moved from %s to %s.", short(before.Head), short(after.Head)))
	}
	p = append(p, common(before, after, newFiles)...)
	if after.Status != before.Status {
		p = append(p, "The working tree changed. git status now shows:"+indent(after.Status))
	}
	p = append(p, refChanges(before.Refs, after.Refs, "")...)
	return p
}

// AfterFix lists what a fix step changed that it must not change. The new
// commits on the branch are checked by the caller with git.
func AfterFix(before, after Snapshot, branch string, newFiles []string) []string {
	p := common(before, after, newFiles)
	if after.Status != "" {
		p = append(p, "The fix left uncommitted changes. git status shows:"+indent(after.Status))
	}
	p = append(p, refChanges(before.Refs, after.Refs, "refs/heads/"+branch)...)
	return p
}

// common holds the checks for every step.
func common(before, after Snapshot, newFiles []string) []string {
	var p []string
	if after.Branch != before.Branch {
		p = append(p, fmt.Sprintf("The current branch changed from %s to %s.", before.Branch, after.Branch))
	}
	if after.LogHash != before.LogHash {
		p = append(p, "The log file changed.")
	}
	if after.PushConfig != before.PushConfig {
		p = append(p, "git's push settings changed, so an agent may have tried to get past twokey's push block. They are now:"+indent(after.PushConfig))
	}
	for _, b := range sortedKeys(before.Remote) {
		if after.Remote[b] != before.Remote[b] {
			p = append(p, fmt.Sprintf("Branch %s changed on the remote (%s -> %s). Something pushed during the step.",
				b, short(before.Remote[b]), short(after.Remote[b])))
		}
	}
	for _, f := range sortedKeys(before.RunFiles) {
		switch h, ok := after.RunFiles[f]; {
		case !ok:
			p = append(p, fmt.Sprintf("twokey's file %s was deleted.", f))
		case h != before.RunFiles[f]:
			p = append(p, fmt.Sprintf("twokey's file %s was changed.", f))
		}
	}
	for _, f := range sortedKeys(after.RunFiles) {
		if _, old := before.RunFiles[f]; !old && !slices.Contains(newFiles, f) {
			p = append(p, fmt.Sprintf("A new file appeared in twokey's run folder: %s.", f))
		}
	}
	return p
}

// refChanges compares for-each-ref output, ignoring one ref ("" = none).
func refChanges(before, after, ignore string) []string {
	parse := func(s string) map[string]string {
		m := map[string]string{}
		for _, line := range strings.Split(s, "\n") {
			if name, sha, ok := strings.Cut(line, " "); ok && name != ignore {
				m[name] = sha
			}
		}
		return m
	}
	b, a := parse(before), parse(after)
	var p []string
	for _, name := range sortedKeys(b) {
		switch sha, ok := a[name]; {
		case !ok:
			p = append(p, fmt.Sprintf("The ref %s was deleted.", name))
		case sha != b[name]:
			p = append(p, fmt.Sprintf("The ref %s moved (%s -> %s).", name, short(b[name]), short(sha)))
		}
	}
	for _, name := range sortedKeys(a) {
		if _, ok := b[name]; !ok {
			p = append(p, fmt.Sprintf("A new ref appeared: %s.", name))
		}
	}
	return p
}

func manifest(dir string) (map[string]string, error) {
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		h, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		m[filepath.ToSlash(rel)] = h
		return nil
	})
	return m, err
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func short(sha string) string {
	if sha == "" {
		return "(none)"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func indent(s string) string {
	if s == "" {
		return " (nothing)"
	}
	return "\n  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
