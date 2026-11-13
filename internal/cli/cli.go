// Package cli reads twokey's command line and runs the chosen command.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/doctor"
	"github.com/Sten-Qy-Li/twokey/internal/initcmd"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/version"
)

const usage = `twokey runs a cross-checked audit-and-fix loop between Codex and Claude Code.

Usage:
  twokey <command> [options]

Commands:
  init      Create the config file and check .gitignore
  doctor    Check that everything twokey needs is in place
  run       Start a run
  round     Run one round (audits to push), then stop
  resume    Continue a paused or stopped run
  decide    Answer a finding, or change an automatic decision
  stop      Stop a run that is going
  schedule  Start rounds at set times (Windows Task Scheduler)
  render    Show a prompt without running it
  unlock    Remove a stale lock
  status    Show the current run
  report    Rebuild a run's report
  version   Print the version

Run "twokey <command> -h" to see a command's options.
`

// Env is what a command needs from the outside world.
type Env struct {
	Dir    string // the folder twokey was started in
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Runner proc.Runner
	// StagingRoot and ScratchRoot replace twokey's own folders (tests).
	StagingRoot, ScratchRoot string
	GOOS                     string           // "" = runtime.GOOS (tests)
	Now                      func() time.Time // nil = time.Now (tests)
	Exe                      string           // "" = this twokey.exe; the program a schedule's task runs (tests)
}

// Main runs twokey and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "twokey: can't read the current folder:", err)
		return 1
	}
	return Run(ctx, args, Env{Dir: dir, In: os.Stdin, Out: stdout, Err: stderr, Runner: proc.OS{}})
}

// Run runs one command line.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Err, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(env.Out, usage)
		return 0
	case "version", "--version":
		fmt.Fprintln(env.Out, "twokey", version.String())
		return 0
	case "init":
		return runInit(ctx, rest, env)
	case "doctor":
		return runDoctor(ctx, rest, env)
	case "run":
		return runRun(ctx, rest, env)
	case "round":
		return runRound(ctx, rest, env)
	case "resume":
		return runResume(ctx, rest, env)
	case "render":
		return runRender(ctx, rest, env)
	case "unlock":
		return runUnlock(ctx, rest, env)
	case "status":
		return runStatus(ctx, rest, env)
	case "report":
		return runReport(ctx, rest, env)
	case "decide":
		return runDecide(ctx, rest, env)
	case "stop":
		return runStop(ctx, rest, env)
	case "schedule":
		return runSchedule(ctx, rest, env)
	default:
		fmt.Fprintf(env.Err, "twokey: unknown command %q. Run \"twokey help\" to see the commands.\n", cmd)
		return 2
	}
}

// newFlags makes a flag set that reports errors in twokey's style.
func newFlags(name, summary string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Err)
	fs.Usage = func() {
		fmt.Fprintf(env.Err, "Usage: twokey %s [options]\n\n%s\n\nOptions:\n", name, summary)
		fs.PrintDefaults()
	}
	return fs
}

// parse returns -1 if the command should go on, else the exit code.
func parse(fs *flag.FlagSet, args []string) int {
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	case fs.NArg() > 0:
		fmt.Fprintf(fs.Output(), "twokey %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return 2
	}
	return -1
}

func runInit(ctx context.Context, args []string, env Env) int {
	fs := newFlags("init", "Creates the config file in the repository root and checks that git ignores it.", env)
	configPath := fs.String("config", "", "write the config to this file instead of local-only_twokey.toml")
	templates := fs.Bool("templates", false, "copy the default prompt templates to <run_dir>/templates for editing")
	if code := parse(fs, args); code >= 0 {
		return code
	}
	if *templates {
		return copyTemplates(ctx, env, *configPath)
	}
	return initcmd.Run(ctx, initcmd.Options{Dir: env.Dir, ConfigPath: *configPath, Runner: env.Runner, Out: env.Out})
}

func runDoctor(ctx context.Context, args []string, env Env) int {
	fs := newFlags("doctor", "Checks git, both agent CLIs, the config, the repository and the notification setup.", env)
	configPath := fs.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	smoke := fs.Bool("smoke", false, "also make small real calls to both agents (uses a little of your plan)")
	if code := parse(fs, args); code >= 0 {
		return code
	}
	return doctor.Run(ctx, doctor.Options{Dir: env.Dir, ConfigPath: *configPath, Runner: env.Runner, Out: env.Out,
		StagingRoot: env.StagingRoot, ScratchRoot: env.ScratchRoot, Smoke: *smoke})
}
