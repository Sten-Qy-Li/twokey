// Package initcmd implements "twokey init": write the config template and
// check that git ignores twokey's files.
package initcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sten-Qy-Li/twokey"
	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// Options for Run.
type Options struct {
	Dir        string // the folder twokey was started in
	ConfigPath string // "" = <repo root>/local-only_twokey.toml
	Runner     proc.Runner
	Out        io.Writer
}

// Run writes the config template and checks .gitignore. It returns the
// exit code.
func Run(ctx context.Context, o Options) int {
	repo := gitx.Repo{Dir: o.Dir, R: o.Runner}
	root, err := repo.TopLevel(ctx)
	if err != nil {
		ui.Line(o.Out, ui.Fail, "This folder is not inside a git repository.")
		ui.Next(o.Out, "run twokey init in the project's repository.")
		return 1
	}

	path := ConfigPath(o.Dir, root, o.ConfigPath)
	content, notes := prefill(ctx, repo, twokey.ConfigTemplate)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("%s already exists. twokey won't overwrite it.", path))
		ui.Next(o.Out, "edit it, or delete it and run twokey init again.")
		return 1
	}
	if err != nil {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("Can't create %s: %v", path, err))
		return 1
	}
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		ui.Line(o.Out, ui.Fail, fmt.Sprintf("Can't write %s: %v", path, werr))
		return 1
	}

	ui.Line(o.Out, ui.OK, "Created "+path)
	for _, n := range notes {
		ui.Line(o.Out, n.status, n.msg)
	}

	// The run folder name comes from the template; check what git ignores.
	defaults := config.Default()
	var toCheck []string
	if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
		toCheck = append(toCheck, rel)
	}
	toCheck = append(toCheck, defaults.Project.RunDir)
	for _, p := range toCheck {
		CheckIgnored(ctx, repo, p, o.Out)
	}

	ui.Detail(o.Out, "Next: fill in the settings in "+filepath.Base(path)+", then run twokey doctor.")
	return 0
}

// ConfigPath resolves the config file path: flag (relative to dir) or the
// default name in the repo root.
func ConfigPath(dir, root, flag string) string {
	switch {
	case flag == "":
		return filepath.Join(root, config.FileName)
	case filepath.IsAbs(flag):
		return flag
	default:
		return filepath.Join(dir, flag)
	}
}

// CheckIgnored prints whether git ignores rel (a path relative to the repo
// root), with the line to add if it doesn't. It returns true if ignored.
func CheckIgnored(ctx context.Context, repo gitx.Repo, rel string, out io.Writer) bool {
	ignored, err := repo.IsIgnored(ctx, rel)
	if err != nil {
		ui.Line(out, ui.Warn, fmt.Sprintf("Can't check whether git ignores %s: %v", rel, err))
		return false
	}
	if ignored {
		ui.Line(out, ui.OK, "git ignores "+filepath.ToSlash(rel))
		return true
	}
	ui.Line(out, ui.Warn, fmt.Sprintf("git does not ignore %s. It must never be committed.", filepath.ToSlash(rel)))
	ui.Next(out, "add this line to .gitignore: /"+filepath.ToSlash(rel))
	return false
}

type note struct {
	status ui.Status
	msg    string
}

// prefill fills in the branch settings from git, where it can.
func prefill(ctx context.Context, repo gitx.Repo, tmpl string) (string, []note) {
	var notes []note
	protected := "main"
	if def, err := repo.RemoteDefaultBranch(ctx, "origin"); err == nil && def != "" {
		protected = def
		tmpl = strings.Replace(tmpl, `protected_branches = ["main"]`, fmt.Sprintf("protected_branches = [%s]", tomlString(def)), 1)
		notes = append(notes, note{ui.OK, fmt.Sprintf("protected_branches = [%q] (the default branch of origin)", def)})
	}

	cur, err := repo.CurrentBranch(ctx)
	switch {
	case err != nil:
		notes = append(notes, note{ui.Warn, "HEAD is not on a branch. Set [project] branch yourself."})
	case cur == protected:
		notes = append(notes, note{ui.Warn, fmt.Sprintf("You are on %s, a protected branch. Set [project] branch to your working branch.", cur)})
	default:
		tmpl = strings.Replace(tmpl, `branch = "my-branch"`, "branch = "+tomlString(cur), 1)
		notes = append(notes, note{ui.OK, fmt.Sprintf("branch = %q (the current branch)", cur)})
	}
	return tmpl, notes
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
