package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/initcmd"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/schedule"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

const scheduleUsage = `Usage: twokey schedule <command> [options]

Starts one round (twokey round --autopilot) at set times, with Windows Task
Scheduler. Each round runs unattended and makes decisions by itself.

Commands:
  add <HH:MM>               add a start time, every day
  add <YYYY-MM-DD> <HH:MM>  add a start time, once
  remove <time> | all       remove a start time
  until <YYYY-MM-DD> | none the last day a round may start; then the schedule switches itself off
  program <twokey.exe>      the twokey.exe the starts run: a tagged release build, not your development build
  on                        start the rounds at those times (with this twokey.exe, unless program set one)
  off                       no new rounds; a round that is running carries on (twokey stop stops it)
  status                    the schedule, what the last starts did, and what waits for you
  test                      a start in 2 minutes that runs the checks only, without agents

Options: --config <file> (before the other arguments)
`

// scheduleCmd is what every schedule command needs.
type scheduleCmd struct {
	env     Env
	root    string
	cfg     *config.Config
	cfgFile string // absolute: the task passes it to schedule fire
	runRoot string
	name    string // the task's name
	tasks   schedule.Tasks
	now     time.Time
}

func runSchedule(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Err, scheduleUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help", "help":
		fmt.Fprint(env.Out, scheduleUsage)
		return 0
	case "add", "remove", "until", "program", "on", "off", "status", "test", "fire":
	default:
		fmt.Fprintf(env.Err, "twokey schedule: unknown command %q.\n\n%s", sub, scheduleUsage)
		return 2
	}
	fs := flag.NewFlagSet("schedule "+sub, flag.ContinueOnError)
	fs.SetOutput(env.Err)
	fs.Usage = func() { fmt.Fprint(env.Err, scheduleUsage) }
	configPath := fs.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	test := fs.Bool("test", false, "fire: run the checks only (twokey schedule test)")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	root, cfg, ok := project(ctx, env, *configPath)
	if !ok {
		return 1
	}
	cfgFile, err := filepath.Abs(initcmd.ConfigPath(env.Dir, root, *configPath))
	if err != nil {
		ui.Line(env.Out, ui.Fail, ui.Sentence(err.Error()))
		return 1
	}
	runRoot := filepath.Join(root, cfg.Project.RunDir)
	c := scheduleCmd{env: env, root: root, cfg: cfg, cfgFile: cfgFile, runRoot: runRoot,
		name: schedule.TaskName(root), tasks: schedule.Tasks{R: env.Runner, Dir: runRoot}, now: time.Now()}
	if env.Now != nil {
		c.now = env.Now()
	}
	goos := env.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "windows" && sub != "status" {
		ui.Line(env.Out, ui.Fail, "Schedules use Windows Task Scheduler, so they work only on Windows.")
		ui.Next(env.Out, "run twokey round yourself, or with your system's scheduler.")
		return 1
	}
	switch sub {
	case "fire":
		return c.fire(ctx, *test)
	case "add", "remove", "until", "program":
		return c.change(ctx, sub, fs.Args())
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Err, "twokey schedule %s: unexpected argument %q\n", sub, fs.Arg(0))
		return 2
	}
	switch sub {
	case "on":
		return c.on(ctx)
	case "off":
		return c.off(ctx)
	case "test":
		return c.test(ctx)
	}
	return c.status(ctx)
}

func (c scheduleCmd) load() (schedule.Schedule, bool) {
	s, err := schedule.Load(c.runRoot)
	if err != nil {
		ui.Line(c.env.Out, ui.Fail, ui.Sentence(err.Error()))
		ui.Next(c.env.Out, "fix or delete "+filepath.Join(c.runRoot, schedule.FileName)+", then try again.")
		return s, false
	}
	return s, true
}

func (c scheduleCmd) save(s schedule.Schedule) bool {
	if err := schedule.Save(c.runRoot, s); err != nil {
		ui.Line(c.env.Out, ui.Fail, "Can't save the schedule: "+err.Error())
		return false
	}
	return true
}

