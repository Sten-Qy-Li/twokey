// Package engine runs the audit-and-fix loop (SPEC section 3). The control
// flow is ordinary code: agents audit and fix, the engine decides what
// happens next and checks what the agents did.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/gate"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/invariant"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/logfile"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/render"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

// Exit codes of "twokey run" and "twokey resume" (DECISIONS.md D31).
const (
	ExitDone      = 0 // converged, or the owner stopped the run to resume later
	ExitError     = 1 // preflight failed, or something broke
	ExitPaused    = 3 // the run waits for the owner
	ExitMaxRounds = 4 // stopped after the last round with findings left
)

// Pause reasons (SPEC section 10).
const (
	PauseDecision    = string(gate.PauseDecision)
	PauseAdjudicate  = string(gate.PauseAdjudicate)
	PauseRecurring   = string(gate.PauseRecurring)
	PauseInvariant   = string(gate.PauseInvariant)
	PauseVerify      = "PAUSE_VERIFY_FAILED"
	PauseNoProgress  = "PAUSE_NO_PROGRESS"
	PauseRemoteMoved = "PAUSE_REMOTE_MOVED"
	PauseAgentFailed = "PAUSE_AGENT_FAILED"
	PausePushFailed  = "PAUSE_PUSH_FAILED"
	PauseInterrupted = "PAUSE_INTERRUPTED"
	PauseProtected   = "PAUSE_PROTECTED_MOVED" // a protected branch moved during an agent step (D40)
	PauseUsageLimit  = "PAUSE_USAGE_LIMIT"
	PauseTimeout     = "PAUSE_TIMEOUT"
)

// Cleaner removes the Docker resources a step left behind (SPEC section 9).
type Cleaner interface {
	Cleanup(ctx context.Context, prefix string) ([]string, error)
}

// Options configure a run.
type Options struct {
	Root        string // the repository root
	Cfg         *config.Config
	Runner      runner.Runner
	Proc        proc.Runner // git, verify commands and the editor
	UI          UI
	Out         io.Writer
	Now         func() time.Time
	StagingRoot string
	ScratchRoot string
	FakeAgents  bool   // the runner is fake: don't look for the CLIs
	MaxRounds   int    // 0 = [loop] max_rounds
	Autopilot   bool   // the mode recorded in state.json
	Editor      string // command that opens a prompt for editing
	Docker      Cleaner
	Heartbeat   time.Duration                                   // how often to print running steps; 0 = every minute
	Sleep       func(ctx context.Context, d time.Duration) bool // waits for a usage limit; false = stopped
	// Sandbox tries commands in Codex's sandbox at the start of a run
	// (D51). nil = the real check, on Windows, with real agents only.
	Sandbox func(ctx context.Context) []runner.SandboxResult
}

type engine struct {
	o       Options
	cfg     *config.Config
	st      *state.State
	mu      sync.Mutex // guards st while audits run in parallel
	broken  bool       // saving state failed: stop
	runRoot string     // <root>/<run_dir>
	runDir  string     // <runRoot>/runs/<run id>
	staging string     // <staging root>/<run id>
	repo    gitx.Repo
	logPath string
	tmpl    render.Templates
	taker   invariant.Taker
	// stateHash is the SHA-256 of the state.json twokey last wrote, so a
	// change by an agent is caught although twokey rewrites the file
	// during every step.
	stateHash  string
	beat       *heartbeat
	dockerWarn sync.Once // the parallel audits share one warning
}

func newEngine(o Options) *engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = time.Minute
	}
	o.Out = &lockedWriter{w: o.Out} // parallel steps print too
	e := &engine{o: o, cfg: o.Cfg}
	e.beat = &heartbeat{active: map[string]beatItem{}}
	e.runRoot = filepath.Join(o.Root, o.Cfg.Project.RunDir)
	e.repo = gitx.Repo{Dir: o.Root, R: o.Proc}
	e.logPath = filepath.Join(o.Root, o.Cfg.Project.LogFile)
	e.tmpl = render.Templates{OverrideDir: filepath.Join(e.runRoot, "templates")}
	return e
}

