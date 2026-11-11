package doctor

import (
	"regexp"
	"strings"
)

var (
	flagToken  = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)
	quotedWord = regexp.MustCompile(`"([^"]+)"`)
)

// optionLine returns the option names a --help line defines, such as
// ["-s", "--sandbox"] for "  -s, --sandbox <SANDBOX_MODE>". Lines that
// don't start with an option return nil.
func optionLine(line string) []string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "-") {
		return nil
	}
	// The names end where the argument or the description starts.
	if i := strings.IndexAny(t, "<["); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, "  "); i >= 0 {
		t = t[:i]
	}
	var names []string
	for _, tok := range strings.Split(t, ",") {
		tok = strings.TrimSpace(tok)
		if f := strings.Fields(tok); len(f) > 0 {
			tok = f[0]
		}
		if flagToken.MatchString(tok) {
			names = append(names, tok)
		}
	}
	return names
}

// Flags returns every option a --help text defines.
func Flags(help string) map[string]bool {
	flags := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		for _, n := range optionLine(line) {
			flags[n] = true
		}
	}
	return flags
}

// Missing returns the names in want that the --help text doesn't define.
func Missing(help string, want []string) []string {
	have := Flags(help)
	var miss []string
	for _, w := range want {
		if !have[w] {
			miss = append(miss, w)
		}
	}
	return miss
}

// Choices returns the allowed values an option lists in its help:
//
//	claude: --permission-mode <mode>  ... (choices: "acceptEdits", "auto", ...)
//	codex:  -s, --sandbox <SANDBOX_MODE> ... [possible values: read-only, workspace-write]
//
// It returns nil if the option or its list isn't found.
func Choices(help, flag string) []string {
	lines := strings.Split(help, "\n")
	start := -1
	for i, line := range lines {
		for _, n := range optionLine(line) {
			if n == flag {
				start = i
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return nil
	}
	// The option's text runs until the next option line.
	block := lines[start]
	for _, line := range lines[start+1:] {
		if optionLine(line) != nil {
			break
		}
		block += " " + strings.TrimSpace(line)
	}

	if _, after, ok := strings.Cut(block, "(choices:"); ok {
		list, _, _ := strings.Cut(after, ")")
		var out []string
		for _, m := range quotedWord.FindAllStringSubmatch(list, -1) {
			out = append(out, m[1])
		}
		return out
	}
	if _, after, ok := strings.Cut(block, "[possible values:"); ok {
		list, _, _ := strings.Cut(after, "]")
		var out []string
		for _, v := range strings.Split(list, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}
