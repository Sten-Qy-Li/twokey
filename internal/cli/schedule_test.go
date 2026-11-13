package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/schedule"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

// fakeScheduler runs git for real and plays schtasks.exe, keeping the
// registered tasks in memory.
type fakeScheduler struct {
	tasks map[string]string // name -> task XML, as schtasks /Query prints it
	calls []string
}

func (f *fakeScheduler) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (f *fakeScheduler) Run(ctx context.Context, c proc.Cmd) proc.Result {
	if c.Name != "schtasks.exe" {
		return proc.OS{}.Run(ctx, c)
	}
	f.calls = append(f.calls, strings.Join(c.Args[:3], " "))
	name := c.Args[2]
	fail := proc.Result{ExitCode: 1, Err: os.ErrNotExist, Stderr: "ERROR: The system cannot find the file specified."}
	switch c.Args[0] {
	case "/Create":
		data, err := os.ReadFile(c.Args[4])
		if err != nil {
			return proc.Result{ExitCode: 1, Err: err}
		}
		u := make([]uint16, (len(data)-2)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(data[2+2*i:])
		}
		f.tasks[name] = string(utf16.Decode(u))
	case "/Change":
		x, ok := f.tasks[name]
		if !ok {
			return fail
		}
		on, off := "<Enabled>true</Enabled>\r\n    <Hidden>", "<Enabled>false</Enabled>\r\n    <Hidden>"
		if c.Args[3] == "/ENABLE" {
			f.tasks[name] = strings.Replace(x, off, on, 1)
		} else {
			f.tasks[name] = strings.Replace(x, on, off, 1)
		}
	case "/Delete":
		delete(f.tasks, name)
	case "/Query":
		x, ok := f.tasks[name]
		if !ok {
			return fail
		}
		return proc.Result{Stdout: x}
	}
	return proc.Result{}
}

