package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

// TaskAPI is Task Scheduler, as twokey uses it. Tasks implements it.
type TaskAPI interface {
	Register(ctx context.Context, name string, xml []byte) error
	SetEnabled(ctx context.Context, name string, on bool) error
	Delete(ctx context.Context, name string) error
	Query(ctx context.Context, name string) Info
}

// Slot is what a scheduled start needs.
type Slot struct {
	// Engine is set up as for "twokey round --autopilot". Fire replaces
	// its Out and UI, so the slot's output also goes to a log file.
	Engine  engine.Options
	Console io.Writer
	Tasks   TaskAPI
	Task    string // the task's name
	Test    bool   // "twokey schedule test": the checks only, no agents
	// Notify sends the one notification of a start (D67); the round's own
	// notifications are off, so the owner gets one per start. nil = none.
	Notify func(title, message string)
	// Version is this twokey's version. A start refuses a development
	// build (D68). "" = don't check (tests).
	Version string
}

// resumable are the pauses a later slot may pick up by itself: a usage
// limit, a failed push (network or login), and questions autopilot
// answers. Every other pause is a safety stop that waits for the owner.
var resumable = []string{
	engine.PauseUsageLimit, engine.PausePushFailed, engine.PauseDecision,
	engine.PauseAdjudicate, engine.PauseRecurring, engine.PauseSkipped,
}

// Fire runs one scheduled start ("twokey schedule fire", D66): unless
// something is in the way, one round in autopilot. Whatever happens, it
// records the slot for the morning view and exits; it never waits at a
// prompt.
func Fire(ctx context.Context, sl Slot) int {
	o := sl.Engine
	now := o.Now
	if now == nil {
		now = time.Now
	}
	// A start that woke the computer holds it awake from the beginning,
	// not only once the round's loop runs (D64).
	if o.KeepAwake != nil {
		defer o.KeepAwake()()
	}
	runRoot := filepath.Join(o.Root, o.Cfg.Project.RunDir)
	started := now()
	rec := Record{Started: started, Test: sl.Test, Log: LogPath(runRoot, started.UTC().Format("20060102T150405Z"))}

	var buf bytes.Buffer
	outs := []io.Writer{sl.Console, &buf}
	if err := os.MkdirAll(filepath.Dir(rec.Log), 0o755); err == nil {
		if f, err := os.Create(rec.Log); err == nil {
			defer f.Close()
			outs = append(outs, f)
		} else {
			rec.Log = ""
		}
	}
	out := io.MultiWriter(outs...)
	what := "scheduled round"
	if sl.Test {
		what = "test of a scheduled start (no agents)"
	}
	fmt.Fprintf(out, "twokey %s: %s, %s\n\n", version.String(), what, started.Format("Mon 2 Jan 15:04"))

	finish := func(outcome string, lines ...string) int {
		rec.Outcome, rec.Lines, rec.Finished = outcome, lines, now()
		if err := Append(runRoot, rec); err != nil {
			ui.Line(out, ui.Warn, "Can't record this start in "+HistoryFile+": "+err.Error())
		}
		label := ui.OK
		if outcome == Skipped || outcome == Failed || outcome == Paused {
			label = ui.Warn
		}
		ui.Line(out, label, "This start: "+outcome)
		for _, l := range lines {
			ui.Detail(out, l)
		}
		ui.Detail(out, "twokey schedule status shows it with the other starts.")
		if sl.Notify != nil && outcome != NothingNew {
			sl.Notify("twokey: scheduled start", notice(rec))
		}
		switch outcome {
		case Paused:
			return engine.ExitPaused
		case Failed:
			return engine.ExitError
		}
		return engine.ExitDone
	}

	s, err := Load(runRoot)
	if err != nil {
		return finish(Failed, ui.Sentence(err.Error()))
	}
	if p, ok := s.Planned(started); ok {
		rec.Planned = p
	}
	switch {
	case !sl.Test && !s.On:
		return finish(Skipped, "The schedule is off.")
	case !sl.Test && s.Ended(started):
		s.On = false
		err := Save(runRoot, s)
		if err == nil && sl.Tasks != nil {
			err = sl.Tasks.SetEnabled(ctx, sl.Task, false)
		}
		lines := []string{fmt.Sprintf("The schedule ended on %s, so twokey switched it off.", s.Until)}
		if err != nil {
			lines = append(lines, "Switching off failed: "+err.Error()+". Run twokey schedule off.")
		}
		return finish(Ended, lines...)
	case sl.Version != "" && !version.Release(sl.Version):
		return finish(Failed, fmt.Sprintf("This twokey.exe is a development build (%s), and a schedule runs only a tagged release.", sl.Version),
			"Next: rebuild it from a tag, or point the schedule at a release build with twokey schedule program.")
	}
	rec.Outcome = Running
	_ = Append(runRoot, rec)

	if why := blocked(ctx, o, runRoot, out); why != "" {
		return finish(Skipped, why, "twokey changed nothing.")
	}
	if sl.Test {
		if sl.Tasks != nil {
			_ = sl.Tasks.Delete(ctx, CheckTaskName(sl.Task)) // it ran; Task Scheduler would delete it later anyway
		}
		return finish(TestOK, "Task Scheduler started twokey, and the checks passed: a round could start now. No agent ran.")
	}

	before := map[string]string{}
	if sum, err := engine.Latest(o); err == nil {
		for k, v := range sum.State.Automatic {
			before[k] = v
		}
	}
	o.Out, o.UI, o.Autopilot, o.Notify = out, engine.NewTextUI(strings.NewReader(""), out, true), true, nil
	code := engine.Round(ctx, o)
	sum, err := engine.Latest(o)
	if err != nil {
		return finish(Failed, failLine(buf.String()))
	}
	rec.Run, rec.Round = sum.State.RunID, len(sum.State.Rounds)
	outcome, lines := summarize(code, sum, before, buf.String())
	return finish(outcome, lines...)
}

