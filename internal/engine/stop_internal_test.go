package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
)

// "twokey stop --now" during a step cancels the run's context, as Ctrl+C
// does (D63).
func TestStopNowCancelsTheRun(t *testing.T) {
	cfg := config.Default()
	e := newEngine(Options{Root: t.TempDir(), Cfg: cfg, Out: io.Discard, StopPoll: 5 * time.Millisecond})
	if err := os.MkdirAll(e.runRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := e.watchStop(cancel)
	defer done()
	if err := os.WriteFile(filepath.Join(e.runRoot, stopFile), []byte("now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the run's context wasn't cancelled")
	}
	if got := e.interrupted(); got != "Stopped by twokey stop --now." {
		t.Errorf("interrupted() = %q", got)
	}
}
