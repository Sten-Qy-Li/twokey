package schedule

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/state"
)

// View is what the morning view shows ("twokey schedule status").
type View struct {
	Schedule Schedule
	Task     string // the task's name
	Info     Info   // what Task Scheduler has
	Exe      string // this twokey.exe
	ExeGone  bool   // the task's program doesn't exist
	History  []Record
	Latest   *engine.RunSummary // nil without runs
	Running  string             // "run X (process N)" while a twokey holds the lock
	Now      time.Time
}

// missedAfter is how long after a planned start a slot without a record
// counts as missed.
const missedAfter = 10 * time.Minute

// Render builds the morning view: the schedule, what the starts since the
// owner last looked did (and which didn't happen), and what waits for the
// owner.
func Render(v View) string {
	var b strings.Builder
	s := v.Schedule
	when := func(t time.Time) string { return t.In(v.Now.Location()).Format("Mon 2 Jan 15:04") }

	onOff := "off"
	if s.On {
		onOff = "on"
	}
	fmt.Fprintf(&b, "Schedule:    %s: %s\n", onOff, s.Describe())
	if s.On {
		if next, ok := s.Next(v.Now); ok {
			fmt.Fprintf(&b, "Next start:  %s\n", when(next))
		} else {
			b.WriteString("Next start:  none (no start time is left before the end date)\n")
		}
	}
	switch {
	case !v.Info.Exists && s.On:
		fmt.Fprintf(&b, "[WARN] The task %q isn't in Task Scheduler, so nothing starts.\n       Next: twokey schedule on\n", v.Task)
	case !v.Info.Exists:
		b.WriteString("Task:        none in Task Scheduler\n")
	default:
		on := "enabled"
		if !v.Info.Enabled {
			on = "disabled"
		}
		fmt.Fprintf(&b, "Task:        %q in Task Scheduler, %s, runs %s\n", v.Task, on, v.Info.Exe)
		switch {
		case s.On && !v.Info.Enabled:
			b.WriteString("[WARN] The schedule is on, but the task is disabled, so nothing starts.\n       Next: twokey schedule on\n")
		case !s.On && v.Info.Enabled:
			b.WriteString("[WARN] The schedule is off, but the task is enabled.\n       Next: twokey schedule off\n")
		}
		if v.ExeGone {
			b.WriteString("[FAIL] The task's twokey.exe doesn't exist any more, so every start fails.\n       Next: twokey schedule on (with the twokey you want it to run)\n")
		} else if v.Exe != "" && !strings.EqualFold(filepath.Clean(v.Info.Exe), filepath.Clean(v.Exe)) {
			fmt.Fprintf(&b, "[WARN] The task runs another twokey.exe than this one (%s).\n       Next: twokey schedule on, to point it at this one\n", v.Exe)
		}
	}
	if v.Running != "" {
		fmt.Fprintf(&b, "Running now: %s. To stop it safely: twokey stop\n", v.Running)
	}

	since := s.LastLooked
	b.WriteString("\n")
	items := v.items(since)
	switch {
	case since.IsZero():
		b.WriteString("Starts so far:\n")
	default:
		fmt.Fprintf(&b, "Since you last looked (%s):\n", when(since))
	}
	if len(items) == 0 {
		b.WriteString("  Nothing.\n")
		if last := lastRecord(v.History); last != nil {
			fmt.Fprintf(&b, "  The last start was %s: %s.\n", when(last.Started), label(*last, v.Running != ""))
		}
	}
	for _, it := range items {
		fmt.Fprintf(&b, "  %s  %s\n", when(it.at), it.head)
		for _, l := range it.lines {
			fmt.Fprintf(&b, "%-20s%s\n", "", l)
		}
	}

	b.WriteString("\nWaiting for you:\n")
	waiting := false
	if l := v.Latest; l != nil {
		st := l.State
		if st.Status == state.Paused && st.Pause != nil {
			msg, _, _ := strings.Cut(st.Pause.Message, "\n")
			fmt.Fprintf(&b, "  Run %s is paused (%s): %s\n  pending.md in %s says what twokey needs.\n", st.RunID, st.Pause.Reason, msg, l.Dir)
			waiting = true
		}
		for _, w := range l.Waiting {
			fmt.Fprintf(&b, "  %s\n", strings.TrimPrefix(w, "- "))
			waiting = true
		}
	}
	if !waiting {
		b.WriteString("  Nothing.\n")
	}
	if l := v.Latest; l != nil {
		fmt.Fprintf(&b, "Report:      %s\n", filepath.Join(l.Dir, "report.md"))
	}
	return b.String()
}

type item struct {
	at    time.Time
	head  string
	lines []string
}

// items lists the starts after since, and the planned starts that didn't
// happen, oldest first.
func (v View) items(since time.Time) []item {
	var out []item
	planned := map[time.Time]bool{}
	for _, r := range v.History {
		if !r.Planned.IsZero() {
			planned[r.Planned.UTC()] = true
		}
		if !r.Started.After(since) {
			continue
		}
		out = append(out, item{at: r.Started, head: label(r, v.Running != ""), lines: r.Lines})
	}
	if v.Schedule.On {
		from := since
		if v.Schedule.Since.After(from) {
			from = v.Schedule.Since
		}
		for _, t := range v.Schedule.Times(from, v.Now.Add(-missedAfter)) {
			if !planned[t.UTC()] {
				out = append(out, item{at: t, head: "didn't start: the PC was off, asleep, on battery or logged out"})
			}
		}
	}
	for i := 1; i < len(out); i++ { // a few items: insertion sort keeps equal times in order
		for j := i; j > 0 && out[j].at.Before(out[j-1].at); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func lastRecord(h []Record) *Record {
	if len(h) == 0 {
		return nil
	}
	return &h[len(h)-1]
}

// label names a record's outcome.
func label(r Record, running bool) string {
	head := map[string]string{
		RoundDone:  "round done",
		Converged:  "converged",
		NothingNew: "nothing new to audit",
		Paused:     "paused",
		Stopped:    "stopped",
		Skipped:    "skipped",
		Failed:     "failed",
		Ended:      "the schedule ended",
		TestOK:     "test passed",
	}[r.Outcome]
	if r.Outcome == Running {
		head = "started, without a result: twokey stopped unexpectedly (a closed window, a crash or a restart). twokey status shows the run"
		if running {
			head = "running now"
		}
	}
	if head == "" {
		head = r.Outcome
	}
	if r.Test && r.Outcome != TestOK {
		head = "test: " + head
	}
	return head
}
