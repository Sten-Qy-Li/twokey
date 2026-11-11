package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// shellCommand runs a command line through cmd.exe, so npm.cmd and other
// .cmd shims work. The line is passed to cmd /C exactly as written in the
// config; Go's own argument quoting would break quotes inside it.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + comspec + `" /D /S /C "` + line + `"`}
	return cmd
}
