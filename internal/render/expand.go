// Package render fills prompt templates (SPEC section 14).
package render

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MissingError lists placeholders that have no value.
type MissingError struct{ Names []string }

func (e *MissingError) Error() string {
	return "the template uses placeholders that have no value: ${" + strings.Join(e.Names, "}, ${") + "}"
}

// Expand replaces each ${name} with vars[name] (DECISIONS.md D13):
//   - only ${name} is a placeholder, and a name without a value is an error;
//   - $${ gives a literal ${;
//   - a bare $word, or a $ not followed by {, stays as it is.
//
// An unclosed ${ or an invalid name inside ${...} is an error too, so a
// typo can't reach an agent.
func Expand(tmpl string, vars map[string]string) (string, error) {
	var b strings.Builder
	var missing []string
	for i := 0; i < len(tmpl); {
		switch {
		case strings.HasPrefix(tmpl[i:], "$${"):
			b.WriteString("${")
			i += 3
		case strings.HasPrefix(tmpl[i:], "${"):
			end := strings.IndexByte(tmpl[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("line %d: a placeholder starts with ${ but has no closing }", lineAt(tmpl, i))
			}
			name := tmpl[i+2 : i+2+end]
			if !nameRe.MatchString(name) {
				return "", fmt.Errorf("line %d: ${%s} is not a valid placeholder name (write $${ for a literal ${)", lineAt(tmpl, i), name)
			}
			v, ok := vars[name]
			if !ok {
				if !slices.Contains(missing, name) {
					missing = append(missing, name)
				}
			}
			b.WriteString(v)
			i += 2 + end + 1
		default:
			b.WriteByte(tmpl[i])
			i++
		}
	}
	if len(missing) > 0 {
		return "", &MissingError{Names: missing}
	}
	return b.String(), nil
}

// Names returns the placeholder names a template uses, in order.
func Names(tmpl string) []string {
	var names []string
	for i := 0; i < len(tmpl); {
		switch {
		case strings.HasPrefix(tmpl[i:], "$${"):
			i += 3
		case strings.HasPrefix(tmpl[i:], "${"):
			end := strings.IndexByte(tmpl[i+2:], '}')
			if end < 0 {
				return names
			}
			if n := tmpl[i+2 : i+2+end]; nameRe.MatchString(n) && !slices.Contains(names, n) {
				names = append(names, n)
			}
			i += 2 + end + 1
		default:
			i++
		}
	}
	return names
}

func lineAt(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }
