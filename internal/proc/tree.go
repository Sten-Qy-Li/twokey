package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TreeCmd is a long-running command, such as an agent CLI: its prompt
// comes on stdin, and its output goes straight to files, so two processes
// running in parallel can't block each other (SPEC section 9).
type TreeCmd struct {
	Dir    string
	Name   string
	Args   []string
	Env    []string // extra KEY=value entries on top of the current environment
	Stdin  string
	Stdout string // file path
	Stderr string // file path
}

// TreeResult is what a TreeCmd did.
type TreeResult struct {
	ExitCode int   // -1 if the command didn't start or was killed
	Err      error // nil only when the command ran and exited with 0
	Killed   bool  // the context ended first, and the whole tree was killed
}

// RunTree runs the command with every process it starts in one group: a
// Windows job object, or a process group elsewhere. When ctx ends (a
// timeout or Ctrl+C), the whole tree is killed. When the command exits,
// processes it left behind are killed too. On Windows the job also dies
// with twokey itself.
func RunTree(ctx context.Context, c TreeCmd) TreeResult {
	res := TreeResult{ExitCode: -1}
	stdout, err := os.OpenFile(c.Stdout, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		res.Err = err
		return res
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(c.Stderr, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		res.Err = err
		return res
	}
	defer stderr.Close()

	//nolint:noctx // RunTree kills the whole tree when ctx ends, not only the first process.
	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 10 * time.Second

	t, err := startTree(cmd)
	if err != nil {
		res.Err = err
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		res.Killed = true
		t.kill()
		err = <-done
	}
	t.close()
	if cmd.ProcessState != nil && !res.Killed {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case res.Killed:
		res.Err = fmt.Errorf("%s was stopped: %w", c.Name, ctx.Err())
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Err = fmt.Errorf("%s exited with code %d", c.Name, res.ExitCode)
		} else {
			res.Err = err
		}
	}
	return res
}
