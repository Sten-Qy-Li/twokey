package engine

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/invariant"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/upstream"
)

// compareUpstream starts a round (SPEC section 3, "Upstream"). With
// [project] upstream set, it notes how the round's commit stands to
// <remote>/<upstream>, for the audits (${upstream_status}), and warns when
// it is behind or has diverged. It never moves the branch: bringing the
// upstream's commits in is the owner's call (DECISIONS.md D57).
func (e *engine) compareUpstream(ctx context.Context, r *state.Round) bool {
	up := e.cfg.Project.Upstream
	if up == "" {
		e.update(func() { r.Synced = true })
		return true
	}
	remote, branch := e.st.Remote, e.st.Branch
	if err := e.repo.Fetch(ctx, remote); err != nil {
		return e.pause(PausePushFailed, "git fetch failed: "+err.Error(), "Check your network and login, then run twokey resume.", "upstream", nil)
	}
	ru, err := e.repo.RevParse(ctx, "refs/remotes/"+remote+"/"+up)
	if err != nil {
		return e.pause(PauseInvariant, fmt.Sprintf("Can't read %s/%s after git fetch.", remote, up),
			"Check [project] upstream in the config, then run twokey resume.", "upstream", nil)
	}
	ahead, behind, err := e.repo.AheadBehind(ctx, r.Commit, ru)
	if err != nil {
		return e.pause(PauseInvariant, "Can't compare the branches: "+err.Error(), "Check the repository, then run twokey resume.", "upstream", nil)
	}
	rel := upstream.Classify(ahead, behind)
	what := upstream.Describe(rel, ahead, behind, remote+"/"+branch, remote+"/"+up)
	if advice := upstream.Advice(rel, remote, branch, up); advice != "" {
		ui.Line(e.o.Out, ui.Warn, what+" twokey doesn't move "+branch+": this round audits it as it is.")
		ui.Next(e.o.Out, advice)
	} else {
		ui.Line(e.o.Out, ui.OK, what)
	}
	e.update(func() { r.Synced, r.UpstreamSHA, r.UpstreamAhead, r.UpstreamBehind = true, ru, ahead, behind })
	return true
}

// upstreamStatus tells the agents how the round's commit stood to
// <remote>/<upstream> when the round started (DECISIONS.md D42). Both blind
// audits get the same text, so they judge checks against a live deployment
// the same way.
func (e *engine) upstreamStatus(r *state.Round) string {
	up := e.cfg.Project.Upstream
	if up == "" {
		return "none is set, so twokey doesn't compare this commit with another branch."
	}
	ref := e.cfg.Project.Remote + "/" + up
	const same = " Both audits get this same note. Judge checks against a live deployment with it in mind."
	switch {
	case r.UpstreamSHA == "":
		return fmt.Sprintf("%s. twokey hasn't compared this commit with it yet.", ref)
	case r.UpstreamAhead == 0 && r.UpstreamBehind == 0:
		return fmt.Sprintf("%s. When this round started, it was the same commit as the one you audit (%s). "+
			"A deployment built from %s runs this commit once it has finished deploying.%s", ref, short(r.Commit), ref, same)
	case r.UpstreamBehind == 0:
		return fmt.Sprintf("%s. When this round started, it was %s, and the commit you audit (%s) was %s ahead of it. "+
			"A deployment built from %s doesn't include those commits yet.%s",
			ref, short(r.UpstreamSHA), short(r.Commit), plural(r.UpstreamAhead, "commit"), ref, same)
	case r.UpstreamAhead == 0:
		return fmt.Sprintf("%s. When this round started, it was %s, %s ahead of the commit you audit (%s). "+
			"A deployment built from %s may include changes that this commit lacks.%s",
			ref, short(r.UpstreamSHA), plural(r.UpstreamBehind, "commit"), short(r.Commit), ref, same)
	default:
		return fmt.Sprintf("%s. When this round started, it was %s, and it had diverged from the commit you audit (%s): "+
			"this commit has %s that %s lacks, and %s has %s that this commit lacks. A deployment built from %s runs a different mix of changes.%s",
			ref, short(r.UpstreamSHA), short(r.Commit), plural(r.UpstreamAhead, "commit"), ref, ref, plural(r.UpstreamBehind, "commit"), ref, same)
	}
}

