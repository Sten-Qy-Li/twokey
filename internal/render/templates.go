package render

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sten-Qy-Li/twokey"
)

// TemplateNames are the prompt templates, without ".md".
var TemplateNames = []string{"preamble", "audit", "convergence", "dispute", "fix", "rule-no-changes", "rule-fix"}

// Templates loads prompt templates: a file in OverrideDir wins over the
// default that ships in the binary (SPEC section 13).
type Templates struct {
	OverrideDir string // <run_dir>/templates; "" = defaults only
}

// Get returns a template's text and where it came from.
func (t Templates) Get(name string) (text, source string, err error) {
	if t.OverrideDir != "" {
		p := filepath.Join(t.OverrideDir, name+".md")
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), p, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
	}
	data, err := fs.ReadFile(twokey.Prompts, name+".md")
	if err != nil {
		return "", "", fmt.Errorf("there is no prompt template %q", name)
	}
	return string(data), "default " + name + ".md", nil
}

// Prompt renders the preamble plus the step's template. The step write
// rule is rendered first, because it contains ${branch}.
func (t Templates) Prompt(step string, vars map[string]string) (string, error) {
	rule := "rule-no-changes"
	if step == "fix" {
		rule = "rule-fix"
	}
	v := maps.Clone(vars)
	ruleText, err := t.render(rule, v)
	if err != nil {
		return "", err
	}
	v["step_write_rule"] = strings.TrimRight(ruleText, "\r\n")

	pre, err := t.render("preamble", v)
	if err != nil {
		return "", err
	}
	body, err := t.render(step, v)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(pre, "\r\n") + "\n\n" + body, nil
}

func (t Templates) render(name string, vars map[string]string) (string, error) {
	text, source, err := t.Get(name)
	if err != nil {
		return "", err
	}
	out, err := Expand(text, vars)
	if err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return out, nil
}

// CopyDefaults writes the default templates to dir for editing ("twokey
// init --templates"). Existing files are kept. It returns the files it
// wrote.
func CopyDefaults(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var wrote []string
	for _, name := range TemplateNames {
		p := filepath.Join(dir, name+".md")
		if _, err := os.Stat(p); err == nil {
			continue
		}
		data, err := fs.ReadFile(twokey.Prompts, name+".md")
		if err != nil {
			return wrote, err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return wrote, err
		}
		wrote = append(wrote, p)
	}
	return wrote, nil
}
