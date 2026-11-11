package lock

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir, Info{PID: os.Getpid(), RunID: "run-1", Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	info, err := Read(dir)
	if err != nil || info.RunID != "run-1" || info.PID != os.Getpid() {
		t.Fatalf("Read = %+v, %v", info, err)
	}

	_, err = Acquire(dir, Info{PID: os.Getpid(), RunID: "run-2"})
	var held *HeldError
	if !errors.As(err, &held) || !held.Alive || held.Info.RunID != "run-1" {
		t.Fatalf("second Acquire: %v", err)
	}
	if _, err := Remove(dir); !errors.As(err, &held) {
		t.Errorf("Remove must refuse a live lock: %v", err)
	}

	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock still there: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("a second Release must be harmless: %v", err)
	}
}

func TestStaleLock(t *testing.T) {
	dir := t.TempDir()
	pid := deadPID(t)
	if _, err := Acquire(dir, Info{PID: pid, RunID: "old"}); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire(dir, Info{PID: os.Getpid(), RunID: "new"})
	var held *HeldError
	if !errors.As(err, &held) || held.Alive {
		t.Fatalf("want a stale HeldError, got %v", err)
	}
	info, err := Remove(dir)
	if err != nil || info.RunID != "old" {
		t.Fatalf("Remove = %+v, %v", info, err)
	}
	if _, err := Acquire(dir, Info{PID: os.Getpid(), RunID: "new"}); err != nil {
		t.Errorf("Acquire after Remove: %v", err)
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process is alive")
	}
	if Alive(deadPID(t)) || Alive(0) || Alive(-1) {
		t.Error("dead or invalid PIDs are not alive")
	}
}

// deadPID starts and waits for a short process, and returns its PID.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "--version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.Pid()
}
