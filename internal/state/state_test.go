package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	s := &State{RunID: "r", Started: time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC), Status: Running,
		ProtectedSHAs: map[string]string{"main": "abc"}, Decisions: map[string]string{"R1-M-01": "B"}}
	r := s.NewRound("c1", "c1")
	r.Ensure(CodexAudit, "codex").Status = Done
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != Version || !reflect.DeepEqual(got.Rounds, s.Rounds) || got.Decisions["R1-M-01"] != "B" || got.Rulings == nil {
		t.Errorf("round trip: %+v", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".state-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("{")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "is damaged") {
		t.Errorf("damaged: %v", err)
	}
	write(`{"version": 99}`)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "format version 99") {
		t.Errorf("version: %v", err)
	}
}

func TestRoundSteps(t *testing.T) {
	s := &State{}
	if s.Current() != nil {
		t.Error("no round yet")
	}
	r := s.NewRound("c1", "c1")
	if r.N != 1 || s.Current() != r || r.IsDone(Fix) {
		t.Errorf("round: %+v", r)
	}
	a := r.Ensure(Fix, "claude")
	if r.Ensure(Fix, "claude") != a || len(r.Steps) != 1 || a.Status != Pending {
		t.Error("Ensure must return the same step")
	}
	a.Status = Done
	if !r.IsDone(Fix) {
		t.Error("IsDone")
	}
	if s.NewRound("c2", "c2").N != 2 {
		t.Error("round numbers count up")
	}
}

// Resume behaviour for every step status (SPEC section 15).
func TestInterrupted(t *testing.T) {
	s := &State{}
	r := s.NewRound("c1", "c1")
	statuses := map[string]string{CodexAudit: Done, ClaudeAudit: Active, Convergence: Active, Dispute: Failed, Fix: Pending}
	for name, st := range statuses {
		r.Ensure(name, "x").Status = st
	}
	rerun, fix := s.Interrupted()
	if fix != nil {
		t.Errorf("no fix was running: %+v", fix)
	}
	if len(rerun) != 2 || r.Step(ClaudeAudit).Status != Pending || r.Step(Convergence).Status != Pending {
		t.Errorf("running read-only steps rerun: %v", rerun)
	}
	for name, want := range map[string]string{CodexAudit: Done, Dispute: Failed, Fix: Pending} {
		if got := r.Step(name).Status; got != want {
			t.Errorf("%s: %s, want %s unchanged", name, got, want)
		}
	}

	r.Step(Fix).Status = Active
	rerun, fix = s.Interrupted()
	if len(rerun) != 0 || fix == nil || fix.Status != Active {
		t.Errorf("a running fix must stay running for the caller to pause: %v %+v", rerun, fix)
	}
	if rerun, fix := (&State{}).Interrupted(); rerun != nil || fix != nil {
		t.Error("no rounds: nothing interrupted")
	}
}
