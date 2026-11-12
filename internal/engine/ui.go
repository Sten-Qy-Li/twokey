package engine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// Choice is the owner's answer before a step.
type Choice int

// The choices before a step (SPEC section 12, "Stepper mode").
const (
	Run Choice = iota
	Edit
	Quit
)

// UI is how the engine talks to the owner. Stepper mode asks at every
// step; autopilot never asks.
type UI interface {
	// Before is called before an agent step, the checks or a push. In
	// stepper mode it is the one stop between steps (D56).
	Before(what string, prompts []string) Choice
	// Edited is called after the editor closed, before the edited prompt
	// is used. false = quit.
	Edited(path string) bool
	// After shows a step's result. It doesn't wait: the next step's Before
	// or a question does.
	After(text string)
	// Decide asks for a decision on a needs_decision finding: an option,
	// the owner's own text, or "drop". quit = stop and resume later.
	Decide(f schema.MergedFinding) (answer string, quit bool)
	// Rule asks "fix" or "drop" for a disputed finding Codex maintained.
	Rule(f schema.MergedFinding, codexEvidence string) (ruling, note string, quit bool)
	// Recurring asks "fix" or "drop" for a finding that keeps coming back.
	Recurring(f schema.MergedFinding, lineage int) (ruling, note string, quit bool)
	// Confirm shows a change to a protected branch and asks whether it was
	// expected. Only an explicit yes counts.
	Confirm(text string) bool
}

// TextUI asks at the terminal. With Autopilot set it doesn't wait between
// steps, but it still asks for decisions and rulings: a pause waits at the
// terminal prompt in both modes (SPEC section 10). Without input (end of
// file), the owner's answer counts as "quit", and the run pauses.
type TextUI struct {
	In        *bufio.Reader
	Out       io.Writer
	Autopilot bool
}

// NewTextUI reads answers from in.
func NewTextUI(in io.Reader, out io.Writer, autopilot bool) *TextUI {
	return &TextUI{In: bufio.NewReader(in), Out: out, Autopilot: autopilot}
}

var errEOF = errors.New("no more input")

func (t *TextUI) ask(prompt string) (string, error) {
	fmt.Fprint(t.Out, prompt)
	line, err := t.In.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(t.Out)
		return "", errEOF
	}
	return strings.TrimSpace(line), nil
}

// Before implements UI.
func (t *TextUI) Before(what string, prompts []string) Choice {
	ui.Line(t.Out, ui.Busy, "Next: "+what)
	for _, p := range prompts {
		ui.Detail(t.Out, "Prompt: "+p)
	}
	if t.Autopilot {
		return Run
	}
	for {
		hint := "Enter = run, q = quit (resume later): "
		if len(prompts) > 0 {
			hint = "Enter = run, e = edit the prompt first, q = quit (resume later): "
		}
		a, err := t.ask(fmt.Sprintf("%-7s %s", "", hint))
		switch {
		case err != nil || a == "q":
			return Quit
		case a == "":
			return Run
		case a == "e" && len(prompts) > 0:
			return Edit
		}
	}
}

// Edited implements UI.
func (t *TextUI) Edited(path string) bool {
	a, err := t.ask(fmt.Sprintf("%-7s Save and close %s, then press Enter (q = quit): ", "", path))
	return err == nil && a != "q"
}

// After implements UI.
func (t *TextUI) After(text string) {
	fmt.Fprintln(t.Out, strings.TrimRight(text, "\n"))
}

func (t *TextUI) showFinding(f schema.MergedFinding) {
	fmt.Fprintf(t.Out, "\n%s [%s %s] %s\n", f.ID, f.Severity, f.Kind, f.Title)
	if f.Evidence != "" {
		fmt.Fprintf(t.Out, "  Evidence: %s\n", f.Evidence)
	}
	if f.FixHint != "" {
		fmt.Fprintf(t.Out, "  Fix hint: %s\n", f.FixHint)
	}
}

// Decide implements UI.
func (t *TextUI) Decide(f schema.MergedFinding) (string, bool) {
	t.showFinding(f)
	for i, o := range f.Options {
		rec := ""
		if o == f.RecommendedOption {
			rec = "  (recommended)"
		}
		fmt.Fprintf(t.Out, "  %d. %s%s\n", i+1, o, rec)
	}
	for {
		a, err := t.ask("  Choose a number, type your own answer, or type drop (q = quit): ")
		switch {
		case err != nil || a == "q":
			return "", true
		case a == "":
			continue
		case a == "drop":
			return "drop", false
		}
		if n, err := strconv.Atoi(a); err == nil {
			if n >= 1 && n <= len(f.Options) {
				return f.Options[n-1], false
			}
			continue
		}
		return a, false
	}
}

// Rule implements UI.
func (t *TextUI) Rule(f schema.MergedFinding, codexEvidence string) (string, string, bool) {
	t.showFinding(f)
	fmt.Fprintf(t.Out, "  Convergence disputed it: %s\n", f.DisputeReason)
	fmt.Fprintf(t.Out, "  Codex keeps it: %s\n", codexEvidence)
	return t.fixOrDrop()
}

// Recurring implements UI.
func (t *TextUI) Recurring(f schema.MergedFinding, lineage int) (string, string, bool) {
	t.showFinding(f)
	fmt.Fprintf(t.Out, "  This problem came back %d times after being fixed (latest: recurs_of %s).\n", lineage, f.RecursOf)
	return t.fixOrDrop()
}

// Confirm implements UI.
func (t *TextUI) Confirm(text string) bool {
	fmt.Fprintf(t.Out, "\n%s\n", strings.TrimRight(text, "\n"))
	a, err := t.ask("  Was this change expected, for example a teammate's merge? Type y to go on; anything else keeps the run paused: ")
	return err == nil && (strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"))
}

func (t *TextUI) fixOrDrop() (string, string, bool) {
	for {
		a, err := t.ask("  Type fix or drop, optionally followed by a note (q = quit): ")
		if err != nil || a == "q" {
			return "", "", true
		}
		word, note, _ := strings.Cut(a, " ")
		if word == "fix" || word == "drop" {
			return word, strings.TrimSpace(note), false
		}
	}
}
