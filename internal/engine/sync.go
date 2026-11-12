package engine

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/invariant"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/upstream"
)

// sync starts a round (SPEC section 3, "Sync"). With [project] upstream
// set, it fast-forwards the branch to <remote>/<upstream> when the branch is
// behind, and pauses when the two have diverged. Apart from this
// fast-forward, twokey never merges or rebases (DECISIONS.md D39).
func (e *engine) sync(ctx context.Context, r *state.Round) bool {
	up := e.cfg.Project.Upstream
	if up == "" {
		e.update(func() { r.Synced = true })
		return true
	}
	remote, branch := e.st.Remote, e.st.Branch
	if err := e.repo.Fetch(ctx, remote); err != nil {
		return e.pause(PausePushFailed, "git fetch failed: "+err.Error(), "Check your network and login, then run twokey resume.", "sync", nil)
	}
	head, errH := e.repo.RevParse(ctx, "HEAD")
	rb, errB := e.repo.RevParse(ctx, "refs/remotes/"+remote+"/"+branch)
	ru, errU := e.repo.RevParse(ctx, "refs/remotes/"+remote+"/"+up)
	if errH != nil || errB != nil || errU != nil {
		return e.pause(PauseInvariant, fmt.Sprintf("Can't read HEAD, %s/%s or %s/%s after git fetch.", remote, branch, remote, up),
			"Check [project] branch and upstream in the config, then run twokey resume.", "sync", nil)
	}
	// HEAD must match the remote branch. The one exception is a
	// fast-forward toward the upstream whose push failed: it is pushed now.
	if head != rb {
		fromBranch, _ := e.repo.IsAncestor(ctx, rb, head)
		toUpstream, _ := e.repo.IsAncestor(ctx, head, ru)
		if !fromBranch || !toUpstream {
			return e.pause(PauseRemoteMoved, fmt.Sprintf("HEAD (%s) and %s/%s (%s) differ.", short(head), remote, branch, short(rb)),
				fmt.Sprintf("Make them match: push your commits, or run git merge --ff-only %s/%s. Then run twokey resume.", remote, branch), "sync", nil)
		}
	}
	ahead, behind, err := e.repo.AheadBehind(ctx, rb, ru)
	if err != nil {
		return e.pause(PauseInvariant, "Can't compare the branches: "+err.Error(), "Check the repository, then run twokey resume.", "sync", nil)
	}
	rel := upstream.Classify(ahead, behind)
	what := upstream.Describe(rel, ahead, behind, remote+"/"+branch, remote+"/"+up)
	switch rel {
	case upstream.Diverged:
		return e.pause(PauseDiverged, what+" twokey only fast-forwards "+branch+". It never merges or rebases it.",
			fmt.Sprintf("Merge %s into %s first, through a merge request with a merge commit (not squash). Then run twokey resume. "+
				"A squash merge leaves the commits of %s out of %s, so the two stay diverged for good.", branch, up, branch, up), "sync", nil)
	case upstream.Behind:
		return e.fastForward(ctx, r, rb, ru, behind)
	}
	ui.Line(e.o.Out, ui.OK, what)
	e.update(func() {
		r.Commit, r.RemoteSHA, r.Synced = rb, rb, true
		r.UpstreamSHA, r.UpstreamAhead = ru, ahead
	})
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
	case r.UpstreamAhead == 0:
		return fmt.Sprintf("%s. When this round started, it was the same commit as the one you audit (%s). "+
			"A deployment built from %s runs this commit once it has finished deploying.%s", ref, short(r.Commit), ref, same)
	default:
		return fmt.Sprintf("%s. When this round started, it was %s, and the commit you audit (%s) was %s ahead of it. "+
			"A deployment built from %s doesn't include those commits yet.%s",
			ref, short(r.UpstreamSHA), short(r.Commit), plural(r.UpstreamAhead, "commit"), ref, same)
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
	return fmt.Sprintf("merge %s into %s (merge commit, not squash). The next run fast-forwards.", e.st.Branch, up)
}