// self is this twokey.exe.
func (c scheduleCmd) self() (string, error) {
	if c.env.Exe != "" {
		return c.env.Exe, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func (c scheduleCmd) version(exe string) (string, error) {
	if c.env.ExeVersion != nil {
		return c.env.ExeVersion(exe)
	}
	return version.OfFile(exe)
}

// program is the twokey.exe the starts run (D68): the schedule's, else
// this one. It must be a tagged release build, so a night run never picks
// up a development build, and rebuilding one never fails because a start
// holds the file.
func (c scheduleCmd) program(s schedule.Schedule) (exe, ver string, ok bool) {
	exe = s.Program
	if exe == "" {
		var err error
		if exe, err = c.self(); err != nil {
			ui.Line(c.env.Out, ui.Fail, "Can't find this twokey.exe: "+err.Error())
			return "", "", false
		}
	}
	ok = c.release(exe, &ver)
	return exe, ver, ok
}

// release checks that exe is a release build of twokey, and says what to
// do if it isn't.
func (c scheduleCmd) release(exe string, ver *string) bool {
	out := c.env.Out
	v, err := c.version(exe)
	switch {
	case err != nil:
		ui.Line(out, ui.Fail, ui.Sentence(err.Error())+".")
	case strings.Contains(strings.ToLower(exe), "go-build"):
		ui.Line(out, ui.Fail, exe+" is a temporary build (go run), which Task Scheduler can't start later.")
	case !version.Release(v):
		ui.Line(out, ui.Fail, fmt.Sprintf("%s is a development build (%s), and a schedule runs only a tagged release.", exe, v))
	default:
		*ver = v
		return true
	}
	ui.Next(out, "twokey schedule program <a release build of twokey.exe>. The README's \"Scheduled rounds\" says how to build one.")
	return false
}

// task is what the Task Scheduler task runs.
func (c scheduleCmd) task(s schedule.Schedule) (schedule.Task, bool) {
	exe, _, ok := c.program(s)
	return schedule.Task{Exe: exe, Repo: c.root, Config: c.cfgFile}, ok
}

// sync makes the task match a schedule that is on. With no start times
// left, the task goes and the schedule is off.
func (c scheduleCmd) sync(ctx context.Context, s *schedule.Schedule) bool {
	if !s.On {
		return true
	}
	if len(s.Daily)+len(s.Once) == 0 {
		s.On = false
		if c.tasks.Query(ctx, c.name).Exists {
			if err := c.tasks.Delete(ctx, c.name); err != nil {
				ui.Line(c.env.Out, ui.Fail, "Can't remove the task: "+err.Error())
				return false
			}
		}
		ui.Line(c.env.Out, ui.OK, "No start time is left, so the schedule is off.")
		return true
	}
	t, ok := c.task(*s)
	if !ok {
		return false
	}
	if err := c.tasks.Register(ctx, c.name, schedule.TaskXML(*s, t, true, c.now)); err != nil {
		ui.Line(c.env.Out, ui.Fail, "Can't update the task in Task Scheduler: "+err.Error())
		return false
	}
	return true
}

func (c scheduleCmd) change(ctx context.Context, sub string, args []string) int {
	s, ok := c.load()
	if !ok {
		return 1
	}
	out := c.env.Out
	note := ""
	switch sub {
	case "add":
		if len(args) == 0 {
			fmt.Fprintln(c.env.Err, "Usage: twokey schedule add <HH:MM> | <YYYY-MM-DD> <HH:MM>")
			return 2
		}
		daily, value, err := schedule.ParseSlot(args)
		if err != nil {
			ui.Line(out, ui.Fail, ui.Sentence(err.Error()))
			return 2
		}
		if !daily {
			if t, _ := time.ParseInLocation("2006-01-02 15:04", value, c.now.Location()); !t.After(c.now) {
				ui.Line(out, ui.Fail, value+" has already passed.")
				return 1
			}
		}
		if !s.Add(daily, value) {
			ui.Line(out, ui.OK, "The schedule already has "+value+".")
			return 0
		}
	case "remove":
		if len(args) == 0 {
			fmt.Fprintln(c.env.Err, "Usage: twokey schedule remove <HH:MM> | <YYYY-MM-DD> <HH:MM> | all")
			return 2
		}
		daily, value := false, "all"
		if len(args) != 1 || args[0] != "all" {
			var err error
			if daily, value, err = schedule.ParseSlot(args); err != nil {
				ui.Line(out, ui.Fail, ui.Sentence(err.Error()))
				return 2
			}
		}
		if !s.Remove(daily, value) {
			ui.Line(out, ui.Fail, "The schedule has no start time "+value+".")
			ui.Next(out, "twokey schedule status lists them.")
			return 1
		}
	case "program":
		if len(args) != 1 {
			fmt.Fprintln(c.env.Err, "Usage: twokey schedule program <twokey.exe>")
			return 2
		}
		exe, err := filepath.Abs(args[0])
		if err != nil {
			ui.Line(out, ui.Fail, ui.Sentence(err.Error()))
			return 1
		}
		var ver string
		if !c.release(exe, &ver) {
			return 1
		}
		s.Program = exe
		note = fmt.Sprintf("The starts run %s (%s).", exe, ver)
	case "until":
		if len(args) != 1 {
			fmt.Fprintln(c.env.Err, "Usage: twokey schedule until <YYYY-MM-DD> | none")
			return 2
		}
		if args[0] == "none" {
			s.Until = ""
		} else {
			d, err := schedule.ParseDay(args[0])
			if err != nil {
				ui.Line(out, ui.Fail, ui.Sentence(err.Error()))
				return 2
			}
			if d < c.now.Format("2006-01-02") {
				ui.Line(out, ui.Fail, d+" has already passed.")
				return 1
			}
			s.Until = d
		}
	}
	if !c.sync(ctx, &s) || !c.save(s) {
		return 1
	}
	state := "off"
	if s.On {
		state = "on"
	}
	ui.Line(out, ui.OK, fmt.Sprintf("Schedule (%s): %s.", state, s.Describe()))
	if note != "" {
		ui.Detail(out, note)
	}
	if !s.On && len(s.Daily)+len(s.Once) > 0 {
		ui.Next(out, "twokey schedule on")
	}
	return 0
}

func (c scheduleCmd) on(ctx context.Context) int {
	s, ok := c.load()
	if !ok {
		return 1
	}
	out := c.env.Out
	switch {
	case len(s.Daily)+len(s.Once) == 0:
		ui.Line(out, ui.Fail, "The schedule has no start times yet.")
		ui.Next(out, "twokey schedule add 02:00 (every day), or twokey schedule add 2026-11-14 13:30 (once)")
		return 1
	case s.Ended(c.now):
		ui.Line(out, ui.Fail, "The schedule's end date ("+s.Until+") has passed.")
		ui.Next(out, "twokey schedule until <YYYY-MM-DD>, or twokey schedule until none")
		return 1
	}
	if _, ok := s.Next(c.now); !ok {
		ui.Line(out, ui.Fail, "No start time is left before the end date.")
		ui.Next(out, "add one with twokey schedule add, or move the end date with twokey schedule until.")
		return 1
	}
	exe, ver, ok := c.program(s)
	if !ok {
		return 1
	}
	s.Program, s.On, s.Since = exe, true, c.now
	if !c.sync(ctx, &s) || !c.save(s) {
		return 1
	}
	next, _ := s.Next(c.now)
	ui.Line(out, ui.OK, fmt.Sprintf("The schedule is on: %s. Next start: %s.", s.Describe(), next.Format("Mon 2 Jan 15:04")))
	ui.Detail(out, fmt.Sprintf("Each start runs twokey round --autopilot with %s (%s), in a new window, and makes decisions by itself.", exe, ver))
	if !c.cfg.Autopilot.Decide {
		ui.Line(out, ui.Warn, "[autopilot] decide = false: a round pauses at the first decision, until you answer.")
	}
	ui.Detail(out, "A round starts only while you are logged in (a locked screen is fine) and the PC is plugged in.")
	ui.Detail(out, "If the PC is asleep, Task Scheduler tries to wake it. Hibernate and shutdown skip the start.")
	ui.Detail(out, "To stop a round that has started: twokey stop. To stop future rounds: twokey schedule off.")
	ui.Next(out, "twokey schedule test, to try a start in 2 minutes without agents.")
	return 0
}

func (c scheduleCmd) off(ctx context.Context) int {
	s, ok := c.load()
	if !ok {
		return 1
	}
	s.On = false
	if !c.save(s) {
		return 1
	}
	if c.tasks.Query(ctx, c.name).Exists {
		if err := c.tasks.SetEnabled(ctx, c.name, false); err != nil {
			ui.Line(c.env.Out, ui.Fail, "Can't disable the task in Task Scheduler: "+err.Error())
			return 1
		}
	}
	ui.Line(c.env.Out, ui.OK, "The schedule is off. No new round starts.")
	if info, err := lock.Read(c.runRoot); err == nil && lock.Alive(info.PID) {
		ui.Detail(c.env.Out, fmt.Sprintf("Run %s is going now; switching off doesn't stop it.", info.RunID))
		ui.Next(c.env.Out, "twokey stop, to stop it safely before its next agent step")
	}
	return 0
}

func (c scheduleCmd) test(ctx context.Context) int {
	s, ok := c.load()
	if !ok {
		return 1
	}
	t, ok := c.task(s)
	if !ok {
		return 1
	}
	start := c.now.Add(2 * time.Minute).Truncate(time.Second)
	if err := c.tasks.Register(ctx, schedule.CheckTaskName(c.name), schedule.CheckTaskXML(t, start)); err != nil {
		ui.Line(c.env.Out, ui.Fail, "Can't create the test task in Task Scheduler: "+err.Error())
		return 1
	}
	ui.Line(c.env.Out, ui.OK, fmt.Sprintf("Task Scheduler starts a test at %s. It runs the checks a round needs, without agents, and changes nothing.", start.Format("15:04")))
	ui.Detail(c.env.Out, "A window opens and closes. Leave the PC awake until then.")
	ui.Next(c.env.Out, "after "+start.Add(time.Minute).Format("15:04")+": twokey schedule status")
	return 0
}

func (c scheduleCmd) fire(ctx context.Context, test bool) int {
	o, err := engineOptions(c.env, c.root, c.cfg, "", true, 0)
	if err != nil {
		ui.Line(c.env.Out, ui.Fail, ui.Sentence(err.Error()))
		return 1
	}
	return schedule.Fire(ctx, schedule.Slot{Engine: o, Console: c.env.Out, Tasks: c.tasks, Task: c.name, Test: test, Notify: o.Notify,
		Version: version.String()})
}

func (c scheduleCmd) status(ctx context.Context) int {
	s, ok := c.load()
	if !ok {
		return 1
	}
	hist, err := schedule.History(c.runRoot)
	if err != nil {
		ui.Line(c.env.Out, ui.Warn, "Can't read "+schedule.HistoryFile+": "+err.Error())
	}
	v := schedule.View{Schedule: s, Task: c.name, History: hist, Now: c.now}
	goos := c.env.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		v.Info = c.tasks.Query(ctx, c.name)
	}
	if v.Program = s.Program; v.Program == "" {
		v.Program = v.Info.Exe
	}
	if v.Program != "" {
		if v.Version, err = c.version(v.Program); err != nil {
			v.Unusable = err.Error()
		}
	}
	if o, err := engineOptions(c.env, c.root, c.cfg, "", false, 0); err == nil {
		if sum, err := engine.Latest(o); err == nil {
			v.Latest = &sum
		}
	}
	if info, err := lock.Read(c.runRoot); err == nil && lock.Alive(info.PID) {
		v.Running = fmt.Sprintf("run %s (process %d), since %s", info.RunID, info.PID, info.Started.Local().Format("15:04"))
	}
	fmt.Fprint(c.env.Out, schedule.Render(v))
	s.LastLooked = c.now
	_ = schedule.Save(c.runRoot, s) // "since you last looked" moves on; the view above is already shown
	return 0
}
