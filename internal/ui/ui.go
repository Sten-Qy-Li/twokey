// Package ui prints twokey's status lines. Labels are plain ASCII so they
// look the same in every terminal: [OK], [WARN], [FAIL], [PAUSE], [..].
package ui

import (
	"fmt"
	"io"
	"unicode"
)

// Status is the label in front of a message.
type Status int

// The status labels.
const (
	OK Status = iota
	Warn
	Fail
	Pause
	Busy
)

// Label returns the ASCII label, for example "[WARN]".
func (s Status) Label() string {
	switch s {
	case OK:
		return "[OK]"
	case Warn:
		return "[WARN]"
	case Fail:
		return "[FAIL]"
	case Pause:
		return "[PAUSE]"
	default:
		return "[..]"
	}
}

// Line prints one status line: the label, padded so messages line up.
func Line(w io.Writer, s Status, msg string) {
	fmt.Fprintf(w, "%-7s %s\n", s.Label(), msg)
}

// Next prints an indented "Next:" line under a status line.
func Next(w io.Writer, msg string) {
	fmt.Fprintf(w, "%-7s Next: %s\n", "", msg)
}

// Detail prints an indented line under a status line.
func Detail(w io.Writer, msg string) {
	fmt.Fprintf(w, "%-7s %s\n", "", msg)
}

// Sentence starts an error message with a capital letter, for showing it
// as a sentence. Go error strings start in lower case.
func Sentence(msg string) string {
	r := []rune(msg)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}