func TestScheduleCommands(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, ".gitignore", "local-only_*\n")
	r.Commit(t, "ignore local files")
	r.WriteFile(t, "local-only_twokey.toml", "[project]\nbranch = \"feature\"\n\n[checkpoint]\nlabel = \"Milestone 1\"\n"+
		"requirements_file = \"local-only_req.md\"\n\n[verify]\ncommands = []\n")
	f := &fakeScheduler{tasks: map[string]string{}}
	dir := t.TempDir()
	exe, dev := filepath.Join(dir, "release", "twokey.exe"), filepath.Join(dir, "bin", "twokey.exe")
	versions := map[string]string{exe: "v0.2.0-rc.8", dev: "v0.2.1-0.20261113150000-156bb60ad32f"}
	exeVersion := func(file string) (string, error) {
		if v, ok := versions[file]; ok {
			return v, nil
		}
		return version.OfFile(file)
	}
	self := exe // the twokey.exe that runs the command
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)
	goos := "windows"
	sched := func(args ...string) (string, int) {
		t.Helper()
		var out bytes.Buffer
		code := Run(t.Context(), append([]string{"schedule"}, args...), Env{Dir: r.Dir, Out: &out, Err: &out, Runner: f,
			GOOS: goos, Now: func() time.Time { return now }, Exe: self, ExeVersion: exeVersion})
		return out.String(), code
	}
	expect := func(args []string, code int, want ...string) {
		t.Helper()
		out, got := sched(args...)
		if got != code {
			t.Errorf("schedule %s: exit %d, want %d:\n%s", strings.Join(args, " "), got, code, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("schedule %s: output lacks %q:\n%s", strings.Join(args, " "), w, out)
			}
		}
	}
	// twokey takes the root from git, which gives the long form of a path
	// that the temp folder may spell short (RUNNER~1 on CI).
	top := filepath.FromSlash(testutil.Git(t, r.Dir, "rev-parse", "--show-toplevel"))
	name := schedule.TaskName(top)

	expect([]string{"on"}, 1, "The schedule has no start times yet.")
	expect([]string{"add", "02:00"}, 0, "[OK]    Schedule (off): every day at 02:00.", "Next: twokey schedule on")
	expect([]string{"add", "2026-11-12", "09:00"}, 1, "2026-11-12 09:00 has already passed.")
	expect([]string{"add", "25:00"}, 2, `"25:00" isn't a time.`)
	expect([]string{"until", "2026-11-20"}, 0, "until Fri 20 Nov")
	if len(f.calls) != 0 {
		t.Fatalf("an off schedule must not touch Task Scheduler: %v", f.calls)
	}

	// A schedule runs a tagged release build, not the development build
	// (D68).
	self = dev
	expect([]string{"on"}, 1, "[FAIL]  "+dev+" is a development build (v0.2.1-0.20261113150000-156bb60ad32f), and a schedule runs only a tagged release.",
		"Next: twokey schedule program <a release build of twokey.exe>")
	expect([]string{"program", dev}, 1, "is a development build")
	expect([]string{"program", filepath.Join(dir, "nothing.exe")}, 1, "[FAIL]  "+filepath.Join(dir, "nothing.exe")+" doesn't exist.")
	expect([]string{"program", exe}, 0, "Schedule (off): every day at 02:00; until Fri 20 Nov.", "The starts run "+exe+" (v0.2.0-rc.8).")
	if len(f.calls) != 0 {
		t.Fatalf("an off schedule must not touch Task Scheduler: %v", f.calls)
	}
	expect([]string{"on"}, 0, "The schedule is on: every day at 02:00; until Fri 20 Nov. Next start: Sat 14 Nov 02:00.",
		"with "+exe+" (v0.2.0-rc.8)", "Next: twokey schedule test")
	cfg := filepath.Join(top, "local-only_twokey.toml")
	x := f.tasks[name]
	for _, want := range []string{"<Command>" + exe + "</Command>", `<Arguments>schedule fire --config "` + cfg + `"</Arguments>`,
		"<StartBoundary>2026-11-13T02:00:00</StartBoundary>", "<Enabled>true</Enabled>\r\n    <Hidden>"} {
		if !strings.Contains(x, want) {
			t.Errorf("task lacks %q:\n%s", want, x)
		}
	}
	expect([]string{"add", "13:30"}, 0, "Schedule (on): every day at 02:00 and 13:30")
	if !strings.Contains(f.tasks[name], "T13:30:00") {
		t.Error("adding a time to a schedule that is on must update the task")
	}
	expect([]string{"status"}, 0, "Schedule:    on: every day at 02:00 and 13:30; until Fri 20 Nov", "Next start:  Sat 14 Nov 02:00",
		"Program:     "+exe+" (v0.2.0-rc.8)\nTask:        \""+name+"\" in Task Scheduler, enabled\n", "Starts so far:\n  Nothing.")

	expect([]string{"off"}, 0, "The schedule is off. No new round starts.")
	if !strings.Contains(f.tasks[name], "<Enabled>false</Enabled>\r\n    <Hidden>") {
		t.Error("off must disable the task")
	}
	out, _ := sched("status")
	if !strings.Contains(out, "Schedule:    off:") || !strings.Contains(out, "disabled") || strings.Contains(out, "[WARN]") || strings.Contains(out, "[FAIL]") {
		t.Errorf("status:\n%s", out)
	}

	expect([]string{"test"}, 0, "Task Scheduler starts a test at 15:02.")
	if x := f.tasks[schedule.CheckTaskName(name)]; !strings.Contains(x, "schedule fire --test") || !strings.Contains(x, "<Command>"+exe+"</Command>") {
		t.Errorf("test task:\n%s", x)
	}
	expect([]string{"remove", "all"}, 0, "Schedule (off): no start times.")
	expect([]string{"remove", "02:00"}, 1, "The schedule has no start time 02:00.")
	expect([]string{"bogus"}, 2, `unknown command "bogus"`)
	goos = "linux"
	expect([]string{"on"}, 1, "Schedules use Windows Task Scheduler, so they work only on Windows.")
}