// CheckTaskName is the name of "twokey schedule test"'s one-time task.
func CheckTaskName(task string) string { return task + " (test)" }

// blocked says why a slot can't start a round, without changing anything:
// another twokey is running, the owner may be working in the repository,
// or the latest run waits for the owner. A lock whose process has ended is
// removed, as twokey unlock would.
func blocked(ctx context.Context, o engine.Options, runRoot string, out io.Writer) string {
	if info, err := lock.Read(runRoot); err == nil {
		if lock.Alive(info.PID) {
			return fmt.Sprintf("Another twokey run is going (run %s, process %d).", info.RunID, info.PID)
		}
		if _, err := lock.Remove(runRoot); err == nil {
			ui.Line(out, ui.OK, fmt.Sprintf("Removed the stale lock of run %s (process %d had stopped).", info.RunID, info.PID))
		}
	}
	repo := gitx.Repo{Dir: o.Root, R: o.Proc}
	branch := o.Cfg.Project.Branch
	if cur, err := repo.CurrentBranch(ctx); err != nil || cur != branch {
		return fmt.Sprintf("The repository isn't on %s, so you may be working in it.", branch)
	}
	if st, err := repo.Status(ctx); err != nil || st != "" {
		n := len(strings.Split(strings.TrimSpace(st), "\n"))
		return fmt.Sprintf("The working tree isn't clean (%d changed or new files), so you may be working in it.", n)
	}
	sum, err := engine.Latest(o)
	if err == nil && sum.State.Status == state.Paused && sum.State.Pause != nil && !slices.Contains(resumable, sum.State.Pause.Reason) {
		msg, _, _ := strings.Cut(sum.State.Pause.Message, "\n")
		return fmt.Sprintf("Run %s is paused (%s) and waits for you. %s", sum.State.RunID, sum.State.Pause.Reason, msg)
	}
	if err != nil && !errors.Is(err, engine.ErrNoRuns) {
		return "Can't read the latest run: " + ui.Sentence(err.Error())
	}
	return ""
}