// upstreamAdvice is the report's last line after a converged run whose
// branch has commits the upstream lacks (DECISIONS.md D42). It is "" when
// there is nothing to merge.
func (e *engine) upstreamAdvice(ctx context.Context) string {
	up := e.cfg.Project.Upstream
	if up == "" {
		return ""
	}
	_ = e.repo.Fetch(ctx, e.st.Remote) // best effort: the last fetch is recent anyway
	ahead, _, err := e.repo.AheadBehind(ctx, "HEAD", "refs/remotes/"+e.st.Remote+"/"+up)
	if err != nil || ahead == 0 {
		return ""
	}
	return fmt.Sprintf("merge %s into %s (merge commit, not squash). Then run git merge --ff-only %s/%s and git push yourself.",
		e.st.Branch, up, e.st.Remote, up)
}

// pushHead pushes HEAD to <remote>/<branch> with the push rules of SPEC
// section 8: only if nobody else pushed to the branch since r.RemoteSHA,
// never with force, and confirmed with git ls-remote afterwards.
func (e *engine) pushHead(ctx context.Context, r *state.Round, step string) bool {
	remote, branch := e.st.Remote, e.st.Branch
	if err := e.repo.Fetch(ctx, remote); err != nil {
		return e.pause(PausePushFailed, "git fetch failed: "+err.Error(), "Check your network and login, then run twokey resume.", step, nil)
	}
	now, err := e.repo.RevParse(ctx, "refs/remotes/"+remote+"/"+branch)
	if err != nil || now != r.RemoteSHA {
		return e.pause(PauseRemoteMoved, fmt.Sprintf("Someone pushed to %s/%s during this round (%s -> %s).", remote, branch, short(r.RemoteSHA), short(now)),
			"Bring their commits into your branch yourself (twokey never merges or rebases), then run twokey resume.", step, nil)
	}
	head, _ := e.repo.RevParse(ctx, "HEAD")
	if err := e.repo.Push(ctx, remote, branch); err != nil {
		return e.pause(PausePushFailed, "git push failed: "+err.Error(), "Check the error, then run twokey resume.", step, nil)
	}
	shas, err := e.repo.LsRemoteBranches(ctx, remote, append([]string{branch}, e.cfg.Project.ProtectedBranches...))
	if err != nil {
		return e.pause(PausePushFailed, "Can't confirm the push: "+err.Error(), "Check the remote, then run twokey resume.", step, nil)
	}
	if shas[branch] != head {
		return e.invariantFailed("The push", []string{fmt.Sprintf("%s/%s is %s after the push, not %s.", remote, branch, short(shas[branch]), short(head))})
	}
	e.acceptProtected(shas)
	return true
}

// snapshotBefore takes the snapshot before an agent step. No agent was
// running since the last check, so protected branches that moved since
// then become the new baseline (DECISIONS.md D40). A git setting that would
// get a push past the agents' block stops the run first (D62).
func (e *engine) snapshotBefore(ctx context.Context) (invariant.Snapshot, bool) {
	snap, ok := e.snapshot(ctx)
	if !ok {
		return snap, false
	}
	if snap.PushConfig != "" {
		first, _, _ := strings.Cut(snap.PushConfig, "\n")
		return snap, e.pause(PauseInvariant, "These git settings would let agents push past twokey's block:"+indent(snap.PushConfig),
			"Remove them ("+gitx.UnsetHint(first)+"), then run twokey resume.", "", nil)
	}
	e.acceptProtected(snap.Remote)
	return snap, true
}

// acceptProtected takes protected-branch SHAs that twokey read while no
// agent was running as the new baseline, and notes each change.
func (e *engine) acceptProtected(shas map[string]string) {
	var notes []string
	for _, b := range e.cfg.Project.ProtectedBranches {
		if sha, ok := shas[b]; ok && sha != e.st.ProtectedSHAs[b] {
			notes = append(notes, fmt.Sprintf("%s moved on %s while no agent was running (%s -> %s).", b, e.st.Remote, short(e.st.ProtectedSHAs[b]), short(sha)))
		}
	}
	if len(notes) == 0 {
		return
	}
	e.update(func() {
		if e.st.ProtectedSHAs == nil {
			e.st.ProtectedSHAs = map[string]string{}
		}
		for _, b := range e.cfg.Project.ProtectedBranches {
			if sha, ok := shas[b]; ok {
				e.st.ProtectedSHAs[b] = sha
			}
		}
		for _, n := range notes {
			e.st.Notes = append(e.st.Notes, fmt.Sprintf("Round %d: %s", e.st.Current().N, n))
		}
	})
	for _, n := range notes {
		ui.Line(e.o.Out, ui.OK, n+" twokey takes it as the new baseline.")
	}
}