func (e *engine) setRun(id string) {
	e.runDir = filepath.Join(e.runRoot, "runs", id)
	e.staging = filepath.Join(e.o.StagingRoot, id)
	e.taker = invariant.Taker{
		Repo: e.repo, Remote: e.cfg.Project.Remote,
		Branches: append([]string{e.cfg.Project.Branch}, e.cfg.Project.ProtectedBranches...),
		LogPath:  e.logPath, RunDir: e.runDir,
		Skip: func(rel string) bool {
			return rel == state.FileName || strings.HasPrefix(rel, ".state-")
		},
	}
}

// Start runs preflight, creates a run and runs the loop.
func Start(ctx context.Context, o Options) int {
	e := newEngine(o)
	mode := "stepper"
	if o.Autopilot {
		mode = "autopilot"
	}
	fmt.Fprintf(o.Out, "twokey %s: new run in %s mode\n\n", version.String(), mode)
	if !e.preflight(ctx) {
		return ExitError
	}

	id := e.newRunID()
	e.setRun(id)
	lk, err := lock.Acquire(e.runRoot, lock.Info{PID: os.Getpid(), RunID: id, Started: e.o.Now().UTC()})
	if err != nil {
		e.lockFailed(err)
		return ExitError
	}
	defer e.release(lk)

	for _, d := range []string{e.runDir, e.staging} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			ui.Line(o.Out, ui.Fail, "Can't create "+d+": "+err.Error())
			return ExitError
		}
	}
	cleanOldScratch(o.ScratchRoot, e.o.Now(), 7*24*time.Hour)

	protected, err := e.repo.LsRemoteBranches(ctx, e.cfg.Project.Remote, e.cfg.Project.ProtectedBranches)
	if err != nil {
		ui.Line(o.Out, ui.Fail, "Can't read the protected branches on the remote: "+err.Error())
		return ExitError
	}
	head, err := e.repo.RevParse(ctx, "HEAD")
	if err != nil {
		ui.Line(o.Out, ui.Fail, "Can't read HEAD: "+err.Error())
		return ExitError
	}
	maxRounds := e.cfg.Loop.MaxRounds
	if o.MaxRounds > 0 {
		maxRounds = o.MaxRounds
	}
	e.st = &state.State{
		RunID: id, Started: e.o.Now().UTC(), Mode: mode, MaxRounds: maxRounds,
		Branch: e.cfg.Project.Branch, Remote: e.cfg.Project.Remote, Status: state.Running,
		ProtectedSHAs: protected, Decisions: map[string]string{}, Rulings: map[string]string{},
		CodexLimits: e.probeSandbox(ctx),
	}
	e.st.NewRound(head, head)
	e.save()
	ui.Line(o.Out, ui.OK, fmt.Sprintf("Run %s started on %s at %s", id, e.cfg.Project.Branch, short(head)))
	ui.Detail(o.Out, "Run folder: "+e.runDir)
	return e.loop(ctx)
}

// ErrNoRuns means the repository has no twokey runs yet.
var ErrNoRuns = errors.New("there are no runs")