// summarize turns a round's result into the morning view's lines.
func summarize(code int, sum engine.RunSummary, before map[string]string, output string) (string, []string) {
	st := sum.State
	r := st.Current()
	var lines []string
	switch {
	case code == engine.ExitRoundDone:
		lines = append(lines, fixLine(r))
	case code == engine.ExitDone && strings.Contains(output, "There is nothing new to audit."):
		return NothingNew, []string{fmt.Sprintf("Run %s converged on %s, and the branch hasn't moved since. Nothing to audit.", st.RunID, short(r.Commit))}
	case code == engine.ExitDone && st.Status == state.Converged:
		lines = append(lines, fmt.Sprintf("Round %d: both agents cleared %s. Run %s is done.", r.N, short(r.Commit), st.RunID))
	case code == engine.ExitDone && st.Status == state.Stopped:
		lines = append(lines, fmt.Sprintf("Stopped in round %d (twokey stop or Ctrl+C). twokey round continues it.", r.N))
	case code == engine.ExitPaused && st.Pause != nil:
		msg, _, _ := strings.Cut(st.Pause.Message, "\n")
		lines = append(lines, fmt.Sprintf("Round %d paused: %s: %s", r.N, st.Pause.Reason, msg), "pending.md in "+sum.Dir+" says what twokey needs.")
	default:
		return Failed, []string{failLine(output)}
	}
	ids := make([]string, 0, len(st.Automatic))
	for id := range st.Automatic {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if ans := st.Automatic[id]; before[id] != ans {
			kind := "ruling"
			if st.Decisions[id] == ans {
				kind = "decision"
			}
			lines = append(lines, fmt.Sprintf("Automatic %s: %s → %s (twokey decide %s <answer> changes it).", kind, id, ans, id))
		}
	}
	for _, s := range r.Skipped {
		lines = append(lines, fmt.Sprintf("Waiting for you: %s: %s.", s.ID, s.Why))
	}
	switch {
	case code == engine.ExitRoundDone:
		lines = append(lines, "Its fix isn't audited yet: the next round checks it.")
		return RoundDone, lines
	case code == engine.ExitPaused:
		return Paused, lines
	case st.Status == state.Stopped:
		return Stopped, lines
	}
	return Converged, lines
}

// notice is a start's notification: the outcome, the run and the round,
// counts instead of findings text (an ntfy topic can be read by anyone who
// knows it), and where to look.
func notice(r Record) string {
	first := ""
	if len(r.Lines) > 0 {
		first, _, _ = strings.Cut(r.Lines[0], ". ")
		first = strings.TrimSuffix(first, ".") + "."
	}
	var auto, waiting int
	for _, l := range r.Lines {
		switch {
		case strings.HasPrefix(l, "Automatic "):
			auto++
		case strings.HasPrefix(l, "Waiting for you: "):
			waiting++
		}
	}
	var b strings.Builder
	switch r.Outcome {
	case RoundDone, Converged, Paused, Stopped:
		fmt.Fprintf(&b, "Run %s: %s", r.Run, first)
		if r.Outcome == Paused {
			b.Reset()
			fmt.Fprintf(&b, "Run %s paused in round %d.", r.Run, r.Round)
		}
		if auto > 0 {
			fmt.Fprintf(&b, " %d automatic decisions.", auto)
		}
		if waiting > 0 {
			fmt.Fprintf(&b, " %d findings wait for you.", waiting)
		}
	case Skipped:
		b.WriteString("Skipped: " + first)
	case TestOK:
		b.WriteString("The test start passed: a round could start now.")
	case Ended:
		b.WriteString("The schedule ended and is off now.")
	default:
		b.WriteString("The start failed.")
	}
	b.WriteString(" Next: twokey schedule status")
	return strings.Replace(strings.Replace(b.String(), " 1 automatic decisions.", " 1 automatic decision.", 1), " 1 findings wait", " 1 finding waits", 1)
}

// fixLine says what a round fixed and pushed.
func fixLine(r *state.Round) string {
	var f schema.FixResult
	if s := r.Step(state.Fix); s != nil {
		if data, err := os.ReadFile(s.Result); err == nil {
			_ = json.Unmarshal(data, &f)
		}
	}
	var done, notDone []string
	for _, d := range f.Done {
		done = append(done, d.ID)
	}
	for _, n := range f.NotDone {
		notDone = append(notDone, n.ID)
	}
	line := fmt.Sprintf("Round %d: fixed %s, and pushed %d commits (%s).", r.N, orNone(done), len(f.Commits), short(r.PushedSHA))
	if len(f.Commits) == 1 {
		line = strings.Replace(line, "1 commits", "1 commit", 1)
	}
	if len(notDone) > 0 {
		line += " Not done: " + strings.Join(notDone, ", ") + " (the report says why)."
	}
	return line
}

// failLine picks the first [FAIL] line of the slot's output.
func failLine(output string) string {
	for _, l := range strings.Split(output, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "[FAIL]"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return "twokey stopped with an error. The slot's log has the details."
}

func orNone(ids []string) string {
	if len(ids) == 0 {
		return "nothing"
	}
	return andList(ids)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
