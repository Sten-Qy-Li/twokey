package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/initcmd"
	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/paths"
	"github.com/Sten-Qy-Li/twokey/internal/render"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// project finds the repository root and loads its config. It prints the
// problem and returns false if it can't.
func project(ctx context.Context, env Env, configFlag string) (string, *config.Config, bool) {
	root, err := gitx.Repo{Dir: env.Dir, R: env.Runner}.TopLevel(ctx)
	if err != nil {
		ui.Line(env.Out, ui.Fail, "This folder is not inside a git repository.")
		return "", nil, false
	}
	path := initcmd.ConfigPath(env.Dir, root, configFlag)
	cfg, err := config.Load(path)
	var probs config.Problems
	switch {
	case errors.As(err, &probs):
		ui.Line(env.Out, ui.Fail, "The config file has problems: "+path)
		for _, p := range probs {
			ui.Detail(env.Out, "- "+p)
		}
		return "", nil, false
	case err != nil:
		ui.Line(env.Out, ui.Fail, err.Error())
		ui.Next(env.Out, "twokey init")
		return "", nil, false
	}
	return root, cfg, true
}

func engineOptions(env Env, root string, cfg *config.Config, fake string, autopilot bool, maxRounds int) (engine.Options, error) {
	staging := env.StagingRoot
	if staging == "" {
		s, err := paths.StagingRoot()
		if err != nil {
			return engine.Options{}, err
		}
		staging = s
	}
	scratch := env.ScratchRoot
	if scratch == "" {
		scratch = paths.ScratchRoot()
	}
	fakeDir := fake
	if fakeDir != "" && !filepath.IsAbs(fakeDir) {
		fakeDir = filepath.Join(env.Dir, fakeDir)
	}
	return engine.Options{
		Root: root, Cfg: cfg, Proc: env.Runner, Out: env.Out,
		Runner:      runner.Fake{Dir: fakeDir, Git: env.Runner},
		UI:          engine.NewTextUI(env.In, env.Out, autopilot),
		StagingRoot: staging, ScratchRoot: scratch,
		FakeAgents: true, MaxRounds: maxRounds, Autopilot: autopilot, Editor: editor(),
	}, nil
}

// editor opens prompts in stepper mode: $EDITOR, else notepad or vi.
func editor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

const noRealAgents = "Real agent runs arrive in the next milestone (M2). For now, run with --fake-agents <scenario folder>."

func runRun(ctx context.Context, args []string, env Env) int {
	fs := newFlags("run", "Starts a run: blind audits, convergence, fixes and pushes, until both agents clear the same commit.", env)
	configPath := fs.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	autopilot := fs.Bool("autopilot", false, "don't wait for Enter between steps")
	maxRounds := fs.Int("max-rounds", 0, "stop after this many audit rounds (default: [loop] max_rounds)")
	fake := fs.String("fake-agents", "", "play scripted answers from this scenario folder (for tests)")
	if code := parse(fs, args); code >= 0 {
		return code
	}
	if *fake == "" {
		fmt.Fprintln(env.Err, noRealAgents)
		return 2
	}
	root, cfg, ok := project(ctx, env, *configPath)
	if !ok {
		return 1
	}
	o, err := engineOptions(env, root, cfg, *fake, *autopilot || cfg.Loop.Mode == "autopilot", *maxRounds)
	if err != nil {
		ui.Line(env.Out, ui.Fail, err.Error())
		return 1
	}
	return engine.Start(ctx, o)
}

func runResume(ctx context.Context, args []string, env Env) int {
	fs := newFlags("resume", "Continues the latest run, or the one given with --run.", env)
	configPath := fs.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	runID := fs.String("run", "", "the run to continue (default: the latest)")
	more := fs.Int("more-rounds", 0, "allow this many more audit rounds")
	autopilot := fs.Bool("autopilot", false, "don't wait for Enter between steps")
	fake := fs.String("fake-agents", "", "play scripted answers from this scenario folder (for tests)")
	if code := parse(fs, args); code >= 0 {
		return code
	}
	if *fake == "" {
		fmt.Fprintln(env.Err, noRealAgents)
		return 2
	}
	root, cfg, ok := project(ctx, env, *configPath)
	if !ok {
		return 1
	}
	o, err := engineOptions(env, root, cfg, *fake, *autopilot, 0)
	if err != nil {
		ui.Line(env.Out, ui.Fail, err.Error())
		return 1
	}
	return engine.Resume(ctx, o, *runID, *more)
}

func runRender(ctx context.Context, args []string, env Env) int {
	fs := newFlags("render", "Prints the prompt for a step (audit, convergence, dispute or fix) without running it.", env)
	configPath := fs.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	round := fs.Int("round", 0, "the round to render for (default: the latest)")
	agent := fs.String("agent", "", "for audit: codex (default) or claude")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(env.Err, "Usage: twokey render <audit|convergence|dispute|fix> [--round N] [--agent codex|claude]")
		return 2
	}
	root, cfg, ok := project(ctx, env, *configPath)
	if !ok {
		return 1
	}
	o, err := engineOptions(env, root, cfg, "", false, 0)
	if err != nil {
		ui.Line(env.Out, ui.Fail, err.Error())
		return 1
	}
	out, err := engine.Render(ctx, o, fs.Arg(0), *agent, *round)
	if err != nil {
		ui.Line(env.Out, ui.Fail, err.Error())
		return 1
	}
	fmt.Fprint(env.Out, out)
	return 0
}

func runUnlock(ctx context.Context, args []string, env Env) int {
	flags := newFlags("unlock", "Removes a stale lock after checking that the twokey process in it isn't running.", env)
	configPath := flags.String("config", "", "read the config from this file instead of local-only_twokey.toml")
	if code := parse(flags, args); code >= 0 {
		return code
	}
	root, cfg, ok := project(ctx, env, *configPath)
	if !ok {
		return 1
	}
	info, err := lock.Remove(filepath.Join(root, cfg.Project.RunDir))
	var held *lock.HeldError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ui.Line(env.Out, ui.OK, "There is no lock.")
		return 0
	case errors.As(err, &held):
		ui.Line(env.Out, ui.Fail, held.Error()+". twokey won't remove a live lock.")
		ui.Next(env.Out, "wait for that run, or stop it first.")
		return 1
	case err != nil:
		ui.Line(env.Out, ui.Fail, "Can't remove the lock: "+err.Error())
		return 1
	}
	ui.Line(env.Out, ui.OK, fmt.Sprintf("Removed the stale lock of run %s (PID %d is not running).", info.RunID, info.PID))
	return 0
}

func copyTemplates(ctx context.Context, env Env, configFlag string) int {
	root, cfg, ok := project(ctx, env, configFlag)
	if !ok {
		return 1
	}
	dir := filepath.Join(root, cfg.Project.RunDir, "templates")
	wrote, err := render.CopyDefaults(dir)
	if err != nil {
		ui.Line(env.Out, ui.Fail, "Can't copy the templates: "+err.Error())
		return 1
	}
	for _, p := range wrote {
		ui.Line(env.Out, ui.OK, "Wrote "+p)
	}
	if len(wrote) == 0 {
		ui.Line(env.Out, ui.OK, "All templates are already in "+dir+". twokey keeps your edits.")
	}
	ui.Detail(env.Out, "Edit them there. twokey uses a file in that folder instead of its default.")
	return 0
}