// fastForward moves the branch from 'from' to the upstream's commit: first
// locally with git merge --ff-only, then on the remote with the push rules
// of SPEC section 8.
func (e *engine) fastForward(ctx context.Context, r *state.Round, from, to string, n int) bool {
	remote, branch, up := e.st.Remote, e.st.Branch, e.cfg.Project.Upstream
	if e.o.UI.Before(fmt.Sprintf("fast-forward %s to %s/%s (%s, %s), then push it", branch, remote, up, short(to), plural(n, "new commit")), nil) == Quit {
		return e.quit()
	}
	if err := e.repo.MergeFFOnly(ctx, to); err != nil {
		return e.pause(PauseInvariant, "git merge --ff-only failed: "+err.Error(),
			fmt.Sprintf("Run git merge --ff-only %s/%s yourself to see why, then run twokey resume.", remote, up), "sync", nil)
	}
	if head, _ := e.repo.RevParse(ctx, "HEAD"); head != to {
		return e.pause(PauseInvariant, fmt.Sprintf("HEAD is %s after the fast-forward, not %s.", short(head), short(to)),
			"Check the repository, then run twokey resume.", "sync", nil)
	}
	e.update(func() { r.RemoteSHA = from })
	if !e.pushHead(ctx, r, "sync") {
		return false
	}
	note := fmt.Sprintf("Round %d: fast-forwarded %s from %s to %s (%s from %s/%s).", r.N, branch, short(from), short(to), plural(n, "commit"), remote, up)
	e.update(func() {
		r.Commit, r.RemoteSHA, r.Synced = to, to, true
		r.UpstreamSHA, r.UpstreamAhead = to, 0
		e.st.Notes = append(e.st.Notes, note)
	})
	ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Fast-forwarded %s to %s/%s and pushed it: %s -> %s (%s).", branch, remote, up, short(from), short(to), plural(n, "commit")))
	return true
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
// then become the new baseline (DECISIONS.md D40).
func (e *engine) snapshotBefore(ctx context.Context) (invariant.Snapshot, bool) {
	snap, ok := e.snapshot(ctx)
	if ok {
		e.acceptProtected(snap.Remote)
	}
	return snap, ok
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

// stepViolated pauses after an agent step that changed what it must not
// change. A protected branch that moved during the step may be a
// teammate's merge or an agent's push, and twokey can't tell which, so the
// owner confirms it on resume (DECISIONS.md D40).
func (e *engine) stepViolated(ctx context.Context, what string, problems []string, before, moved map[string]string) bool {
	if len(moved) == 0 {
		return e.invariantFailed(what, problems)
	}
	msg := fmt.Sprintf("A protected branch moved on %s during %s:\n%s\ntwokey can't tell a teammate's merge from an agent's push, because the agents use your git login.",
		e.st.Remote, lowerFirst(what), e.describeMoves(ctx, before, moved))
	p := &state.Pause{Reason: PauseProtected, Message: msg, Step: what, Protected: moved}
	next := "Check that this change was expected, for example a teammate's merge. Then run twokey resume and confirm it. If an agent pushed it, undo that on the remote first."
	if len(problems) > 0 {
		p.Reason = PauseInvariant
		p.Message = fmt.Sprintf("%s changed things it must not change:\n- %s\n\n%s", what, strings.Join(problems, "\n- "), msg)
		next = "Look at the changes and undo what the agent shouldn't have done. Then run twokey resume; it asks you to confirm the protected branch too."
	}
	return e.pauseWith(p, next)
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

// confirmProtected asks the owner whether the protected branches that moved
// during the paused step moved on purpose. Yes makes them the new
// baseline; anything else keeps the run paused.
func (e *engine) confirmProtected(ctx context.Context) bool {
	p := e.st.Pause
	text := fmt.Sprintf("A protected branch moved on %s during %s:\n%s", e.st.Remote, lowerFirst(p.Step),
		e.describeMoves(ctx, e.st.ProtectedSHAs, p.Protected))
	if !e.o.UI.Confirm(text) {
		ui.Line(e.o.Out, ui.Pause, "Still paused: the protected branch change is not confirmed.")
		ui.Next(e.o.Out, "run twokey resume again and type y once you have checked the change.")
		return false
	}
	var notes []string
	for _, b := range e.cfg.Project.ProtectedBranches {
		if sha, ok := p.Protected[b]; ok {
			notes = append(notes, fmt.Sprintf("Round %d: you confirmed that %s moved on %s during %s (%s -> %s).",
				e.st.Current().N, b, e.st.Remote, lowerFirst(p.Step), short(e.st.ProtectedSHAs[b]), short(sha)))
		}
	}
	e.update(func() {
		for b, sha := range p.Protected {
			e.st.ProtectedSHAs[b] = sha
		}
		e.st.Notes = append(e.st.Notes, notes...)
		p.Protected = nil
	})
	ui.Line(e.o.Out, ui.OK, "Confirmed. twokey takes the protected branches as they are now.")
	return true
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