// FindRun returns the run folder for an ID, or the latest run if id is "".
func FindRun(runRoot, id string) (string, error) {
	runs := filepath.Join(runRoot, "runs")
	if id != "" {
		dir := filepath.Join(runs, id)
		if _, err := os.Stat(filepath.Join(dir, state.FileName)); err != nil {
			return "", fmt.Errorf("there is no run %s in %s", id, runs)
		}
		return dir, nil
	}
	entries, err := os.ReadDir(runs)
	if err != nil {
		return "", fmt.Errorf("%w in %s yet", ErrNoRuns, runRoot)
	}
	var names []string
	for _, en := range entries {
		if en.IsDir() {
			names = append(names, en.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%w in %s yet", ErrNoRuns, runRoot)
	}
	sort.Strings(names) // run IDs start with the UTC time
	return filepath.Join(runs, names[len(names)-1]), nil
}

// Resume continues a run (SPEC section 10, "Resume").
func Resume(ctx context.Context, o Options, runID string, moreRounds int) int {
	e := newEngine(o)
	dir, err := FindRun(e.runRoot, runID)
	if err != nil {
		ui.Line(o.Out, ui.Fail, ui.Sentence(err.Error()))
		return ExitError
	}
	if e.st, err = state.Load(dir); err != nil {
		ui.Line(o.Out, ui.Fail, ui.Sentence(err.Error()))
		return ExitError
	}
	e.setRun(e.st.RunID)
	fmt.Fprintf(o.Out, "twokey %s: resuming run %s\n\n", version.String(), e.st.RunID)

	lk, err := lock.Acquire(e.runRoot, lock.Info{PID: os.Getpid(), RunID: e.st.RunID, Started: e.o.Now().UTC()})
	if err != nil {
		e.lockFailed(err)
		return ExitError
	}
	defer e.release(lk)

	if moreRounds > 0 {
		e.st.MaxRounds += moreRounds
		ui.Line(o.Out, ui.OK, fmt.Sprintf("Allowed %d more rounds (up to %d).", moreRounds, e.st.MaxRounds))
	}
	switch e.st.Status {
	case state.Converged:
		ui.Line(o.Out, ui.OK, "This run already converged. Start a new one with twokey run.")
		return ExitDone
	case state.MaxRounds:
		if moreRounds == 0 {
			ui.Line(o.Out, ui.OK, "This run stopped after its last round.")
			ui.Next(o.Out, "twokey resume --more-rounds 1")
			return ExitMaxRounds
		}
	}

	if cur, err := e.repo.CurrentBranch(ctx); err != nil || cur != e.st.Branch {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("The repository must be on %s to resume.", e.st.Branch))
		ui.Next(o.Out, "git switch "+e.st.Branch)
		return ExitError
	}
	if !e.resolve(ctx) {
		return e.exitCode()
	}
	e.st.Status, e.st.Pause = state.Running, nil
	e.save()
	return e.loop(ctx)
}

// resolve gets a stopped or paused run ready to go on. It returns false
// if the run must stay paused.
func (e *engine) resolve(ctx context.Context) bool {
	r := e.st.Current()
	rerun, fix := e.st.Interrupted()
	for _, name := range rerun {
		e.cleanupDocker(ctx, r, r.Step(name))
		ui.Line(e.o.Out, ui.OK, name+" was interrupted. It runs again.")
	}
	if fix != nil {
		e.cleanupDocker(ctx, r, fix)
	}
	head, _ := e.repo.RevParse(ctx, "HEAD")
	status, _ := e.repo.Status(ctx)
	if fix != nil {
		if status != "" || head != r.Commit {
			return e.pause(PauseInterrupted, "The fix step was interrupted and left changes:"+indent(status+"\nHEAD: "+short(head)),
				"Commit or discard the changes yourself (twokey never does), then run twokey resume.", state.Fix, nil)
		}
		fix.Status = state.Pending
		ui.Line(e.o.Out, ui.OK, "The interrupted fix left nothing behind. It runs again.")
	}

	if e.st.Pause != nil {
		if len(e.st.Pause.Protected) > 0 && !e.confirmProtected(ctx) {
			return false
		}
		switch e.st.Pause.Reason {
		case PauseAgentFailed, PauseUsageLimit, PauseTimeout:
			for _, s := range r.Steps {
				if s.Status == state.Failed {
					s.Status, s.Error = state.Pending, "" // attempts keep counting, so raw output files stay apart
				}
			}
		case PauseVerify:
			r.Verified = false
		case PauseNoProgress:
			if fs := r.Step(state.Fix); fs != nil && head == r.Commit {
				fs.Status = state.Pending
			}
		case PauseRemoteMoved:
			if err := e.repo.Fetch(ctx, e.st.Remote); err != nil {
				return e.pause(PauseRemoteMoved, "git fetch failed: "+err.Error(), "Check your network, then run twokey resume.", "push", nil)
			}
			remote, _ := e.repo.RevParse(ctx, "refs/remotes/"+e.st.Remote+"/"+e.st.Branch)
			if ok, _ := e.repo.IsAncestor(ctx, remote, "HEAD"); !ok {
				return e.pause(PauseRemoteMoved, fmt.Sprintf("%s/%s still has commits that your branch doesn't have.", e.st.Remote, e.st.Branch),
					"Bring them into your branch yourself (twokey never merges or rebases), then run twokey resume.", "push", nil)
			}
			r.RemoteSHA = remote
		}
	}
	if status != "" && fix == nil {
		return e.pause(PauseInvariant, "The working tree is not clean:"+indent(status),
			"Commit or discard the changes yourself, then run twokey resume.", "", nil)
	}
	return true
}

// loop runs steps until the run stops, pauses or the owner quits.
func (e *engine) loop(ctx context.Context) int {
	stop := e.beat.run(e.o.Out, e.o.Heartbeat, e.o.Now)
	defer stop()
	for !e.broken {
		if ctx.Err() != nil {
			return e.stop("Stopped (Ctrl+C).")
		}
		r := e.st.Current()
		var goOn bool
		switch {
		case !r.Synced:
			goOn = e.compareUpstream(ctx, r)
		case !r.Published:
			goOn = e.audits(ctx, r)
		case !r.IsDone(state.Convergence):
			goOn = e.convergence(ctx, r)
		default:
			g := e.decide(r)
			e.update(func() { r.Gate = &state.Gate{Action: string(g.Action), IDs: g.IDs, Notes: g.Notes} })
			switch g.Action {
			case gate.RunDispute:
				goOn = e.dispute(ctx, r, g.IDs)
			case gate.PauseAdjudicate, gate.PauseDecision, gate.PauseRecurring:
				goOn = e.ask(r, g)
			case gate.StopConverged:
				return e.finish(ctx, state.Converged)
			case gate.StopMaxRounds:
				return e.finish(ctx, state.MaxRounds)
			case gate.Fix:
				switch {
				case !r.IsDone(state.Fix):
					goOn = e.fix(ctx, r, g.IDs)
				case !r.Verified:
					goOn = e.verify(ctx, r)
				case !r.Pushed:
					goOn = e.push(ctx, r)
				default:
					goOn = e.nextRound(ctx)
				}
			default:
				goOn = e.pause(string(g.Action), "The gate stopped the run.", "Check the run folder, then run twokey resume.", "", g.IDs)
			}
		}
		if !goOn {
			return e.exitCode()
		}
	}
	return ExitError
}

func (e *engine) exitCode() int {
	switch {
	case e.broken:
		return ExitError
	case e.st.Status == state.Paused:
		return ExitPaused
	case e.st.Status == state.MaxRounds:
		return ExitMaxRounds
	default:
		return ExitDone
	}
}

// update changes the state and saves it, safely from parallel steps.
func (e *engine) update(f func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f()
	if err := state.Save(e.runDir, e.st); err != nil {
		if !e.broken {
			e.broken = true
			ui.Line(e.o.Out, ui.Fail, "Can't save the run state: "+err.Error())
		}
		return
	}
	e.stateHash, _ = logfile.Hash(filepath.Join(e.runDir, state.FileName))
}

func (e *engine) save() { e.update(func() {}) }

// stop saves the run as stopped by the owner; resume continues it.
func (e *engine) stop(msg string) int {
	e.update(func() { e.st.Status = state.Stopped })
	ui.Line(e.o.Out, ui.OK, msg+" The run is saved.")
	ui.Next(e.o.Out, "twokey resume")
	return ExitDone
}

// quit is a step's way to stop the loop when the owner pressed q.
func (e *engine) quit() bool {
	e.stop("Stopped by you.")
	return false
}

// pause stops the run until the owner acts (SPEC section 10): it writes
// pending.md and tells the owner what to do. It returns false.
func (e *engine) pause(reason, msg, next, step string, ids []string) bool {
	return e.pauseWith(&state.Pause{Reason: reason, Message: msg, Step: step, IDs: ids}, next)
}

func (e *engine) pauseWith(p *state.Pause, next string) bool {
	e.update(func() { e.st.Status, e.st.Pause = state.Paused, p })
	reason, msg := p.Reason, p.Message
	var b strings.Builder
	fmt.Fprintf(&b, "# twokey run %s is paused\n\n", e.st.RunID)
	fmt.Fprintf(&b, "**Why:** %s\n\n%s\n\n", reason, msg)
	if len(p.IDs) > 0 {
		fmt.Fprintf(&b, "**Findings:** %s\n\n", strings.Join(p.IDs, ", "))
	}
	fmt.Fprintf(&b, "**What twokey needs:** %s\n\n", next)
	fmt.Fprintf(&b, "**To continue:** run `twokey resume` in the repository.\n")
	if err := os.WriteFile(filepath.Join(e.runDir, "pending.md"), []byte(b.String()), 0o644); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't write pending.md: "+err.Error())
	}
	ui.Line(e.o.Out, ui.Pause, reason+": "+msg)
	ui.Next(e.o.Out, next)
	ui.Detail(e.o.Out, "Then run: twokey resume")
	return false
}

