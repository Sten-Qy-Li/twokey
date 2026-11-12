package proc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/lock"
)

// The tests run this test binary as the child process. TWOKEY_TREE_HELPER
// says what the child does:
//
//	echo     copy stdin to stdout, write "oops" to stderr, exit 3
//	parent   start a grandchild ("sleep"), write its PID to TWOKEY_TREE_PID, sleep
//	leave    like parent, but exit at once, leaving the grandchild behind
//	sleep    sleep for a minute
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("TWOKEY_TREE_HELPER")
	if mode == "" {
		t.Skip("only runs as a child process")
	}
	switch mode {
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		fmt.Fprint(os.Stderr, "oops")
		os.Exit(3)
	case "parent", "leave":
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), "TWOKEY_TREE_HELPER=sleep")
		if err := cmd.Start(); err != nil {
			os.Exit(10)
		}
		if err := os.WriteFile(os.Getenv("TWOKEY_TREE_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			os.Exit(11)
		}
		if mode == "leave" {
			os.Exit(0)
		}
		time.Sleep(time.Minute)
	case "sleep":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func helper(t *testing.T, mode, stdin string) (TreeCmd, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	return TreeCmd{
		Dir: dir, Name: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"},
		Env:   []string{"TWOKEY_TREE_HELPER=" + mode, "TWOKEY_TREE_PID=" + pidFile},
		Stdin: stdin, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	}, pidFile
}

func TestRunTreeFilesAndExitCode(t *testing.T) {
	c, _ := helper(t, "echo", "the prompt\n")
	r := RunTree(t.Context(), c)
	if r.ExitCode != 3 || r.Err == nil || r.Killed {
		t.Fatalf("result %+v", r)
	}
	out, _ := os.ReadFile(c.Stdout)
	errOut, _ := os.ReadFile(c.Stderr)
	if string(out) != "the prompt\n" || string(errOut) != "oops" {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
}

// waitDead waits until a process is gone.
func waitDead(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !lock.Alive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("process %d is still running", pid)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the child never wrote the grandchild's PID")
	return 0
}

// A timeout kills the child and the grandchild it started.
func TestRunTreeKillsTheWholeTree(t *testing.T) {
	c, pidFile := helper(t, "parent", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	r := RunTree(ctx, c)
	if !r.Killed || r.Err == nil || r.ExitCode != -1 {
		t.Fatalf("result %+v", r)
	}
	if time.Since(start) > 30*time.Second {
		t.Error("RunTree waited too long after the kill")
	}
	waitDead(t, readPID(t, pidFile))
}

// A child that exits normally can't leave a process behind.
func TestRunTreeKillsLeftovers(t *testing.T) {
	c, pidFile := helper(t, "leave", "")
	r := RunTree(t.Context(), c)
	if r.Err != nil || r.ExitCode != 0 {
		t.Fatalf("result %+v", r)
	}
	waitDead(t, readPID(t, pidFile))
}

func TestRunTreeMissingProgram(t *testing.T) {
	dir := t.TempDir()
	r := RunTree(t.Context(), TreeCmd{Dir: dir, Name: filepath.Join(dir, "no-such-program"),
		Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err")})
	if r.Err == nil || r.ExitCode != -1 {
		t.Errorf("result %+v", r)
	}
}
