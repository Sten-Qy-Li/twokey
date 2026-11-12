//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// tree is a process group.
type tree struct{ pgid int }

func startTree(cmd *exec.Cmd) (*tree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &tree{pgid: cmd.Process.Pid}, nil
}

func (t *tree) kill() { _ = syscall.Kill(-t.pgid, syscall.SIGKILL) }

// close kills what is left in the group.
func (t *tree) close() { t.kill() }