// nextRound starts the next round on the pushed commit.
func (e *engine) nextRound(ctx context.Context) bool {
	head, err := e.repo.RevParse(ctx, "HEAD")
	if err != nil {
		return e.pause(PauseInvariant, "Can't read HEAD: "+err.Error(), "Check the repository, then run twokey resume.", "", nil)
	}
	e.update(func() { e.st.NewRound(head, head) })
	ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Round %d starts on %s.", e.st.Current().N, short(head)))
	return true
}

// finish ends the run: report, a twokey log entry, and the summary.
func (e *engine) finish(ctx context.Context, status string) int {
	e.update(func() { e.st.Status, e.st.Pause = status, nil })
	report := e.report()
	var advice string
	if status == state.Converged {
		if advice = e.upstreamAdvice(ctx); advice != "" {
			report += "\n**Next:** " + advice + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(e.runDir, "report.md"), []byte(report), 0o644); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't write report.md: "+err.Error())
	}
	if _, err := e.insertLog(e.twokeyLabel(), 0, "report", report); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't add the report to the log: "+err.Error())
	}
	_ = os.RemoveAll(e.staging)
	if status == state.Converged {
		ui.Line(e.o.Out, ui.OK, fmt.Sprintf("Converged in round %d: both agents cleared %s.", e.st.Current().N, short(e.st.Current().Commit)))
		if advice != "" {
			ui.Next(e.o.Out, advice)
		}
	} else {
		ui.Line(e.o.Out, ui.Warn, fmt.Sprintf("Stopped after round %d, the last one, with findings left.", e.st.Current().N))
		ui.Next(e.o.Out, "read report.md, then twokey resume --more-rounds 1 if you want another round")
	}
	ui.Detail(e.o.Out, "Report: "+filepath.Join(e.runDir, "report.md"))
	return e.exitCode()
}