// movedProtected takes the protected branches that moved during a step out
// of the after-snapshot, so the other checks don't report them, and
// returns them with their new SHAs.
func (e *engine) movedProtected(before invariant.Snapshot, after *invariant.Snapshot) map[string]string {
	var moved map[string]string
	for _, b := range e.cfg.Project.ProtectedBranches {
		if after.Remote[b] != before.Remote[b] {
			if moved == nil {
				moved = map[string]string{}
			}
			moved[b] = after.Remote[b]
			after.Remote[b] = before.Remote[b]
		}
	}
	return moved
}

// acceptMoved takes protected branches that moved during an agent step as
// the new baseline, with a note for the report. Agents can't push (D62),
// so someone else moved them, for example a teammate's merge.
func (e *engine) acceptMoved(what string, before, moved map[string]string) {
	if len(moved) == 0 {
		return
	}
	var notes []string
	for _, b := range e.cfg.Project.ProtectedBranches {
		if sha, ok := moved[b]; ok {
			notes = append(notes, fmt.Sprintf("%s moved on %s during %s (%s -> %s). Agents can't push, so it was someone else, for example a teammate's merge.",
				b, e.st.Remote, lowerFirst(what), short(before[b]), short(sha)))
		}
	}
	e.update(func() {
		if e.st.ProtectedSHAs == nil {
			e.st.ProtectedSHAs = map[string]string{}
		}
		for b, sha := range moved {
			e.st.ProtectedSHAs[b] = sha
		}
		for _, n := range notes {
			e.st.Notes = append(e.st.Notes, fmt.Sprintf("Round %d: %s", e.st.Current().N, n))
		}
	})
	for _, n := range notes {
		ui.Line(e.o.Out, ui.OK, n+" twokey takes it as the new baseline.")
	}
}

// describeMoves lists moved protected branches with their new commits.
func (e *engine) describeMoves(ctx context.Context, before, moved map[string]string) string {
	fetchErr := e.repo.Fetch(ctx, e.st.Remote) // brings the new commits in, to list them
	var b strings.Builder
	for _, br := range e.cfg.Project.ProtectedBranches {
		sha, ok := moved[br]
		if !ok {
			continue
		}
		old := before[br]
		fmt.Fprintf(&b, "- %s: %s -> %s\n", br, short(old), short(sha))
		switch {
		case sha == "":
			b.WriteString("  The branch was deleted on the remote.\n")
			continue
		case old == "":
			b.WriteString("  The branch is new on the remote.\n")
			continue
		case fetchErr != nil:
			fmt.Fprintf(&b, "  Can't list its new commits: %v\n", fetchErr)
			continue
		}
		if ok, _ := e.repo.IsAncestor(ctx, old, sha); !ok {
			fmt.Fprintf(&b, "  Its history was rewritten: %s is no longer part of it (a force push).\n", short(old))
		}
		const most = 20
		lines, err := e.repo.LogLines(ctx, old, sha, most)
		if err != nil {
			fmt.Fprintf(&b, "  Can't list its new commits: %v\n", err)
		}
		for _, l := range lines {
			fmt.Fprintf(&b, "    %s\n", l)
		}
		if len(lines) == most {
			b.WriteString("    ... and maybe more\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// lowerFirst turns "Round 2's fix step" into "round 2's fix step".
func lowerFirst(s string) string {
	if s == "" {
		return "a step"
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// probeSandbox tries git ls-remote and docker in Codex's sandbox, without a
// model call (D51). Codex's prompts name what fails, so Codex records those
// checks as not verified instead of retrying them.
func (e *engine) probeSandbox(ctx context.Context) []string {
	probe := e.o.Sandbox
	if probe == nil {
		if e.o.FakeAgents || runtime.GOOS != "windows" {
			return nil
		}
		probe = func(ctx context.Context) []runner.SandboxResult {
			return runner.ProbeCodexSandbox(ctx, e.o.Proc, e.cfg, e.o.Root)
		}
	}
	var limits, names []string
	for _, r := range runner.Limited(probe(ctx)) {
		limits = append(limits, fmt.Sprintf("%s (%s)", r.Name, r.Detail))
		names = append(names, r.Name)
	}
	if len(limits) > 0 {
		ui.Line(e.o.Out, ui.Warn, "Codex can't run these in its sandbox: "+strings.Join(names, ", ")+".")
		ui.Detail(e.o.Out, "Its prompts say so, and to record those checks as not verified instead of retrying.")
	}
	return limits
}

// sandboxLimits is ${sandbox_limits}: what fails in the agent's sandbox.
func (e *engine) sandboxLimits(agent string) string {
	if agent != "codex" || len(e.st.CodexLimits) == 0 {
		return "none known."
	}
	return "these commands don't work in your sandbox: " + strings.Join(e.st.CodexLimits, "; ") +
		". Don't retry them or look for workarounds. Record what they would have checked as not verified (kind external_evidence)."
}
