package schedule_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schedule"
	"github.com/Sten-Qy-Li/twokey/internal/state"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

// fakeTasks records what Fire asks of Task Scheduler.
type fakeTasks struct{ calls []string }

func (f *fakeTasks) Register(_ context.Context, name string, _ []byte) error {
	f.calls = append(f.calls, "register "+name)
	return nil
}

func (f *fakeTasks) SetEnabled(_ context.Context, name string, on bool) error {
	if on {
		f.calls = append(f.calls, "enable "+name)
	} else {
		f.calls = append(f.calls, "disable "+name)
	}
	return nil
}

func (f *fakeTasks) Delete(_ context.Context, name string) error {
	f.calls = append(f.calls, "delete "+name)
	return nil
}

func (f *fakeTasks) Query(context.Context, string) schedule.Info { return schedule.Info{} }

// project is a throwaway repository with fake agents and a schedule.
type project struct {
	t        *testing.T
	repo     testutil.Repo
	cfg      *config.Config
	scenario string
	tasks    *fakeTasks
	out      bytes.Buffer
	now      time.Time
}

func newProject(t *testing.T) *project {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, ".gitignore", "local-only_*\n")
	r.Commit(t, "ignore local files")
	testutil.Git(t, r.Dir, "push", "--quiet", "origin", "feature")
	r.WriteFile(t, "local-only_req.md", "# Requirements\n")
	r.WriteFile(t, "local-only_logs.md", "# Log\n\n<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->\n")
	cfg := config.Default()
	cfg.Project.Branch = "feature"
	cfg.Checkpoint = config.Checkpoint{Label: "Milestone 1", RequirementsFile: "local-only_req.md"}
	cfg.Verify.Commands = []string{}
	p := &project{t: t, repo: r, cfg: cfg, scenario: t.TempDir(), tasks: &fakeTasks{},
		now: time.Date(2026, 11, 14, 2, 3, 0, 0, time.Local)}
	p.setSchedule(schedule.Schedule{On: true, Daily: []string{"02:00"}, Until: "2026-11-20"})
	return p
}

func (p *project) runRoot() string { return filepath.Join(p.repo.Dir, p.cfg.Project.RunDir) }

func (p *project) setSchedule(s schedule.Schedule) {
	p.t.Helper()
	if err := schedule.Save(p.runRoot(), s); err != nil {
		p.t.Fatal(err)
	}
}

