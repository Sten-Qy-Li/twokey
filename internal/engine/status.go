package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/state"
)

// Status describes a run on one screen ("twokey status", SPEC section 12):
// run, round, steps, elapsed time and the last gate result. runID "" means
// the latest run.
func Status(o Options, runID string) (string, error) {
	e := newEngine(o)
	dir, err := FindRun(e.runRoot, runID)
	if err != nil {
		return "", err
	}
	if e.st, err = state.Load(dir); err != nil {
		return "", err
	}
	st, now := e.st, e.o.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s (%s mode)\n", st.RunID, st.Mode)

	status := st.Status
	if lk, err := lock.Read(e.runRoot); err == nil && lk.RunID == st.RunID {
		if lock.Alive(lk.PID) {
			status = fmt.Sprintf("running (process %d)", lk.PID)
		} else if st.Status == state.Running {
			status = "stopped unexpectedly (a stale lock is left)"
		}
	}
	fmt.Fprintf(&b, "Status:     %s\n", status)
	if st.Pause != nil && st.Status == state.Paused {
		msg, _, _ := strings.Cut(st.Pause.Message, "\n")
		fmt.Fprintf(&b, "Paused:     %s: %s\n", st.Pause.Reason, msg)
	}
	fmt.Fprintf(&b, "Started:    %s (%s ago)\n", st.Started.Local().Format("Mon 2 Jan 15:04"), duration(st.Started, now))
	if r := st.Current(); r != nil {
		fmt.Fprintf(&b, "Round:      %d of at most %d, commit %s\n", r.N, st.MaxRounds, short(r.Commit))
		for _, s := range r.Steps {
			end := s.Finished
			if s.Status == state.Active {
				end = now
			}
			fmt.Fprintf(&b, "  %-13s %-8s %-9s %s\n", s.Name, s.Status, duration(s.Started, end), tries(s.Attempts))
		}
		if r.Gate != nil {
			fmt.Fprintf(&b, "Last gate:  %s\n", strings.TrimSpace(r.Gate.Action+" "+strings.Join(r.Gate.IDs, ", ")))
		}
	}
	fmt.Fprintf(&b, "Run folder: %s\n", dir)
	switch st.Status {
	case state.Paused, state.Stopped:
		b.WriteString("Next:       twokey resume\n")
		if _, err := os.Stat(filepath.Join(dir, "pending.md")); err == nil && st.Status == state.Paused {
			b.WriteString("            (pending.md in the run folder says what twokey needs)\n")
		}
	case state.MaxRounds:
		b.WriteString("Next:       read report.md, then twokey resume --more-rounds 1 if you want another round\n")
	case state.RoundDone:
		b.WriteString("Next:       twokey round (its audits check the fix that isn't audited yet)\n")
	}
	return b.String(), nil
}

// RunSummary is the latest run, for the morning view ("twokey schedule
// status").
type RunSummary struct {
	State   *state.State
	Dir     string   // the run folder
	Waiting []string // findings autopilot skipped that wait for the owner (D61)
}

// Latest returns the latest run. It wraps ErrNoRuns if there is none.
func Latest(o Options) (RunSummary, error) {
	e := newEngine(o)
	dir, err := FindRun(e.runRoot, "")
	if err != nil {
		return RunSummary{}, err
	}
	if e.st, err = state.Load(dir); err != nil {
		return RunSummary{}, err
	}
	return RunSummary{State: e.st, Dir: dir, Waiting: e.waiting()}, nil
}

// Report rebuilds a run's report.md ("twokey report") and returns its path.
// It doesn't add another log entry.
func Report(ctx context.Context, o Options, runID string) (string, error) {
	e := newEngine(o)
	dir, err := FindRun(e.runRoot, runID)
	if err != nil {
		return "", err
	}
	if e.st, err = state.Load(dir); err != nil {
		return "", err
	}
	e.setRun(e.st.RunID)
	report := e.report()
	if e.st.Status == state.Converged {
		if advice := e.upstreamAdvice(ctx); advice != "" {
			report += "\n**Next:** " + advice + "\n"
		}
	}
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func tries(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}
