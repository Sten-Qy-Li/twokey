// Package docker removes what an agent step left in Docker (SPEC section 9,
// "Scratch, containers and ports"; DECISIONS.md D12). A job object doesn't
// stop containers, because the Docker daemon runs them, not the agent.
package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// ErrNotRunning means docker is missing or its daemon isn't running.
var ErrNotRunning = errors.New("docker is not running")

// Cleaner removes Docker resources by name prefix.
type Cleaner struct{ R proc.Runner }

// Cleanup removes the Compose projects, containers, networks and volumes
// whose names start with prefix, in that order, and returns what it
// removed. Docker's name filters match anywhere in a name, so Cleanup lists
// the names and checks the prefix itself: it never touches another name.
func (c Cleaner) Cleanup(ctx context.Context, prefix string) ([]string, error) {
	if prefix == "" {
		return nil, errors.New("no prefix: refusing to clean up everything")
	}
	exe, err := c.R.LookPath("docker")
	if err != nil {
		return nil, ErrNotRunning
	}
	docker := func(args ...string) proc.Result { return c.R.Run(ctx, proc.Cmd{Name: exe, Args: args}) }
	if r := docker("info", "--format", "{{.ServerVersion}}"); r.Err != nil {
		return nil, ErrNotRunning
	}
	var removed, problems []string
	kinds := []struct {
		what   string
		list   []string
		remove func(name string) []string
	}{
		{"Compose project", []string{"compose", "ls", "--all", "--quiet"},
			func(n string) []string { return []string{"compose", "-p", n, "down", "--volumes", "--remove-orphans"} }},
		{"container", []string{"ps", "--all", "--format", "{{.Names}}"},
			func(n string) []string { return []string{"rm", "--force", n} }},
		{"network", []string{"network", "ls", "--format", "{{.Name}}"},
			func(n string) []string { return []string{"network", "rm", n} }},
		{"volume", []string{"volume", "ls", "--format", "{{.Name}}"},
			func(n string) []string { return []string{"volume", "rm", "--force", n} }},
	}
	for _, k := range kinds {
		r := docker(k.list...)
		if r.Err != nil {
			problems = append(problems, fmt.Sprintf("can't list %ss: %s", k.what, firstLine(r.Stderr, r.Err)))
			continue
		}
		for _, name := range Matching(r.Stdout, prefix) {
			if r := docker(k.remove(name)...); r.Err != nil {
				problems = append(problems, fmt.Sprintf("can't remove %s %s: %s", k.what, name, firstLine(r.Stderr, r.Err)))
				continue
			}
			removed = append(removed, k.what+" "+name)
		}
	}
	if len(problems) > 0 {
		return removed, errors.New(strings.Join(problems, "; "))
	}
	return removed, nil
}

// Matching returns the names, one per line, that start with prefix.
func Matching(list, prefix string) []string {
	var names []string
	for _, line := range strings.Split(list, "\n") {
		if name := strings.TrimSpace(line); name != "" && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}

func firstLine(stderr string, err error) string {
	s := strings.TrimSpace(stderr)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" && err != nil {
		s = err.Error()
	}
	return strings.TrimSpace(s)
}
