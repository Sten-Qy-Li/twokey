// Package proc runs external commands (git, the agent CLIs, docker).
// Code takes a Runner, so tests can swap in a fake.
package proc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Cmd describes one external command.
type Cmd struct {
	Dir  string
	Name string
	Args []string
	Env  []string // extra KEY=value entries on top of the current environment
	// Shell, if set, is a command line for the system shell (cmd /C on
	// Windows, sh -c elsewhere); Name and Args are ignored.
	Shell string
}

// Result is what a command did.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int   // -1 if the command didn't start or was stopped
	Err      error // nil only when the command ran and exited with 0
}

// Runner runs external commands.
type Runner interface {
	LookPath(file string) (string, error)
	Run(ctx context.Context, c Cmd) Result
}

// OS runs real commands.
type OS struct{}

// LookPath finds an executable on PATH (PATHEXT applies on Windows).
func (OS) LookPath(file string) (string, error) { return exec.LookPath(file) }

// Run runs the command and waits for it. Stdin is empty.
func (OS) Run(ctx context.Context, c Cmd) Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if c.Shell != "" {
		cmd = shellCommand(ctx, c.Shell)
		c.Name = "the command"
	}
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Don't hang if a grandchild keeps the output pipes open.
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	r := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, Err: err}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil && ctx.Err() != nil {
		r.Err = fmt.Errorf("%s was stopped: %w", c.Name, ctx.Err())
	}
	return r
}
