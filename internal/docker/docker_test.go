package docker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// fake plays a docker with some resources and records what was run.
type fake struct {
	missing bool
	down    bool
	lists   map[string]string // "ls" command -> names
	fail    map[string]bool   // command -> fails
	ran     []string
}

func (f *fake) LookPath(string) (string, error) {
	if f.missing {
		return "", exec.ErrNotFound
	}
	return "/fake/docker", nil
}

func (f *fake) Run(_ context.Context, c proc.Cmd) proc.Result {
	cmd := strings.Join(c.Args, " ")
	f.ran = append(f.ran, cmd)
	if cmd == "info --format {{.ServerVersion}}" && f.down || f.fail[cmd] {
		return proc.Result{ExitCode: 1, Stderr: "error: " + cmd + "\nmore", Err: errors.New("exit 1")}
	}
	if out, ok := f.lists[cmd]; ok {
		return proc.Result{Stdout: out}
	}
	return proc.Result{}
}

const prefix = "tk-20261114t0930z-r2-audit-cx-"

func newFake() *fake {
	return &fake{lists: map[string]string{
		"compose ls --all --quiet":      prefix + "web\nowners-stack\n",
		"ps --all --format {{.Names}}":  prefix + "web-app-1\nowners-db\nx-" + prefix + "db\n",
		"network ls --format {{.Name}}": "bridge\n" + prefix + "web_default\n",
		"volume ls --format {{.Name}}":  "owners-data\n",
	}}
}

func TestCleanupRemovesOnlyThePrefix(t *testing.T) {
	f := newFake()
	removed, err := Cleaner{R: f}.Cleanup(t.Context(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Compose project " + prefix + "web", "container " + prefix + "web-app-1", "network " + prefix + "web_default"}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	for _, cmd := range f.ran {
		if strings.Contains(cmd, "owners") || strings.Contains(cmd, "x-"+prefix) || strings.Contains(cmd, "bridge") {
			t.Errorf("touched someone else's resource: %s", cmd)
		}
	}
	if !contains(f.ran, "compose -p "+prefix+"web down --volumes --remove-orphans") {
		t.Errorf("the Compose project wasn't taken down: %v", f.ran)
	}
}

func TestCleanupWithoutDocker(t *testing.T) {
	for name, f := range map[string]*fake{"missing": {missing: true}, "not running": {down: true}} {
		if _, err := (Cleaner{R: f}).Cleanup(t.Context(), prefix); !errors.Is(err, ErrNotRunning) {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestCleanupReportsFailures(t *testing.T) {
	f := newFake()
	f.fail = map[string]bool{"rm --force " + prefix + "web-app-1": true, "volume ls --format {{.Name}}": true}
	removed, err := Cleaner{R: f}.Cleanup(t.Context(), prefix)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("can't remove container %sweb-app-1: error: rm", prefix)) ||
		!strings.Contains(err.Error(), "can't list volumes") {
		t.Errorf("err %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("the rest must still be removed: %v", removed)
	}
}

func TestCleanupNeedsAPrefix(t *testing.T) {
	f := newFake()
	if _, err := (Cleaner{R: f}).Cleanup(t.Context(), ""); err == nil || len(f.ran) != 0 {
		t.Error("an empty prefix must remove nothing")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
