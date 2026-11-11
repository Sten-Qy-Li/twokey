package initcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/testutil"
)

func runInit(t *testing.T, dir string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Run(t.Context(), Options{Dir: dir, Runner: proc.OS{}, Out: &out})
	return out.String(), code
}

func TestInitWritesPrefilledConfig(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	out, code := runInit(t, r.Dir)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"[OK]    Created ",
		`[OK]    protected_branches = ["main"] (the default branch of origin)`,
		`[OK]    branch = "feature" (the current branch)`,
		"[WARN]  git does not ignore local-only_twokey.toml. It must never be committed.",
		"Next: add this line to .gitignore: /local-only_twokey.toml",
		"[WARN]  git does not ignore local-only_twokey.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatalf("written config doesn't load: %v", err)
	}
	if cfg.Project.Branch != "feature" {
		t.Errorf("branch = %q", cfg.Project.Branch)
	}
}

func TestInitFromSubfolder(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, "src/x.txt", "x")
	if _, code := runInit(t, filepath.Join(r.Dir, "src")); code != 0 {
		t.Fatal("init failed")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, config.FileName)); err != nil {
		t.Errorf("config not in the repo root: %v", err)
	}
}

func TestInitIgnoreRules(t *testing.T) {
	for _, rules := range []string{
		"local-only_*\n",
		"/local-only_twokey.toml\n/local-only_twokey/\n", // a folder rule counts before the folder exists
	} {
		r := testutil.NewRepo(t, "feature")
		r.WriteFile(t, ".gitignore", rules)
		out, _ := runInit(t, r.Dir)
		if strings.Contains(out, "[WARN]  git does not ignore") ||
			!strings.Contains(out, "[OK]    git ignores local-only_twokey.toml") ||
			!strings.Contains(out, "[OK]    git ignores local-only_twokey\n") {
			t.Errorf("rules %q:\n%s", rules, out)
		}
	}
}

func TestInitOnProtectedBranch(t *testing.T) {
	r := testutil.NewRepo(t, "main")
	out, code := runInit(t, r.Dir)
	if code != 0 || !strings.Contains(out, "[WARN]  You are on main, a protected branch.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(r.Dir, config.FileName))
	if !strings.Contains(string(b), `branch = "my-branch"`) {
		t.Error("branch placeholder was replaced")
	}
}

func TestInitWontOverwrite(t *testing.T) {
	r := testutil.NewRepo(t, "feature")
	r.WriteFile(t, config.FileName, "mine")
	out, code := runInit(t, r.Dir)
	if code != 1 || !strings.Contains(out, "already exists. twokey won't overwrite it.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, config.FileName)); string(b) != "mine" {
		t.Error("file was changed")
	}
}

func TestInitOutsideRepo(t *testing.T) {
	testutil.IsolateGit(t)
	out, code := runInit(t, t.TempDir())
	if code != 1 || !strings.Contains(out, "[FAIL]  This folder is not inside a git repository.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestTomlString(t *testing.T) {
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("got %s", got)
	}
}
