//go:build !windows

package proc

import (
	"context"
	"os/exec"
)

// shellCommand runs a command line through sh.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", line)
}