// insertLog writes one entry to the log (SPEC section 7) and returns its
// heading without "### ".
func (e *engine) insertLog(label string, round int, step, body string) (string, error) {
	entry := logfile.Entry{Label: label, Time: e.o.Now().UTC(), RunID: e.st.RunID, Round: round, Step: step, Body: body}
	if _, err := logfile.InsertFile(e.logPath, e.cfg.Project.LogMarker, entry, filepath.Join(e.runDir, "log-backups")); err != nil {
		return "", err
	}
	return strings.TrimPrefix(entry.Heading(), "### "), nil
}

func (e *engine) logFailed(err error) bool {
	var busy *logfile.BusyError
	next := "Fix the log file, then run twokey resume."
	if errors.As(err, &busy) {
		next = "Close the program that has the log file open, then run twokey resume."
	}
	return e.pause(PauseInvariant, "Can't write the log: "+err.Error(), next, "", nil)
}

func (e *engine) release(lk *lock.Lock) {
	if err := lk.Release(); err != nil {
		ui.Line(e.o.Out, ui.Warn, "Can't remove the lock file: "+err.Error())
		ui.Next(e.o.Out, "twokey unlock")
	}
}

func (e *engine) lockFailed(err error) {
	var held *lock.HeldError
	if errors.As(err, &held) {
		ui.Line(e.o.Out, ui.Fail, held.Error())
		if held.Alive {
			ui.Next(e.o.Out, "wait for that run, or stop it first.")
		} else {
			ui.Next(e.o.Out, "twokey unlock")
		}
		return
	}
	ui.Line(e.o.Out, ui.Fail, "Can't take the run lock: "+err.Error())
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func (e *engine) newRunID() string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(e.cfg.Checkpoint.Label), "-"), "-")
	if slug == "" {
		slug = "run"
	}
	base := e.o.Now().UTC().Format("20060102T1504Z") + "-" + slug
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(e.runRoot, "runs", id)); errors.Is(err, fs.ErrNotExist) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// cleanOldScratch deletes scratch folders older than maxAge (SPEC section 9).
func cleanOldScratch(root string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, en := range entries {
		if fi, err := en.Info(); err == nil && en.IsDir() && now.Sub(fi.ModTime()) > maxAge {
			_ = os.RemoveAll(filepath.Join(root, en.Name()))
		}
	}
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
	s = strings.TrimSpace(s)
	if s == "" {
		return " (nothing)"
	}
	return "\n  " + strings.ReplaceAll(s, "\n", "\n  ")
}