func (p *project) file(round int, name, content string) {
	p.t.Helper()
	path := filepath.Join(p.scenario, "r"+string(rune('0'+round)), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// oneFix scripts a round that finds one problem and fixes it.
func (p *project) oneFix() {
	f := `{"id":"R1-CX-01","title":"problem","severity":"P1","kind":"repo_fix","evidence":"app.go:1","fix_hint":"fix it","options":[],"recommended_option":""}`
	m := `{"id":"R1-M-01","title":"problem","severity":"P1","kind":"repo_fix","evidence":"app.go:1","fix_hint":"fix it","options":[],"recommended_option":"",` +
		`"sources":["codex"],"merged_from":["R1-CX-01"],"status":"confirmed","dispute_reason":"","recurs_of":""}`
	p.file(1, "codex-audit.json", `{"audited_commit":"${commit}","verdict":"not_ready","summary":"s","findings":[`+f+`],"log_entry_markdown":"- audit"}`)
	p.file(1, "claude-audit.json", `{"audited_commit":"${commit}","verdict":"not_ready","summary":"s","findings":[],"log_entry_markdown":"- audit"}`)
	p.file(1, "convergence.json", `{"audited_commit":"${commit}","verdict":"not_ready","summary":"s","findings":[`+m+`],"log_entry_markdown":"- merged"}`)
	p.file(1, "fix.actions.json", `{"write":{"fixed.txt":"fixed\n"},"commit":"Fix R1-M-01"}`)
	p.file(1, "fix.json", `{"base_commit":"${commit}","commits":${new_commits},"done":[{"id":"R1-M-01","change":"c","verified_how":"v"}],"not_done":[],"log_entry_markdown":"- fix"}`)
	p.file(1, "fix.2.actions.json", `{"write":{"fixed2.txt":"fixed\n"},"commit":"Fix R1-M-01 again"}`)
	p.file(1, "fix.2.json", `{"base_commit":"${commit}","commits":${new_commits},"done":[{"id":"R1-M-01","change":"c","verified_how":"v"}],"not_done":[],"log_entry_markdown":"- fix"}`)
}

func (p *project) fire(test bool) int {
	p.t.Helper()
	p.out.Reset()
	o := engine.Options{
		Root: p.repo.Dir, Cfg: p.cfg, Proc: proc.OS{}, Out: &p.out,
		Runner:      runner.Fake{Dir: p.scenario, Git: proc.OS{}},
		StagingRoot: p.t.TempDir(), ScratchRoot: p.t.TempDir(),
		FakeAgents: true, Autopilot: true, Decisions: engine.Recommended,
		Now: func() time.Time { return p.now },
	}
	return schedule.Fire(p.t.Context(), schedule.Slot{Engine: o, Console: &p.out, Tasks: p.tasks, Task: "twokey work 123456", Test: test})
}

func (p *project) last() schedule.Record {
	p.t.Helper()
	h, err := schedule.History(p.runRoot())
	if err != nil || len(h) == 0 {
		p.t.Fatalf("history: %v, %v", h, err)
	}
	return h[len(h)-1]
}

func (p *project) runs() int {
	entries, _ := os.ReadDir(filepath.Join(p.runRoot(), "runs"))
	return len(entries)
}

// A start runs one round in autopilot and records what it did (D66).
func TestFireRunsOneRound(t *testing.T) {
	p := newProject(t)
	p.oneFix()
	if code := p.fire(false); code != engine.ExitDone {
		t.Fatalf("exit %d:\n%s", code, p.out.String())
	}
	r := p.last()
	head := testutil.Git(t, p.repo.Dir, "rev-parse", "HEAD")
	if r.Outcome != schedule.RoundDone || r.Round != 1 || r.Run == "" || !r.Planned.Equal(time.Date(2026, 11, 14, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("record: %+v", r)
	}
	want := []string{"Round 1: fixed R1-M-01, and pushed 1 commit (" + head[:7] + ").", "Its fix isn't audited yet: the next round checks it."}
	if strings.Join(r.Lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines:\n%s", strings.Join(r.Lines, "\n"))
	}
	log, err := os.ReadFile(r.Log)
	if err != nil || !strings.Contains(string(log), "scheduled round") || !strings.Contains(string(log), "Pushed 1 commit to origin/feature.") {
		t.Errorf("slot log (%v):\n%s", err, log)
	}
}

// Whatever is in the way, a start changes nothing and says why.
func TestFireSkips(t *testing.T) {
	t.Run("dirty tree", func(t *testing.T) {
		p := newProject(t)
		p.repo.WriteFile(t, "notes.txt", "mine")
		p.fire(false)
		if r := p.last(); r.Outcome != schedule.Skipped || !strings.Contains(r.Lines[0], "The working tree isn't clean (1 changed or new files), so you may be working in it.") || p.runs() != 0 {
			t.Errorf("record %+v, runs %d", r, p.runs())
		}
	})
	t.Run("another run", func(t *testing.T) {
		p := newProject(t)
		lk, err := lock.Acquire(p.runRoot(), lock.Info{PID: os.Getpid(), RunID: "other"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lk.Release() }()
		p.fire(false)
		if r := p.last(); r.Outcome != schedule.Skipped || !strings.HasPrefix(r.Lines[0], "Another twokey run is going (run other") {
			t.Errorf("record %+v", r)
		}
	})
	t.Run("off", func(t *testing.T) {
		p := newProject(t)
		p.setSchedule(schedule.Schedule{Daily: []string{"02:00"}})
		p.fire(false)
		if r := p.last(); r.Outcome != schedule.Skipped || r.Lines[0] != "The schedule is off." {
			t.Errorf("record %+v", r)
		}
	})
	t.Run("ended", func(t *testing.T) {
		p := newProject(t)
		p.now = time.Date(2026, 11, 21, 2, 0, 0, 0, time.Local)
		p.fire(false)
		s, _ := schedule.Load(p.runRoot())
		if r := p.last(); r.Outcome != schedule.Ended || s.On || strings.Join(p.tasks.calls, ",") != "disable twokey work 123456" {
			t.Errorf("record %+v, on %v, calls %v", r, s.On, p.tasks.calls)
		}
	})
	t.Run("a safety stop waits for the owner", func(t *testing.T) {
		p := newProject(t)
		p.cfg.Verify.Commands = []string{"git no-such-command"}
		p.oneFix()
		if code := p.fire(false); code != engine.ExitPaused {
			t.Fatalf("exit %d:\n%s", code, p.out.String())
		}
		if r := p.last(); r.Outcome != schedule.Paused || !strings.HasPrefix(r.Lines[0], "Round 1 paused: PAUSE_VERIFY_FAILED:") {
			t.Errorf("record %+v", r)
		}
		p.fire(false)
		if r := p.last(); r.Outcome != schedule.Skipped || !strings.Contains(r.Lines[0], "is paused (PAUSE_VERIFY_FAILED) and waits for you") {
			t.Errorf("record %+v", r)
		}
	})
	t.Run("test start", func(t *testing.T) {
		p := newProject(t)
		p.fire(true)
		if r := p.last(); r.Outcome != schedule.TestOK || !r.Test || p.runs() != 0 || strings.Join(p.tasks.calls, ",") != "delete twokey work 123456 (test)" {
			t.Errorf("record %+v, runs %d, calls %v", r, p.runs(), p.tasks.calls)
		}
	})
}

// The morning view: what ran since the owner last looked, what didn't
// start, and what waits for them.
func TestRender(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.Local)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 14, h, m, 0, 0, time.Local) }
	v := schedule.View{
		Schedule: schedule.Schedule{On: true, Daily: []string{"02:00", "05:00"}, Until: "2026-11-20",
			Since: at(1, 0).AddDate(0, 0, -1), LastLooked: at(1, 0)},
		Task: "twokey work 123456",
		Info: schedule.Info{Exists: true, Enabled: true, Exe: `C:\tools\twokey.exe`},
		Exe:  `C:\tools\twokey.exe`,
		History: []schedule.Record{
			{Planned: at(2, 0), Started: at(2, 1), Outcome: schedule.RoundDone, Round: 2,
				Lines: []string{"Round 2: fixed R2-M-01, and pushed 2 commits (abc1234).", "Automatic decision: R2-M-02 → A (twokey decide R2-M-02 <answer> changes it)."}},
		},
		Latest: &engine.RunSummary{State: &state.State{RunID: "run1", Status: state.RoundDone}, Dir: filepath.Join("work", "runs", "run1"),
			Waiting: []string{"- R2-M-03 (round 2): it needs your decision. To answer: `twokey decide R2-M-03 <answer>`"}},
		Now: now,
	}
	got := schedule.Render(v)
	for _, want := range []string{
		"Schedule:    on: every day at 02:00 and 05:00; until Fri 20 Nov",
		"Next start:  Sun 15 Nov 02:00",
		`Task:        "twokey work 123456" in Task Scheduler, enabled, runs C:\tools\twokey.exe`,
		"Since you last looked (Sat 14 Nov 01:00):",
		"  Sat 14 Nov 02:01  round done\n                    Round 2: fixed R2-M-01, and pushed 2 commits (abc1234).",
		"  Sat 14 Nov 05:00  didn't start: the PC was off, asleep, on battery or logged out",
		"Waiting for you:\n  R2-M-03 (round 2): it needs your decision.",
		"Report:      " + filepath.Join("work", "runs", "run1", "report.md"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the view lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[WARN]") || strings.Contains(got, "02:00  didn't start") {
		t.Errorf("unexpected warning or missed start:\n%s", got)
	}

	v.Info.Enabled, v.Exe = false, `C:\backup\twokey.exe`
	v.History, v.Schedule.LastLooked = nil, at(7, 0)
	got = schedule.Render(v)
	for _, want := range []string{"[WARN] The schedule is on, but the task is disabled", "[WARN] The task runs another twokey.exe than this one", "Since you last looked (Sat 14 Nov 07:00):\n  Nothing."} {
		if !strings.Contains(got, want) {
			t.Errorf("the view lacks %q:\n%s", want, got)
		}
	}
}
