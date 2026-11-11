package logfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry is one log entry for twokey to insert.
type Entry struct {
	Label string    // for example "Codex - GPT-6.1-Sol"
	Time  time.Time // when the step finished
	RunID string
	Round int    // 0 for entries that belong to no round
	Step  string // for example "codex-audit"
	Body  string // the agent's log_entry_markdown, or twokey's own text
}

// Heading returns the entry's heading line: "### <label> — <UTC>".
func (e Entry) Heading() string {
	return fmt.Sprintf("### %s — %s", e.Label, e.Time.UTC().Format("2006-01-02T15:04:05Z"))
}

// MarkerError means the marker line is missing or appears more than once.
type MarkerError struct{ Count int }

func (e *MarkerError) Error() string {
	if e.Count == 0 {
		return "the marker line is missing"
	}
	return fmt.Sprintf("the marker line appears %d times; it must appear once", e.Count)
}

var headingRe = regexp.MustCompile(`^#{1,3}(\s|$)`)

// Body returns the entry's text without the heading: the twokey line, then
// the body with headings demoted to #### (except inside code fences, D16)
// and marker lines removed (D17). Lines end with "\n".
func (e Entry) body(marker string) string {
	first := fmt.Sprintf("- **twokey:** run `%s`, step `%s`.", e.RunID, e.Step)
	if e.Round > 0 {
		first = fmt.Sprintf("- **twokey:** run `%s`, round %d, step `%s`.", e.RunID, e.Round, e.Step)
	}
	lines := []string{first}
	text := strings.ReplaceAll(e.Body, "\r\n", "\n")
	text = strings.Trim(text, "\n")
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == marker {
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		case headingRe.MatchString(line):
			line = "####" + strings.TrimLeft(line, "#")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

// Insert returns data with the entry inserted directly under the marker
// line (SPEC section 7): marker, blank line, heading, blank line, body,
// blank line, then the previous entry. The file's BOM and line endings are
// kept; new lines use the marker line's ending.
func Insert(data []byte, marker string, e Entry) ([]byte, error) {
	info := Inspect(data, marker)
	if len(info.MarkerLines) != 1 {
		return nil, &MarkerError{Count: len(info.MarkerLines)}
	}

	// Find the end of the marker line (after its line break, if any).
	start := 0
	if info.BOM {
		start = len(bom)
	}
	pos := start
	for n := 1; n < info.MarkerLines[0]; n++ {
		pos += bytes.IndexByte(data[pos:], '\n') + 1
	}
	eol := "\n"
	lineEnd := len(data)
	if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
		lineEnd = pos + i + 1
		if i > 0 && data[pos+i-1] == '\r' {
			eol = "\r\n"
		}
	} else if info.LineEnding == CRLF {
		eol = "\r\n"
	}

	head := data[:lineEnd]
	rest := data[lineEnd:]
	// Drop the blank lines between the marker and the previous entry; the
	// new entry brings its own.
	for {
		switch {
		case bytes.HasPrefix(rest, []byte("\r\n")):
			rest = rest[2:]
			continue
		case bytes.HasPrefix(rest, []byte("\n")):
			rest = rest[1:]
			continue
		}
		break
	}

	var b bytes.Buffer
	b.Write(head)
	if lineEnd == len(data) && !bytes.HasSuffix(head, []byte("\n")) {
		b.WriteString(eol) // the marker was the last line, without a line break
	}
	b.WriteString(eol)
	b.WriteString(e.Heading() + eol)
	b.WriteString(eol)
	b.WriteString(strings.ReplaceAll(e.body(marker), "\n", eol))
	if len(rest) > 0 {
		b.WriteString(eol)
		b.Write(rest)
	}
	return b.Bytes(), nil
}

// Hash returns the SHA-256 of a file, in hex.
func Hash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// BusyError means the log file stayed locked by another program.
type BusyError struct {
	Path string
	Err  error
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("%s is open in another program, and twokey can't replace it: %v", e.Path, e.Err)
}

func (e *BusyError) Unwrap() error { return e.Err }

// These are variables so tests can simulate a locked file.
var (
	rename = os.Rename
	sleep  = time.Sleep
)

// Retries and their waits when Windows reports the file as busy.
var retryWaits = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond}

// InsertFile runs the insert procedure on the file (SPEC section 7, steps
// 2 and 4–6). The caller holds the run lock. It re-reads the file, keeps
// a backup in backupDir (the last 3), writes a temp file in the same
// folder, syncs it and renames it over the log. It returns the new file's
// SHA-256.
func InsertFile(path, marker string, e Entry, backupDir string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	out, err := Insert(data, marker, e)
	if err != nil {
		return "", err
	}
	if err := backup(data, filepath.Base(path), backupDir, e.Time); err != nil {
		return "", fmt.Errorf("can't save a backup of the log: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".twokey-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if fi, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpName, fi.Mode().Perm())
	}

	err = rename(tmpName, path)
	for i := 0; err != nil && busy(err) && i < len(retryWaits); i++ {
		sleep(retryWaits[i])
		err = rename(tmpName, path)
	}
	if err != nil {
		if busy(err) {
			return "", &BusyError{Path: path, Err: err}
		}
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

func backup(data []byte, name, dir string, t time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stamp := t.UTC().Format("20060102T150405.000Z")
	if err := os.WriteFile(filepath.Join(dir, stamp+"-"+name), data, 0o644); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), "-"+name) {
			names = append(names, en.Name())
		}
	}
	sort.Strings(names) // the time stamp sorts oldest first
	for len(names) > 3 {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		names = names[1:]
	}
	return nil
}
