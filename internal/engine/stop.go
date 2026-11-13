package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/lock"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// stopFile, in the run root, asks a running twokey to stop ("twokey stop",
// DECISIONS.md D63): "step" stops before the next agent step, "now" stops
// the current step too.
const stopFile = "stop"

// RequestStop asks the twokey that holds the lock to stop. It returns
// before the run has stopped.
func RequestStop(o Options, now bool) int {
	e := newEngine(o)
	info, err := lock.Read(e.runRoot)
	if err != nil || !lock.Alive(info.PID) {
		ui.Line(o.Out, ui.OK, "No twokey run is going in this repository.")
		return ExitDone
	}
	what, text := "step", fmt.Sprintf("Asked run %s to stop before its next agent step. "+
		"A step that is running finishes first; a fix still gets its checks and push.", info.RunID)
	if now {
		what, text = "now", fmt.Sprintf("Asked run %s to stop now. The running step is stopped, and twokey removes what it left in Docker.", info.RunID)
	}
	if err := os.WriteFile(filepath.Join(e.runRoot, stopFile), []byte(what+"\n"), 0o644); err != nil {
		ui.Line(o.Out, ui.Fail, "Can't write the stop request: "+err.Error())
		return ExitError
	}
	ui.Line(o.Out, ui.OK, text)
	ui.Detail(o.Out, "The run sees the request within a few seconds and saves its state.")
	ui.Next(o.Out, "twokey status shows when it has stopped. Then twokey resume or twokey round continues it.")
	return ExitDone
}

// clearStop removes a stop request left from before this run started.
func (e *engine) clearStop() { _ = os.Remove(filepath.Join(e.runRoot, stopFile)) }

// watchStop looks for a stop request until the returned function is called.
// "now" cancels the run's context, as Ctrl+C does; "step" makes the next
// agent step's start stop the run.
func (e *engine) watchStop(cancel context.CancelFunc) func() {
	every := e.o.StopPoll
	if every == 0 {
		every = 2 * time.Second
	}
	path := filepath.Join(e.runRoot, stopFile)
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				_ = os.Remove(path)
				if strings.TrimSpace(string(data)) == "now" {
					e.stopNow.Store(true)
					ui.Line(e.o.Out, ui.Busy, "twokey stop --now: stopping the running step.")
					cancel()
					return
				}
				e.stopAfter.Store(true)
				ui.Line(e.o.Out, ui.Busy, "twokey stop: the run stops before its next agent step.")
			}
		}
	}()
	return func() { close(quit); <-done }
}

// stopRequested reports whether "twokey stop" asked the run to stop. It
// reads a request the watcher hasn't seen yet too, so the start of a step
// never misses one.
func (e *engine) stopRequested() bool {
	path := filepath.Join(e.runRoot, stopFile)
	if data, err := os.ReadFile(path); err == nil {
		_ = os.Remove(path)
		if strings.TrimSpace(string(data)) == "now" {
			e.stopNow.Store(true)
		}
		e.stopAfter.Store(true)
	}
	return e.stopAfter.Load()
}

// interrupted says why the run's context ended.
func (e *engine) interrupted() string {
	if e.stopNow.Load() {
		return "Stopped by twokey stop --now."
	}
	return "Stopped (Ctrl+C)."
}
