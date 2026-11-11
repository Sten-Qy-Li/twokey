package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 9, 30, 5, 0, time.UTC)

func entry(label string, at time.Time, body string) Entry {
	return Entry{Label: label, Time: at, RunID: "20261114T0930Z-checkpoint-3", Round: 2, Step: "codex-audit", Body: body}
}

func TestInsertLayout(t *testing.T) {
	file := "# Log\n\nRules mention `" + marker + "` inline.\n\n" + marker + "\n\n### Old — 2026-11-01T00:00:00Z\n\nold body\n"
	got, err := Insert([]byte(file), marker, entry("Codex - GPT-6.1-Sol", t0, "- **Task:** audit\n- **Verdict:** not ready\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Log\n\nRules mention `" + marker + "` inline.\n\n" + marker + "\n" +
		"\n" +
		"### Codex - GPT-6.1-Sol — 2026-11-14T09:30:05Z\n" +
		"\n" +
		"- **twokey:** run `20261114T0930Z-checkpoint-3`, round 2, step `codex-audit`.\n" +
		"- **Task:** audit\n" +
		"- **Verdict:** not ready\n" +
		"\n" +
		"### Old — 2026-11-01T00:00:00Z\n\nold body\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestInsertKeepsCRLFAndBOM(t *testing.T) {
	file := "\xEF\xBB\xBF# Log\r\n\r\n" + marker + "\r\n\r\n### Old\r\n"
	got, err := Insert([]byte(file), marker, entry("L", t0, "line one\nline two\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF# Log\r\n") {
		t.Errorf("BOM lost: %q", s[:12])
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Errorf("a lone LF crept in:\n%q", s)
	}
	if !strings.Contains(s, marker+"\r\n\r\n### L — 2026-11-14T09:30:05Z\r\n\r\n- **twokey:**") ||
		!strings.Contains(s, "line two\r\n\r\n### Old\r\n") {
		t.Errorf("layout:\n%q", s)
	}
}

func TestInsertBodyRules(t *testing.T) {
	body := strings.Join([]string{
		"# Big heading",
		"## Section",
		"### Sub",
		"#### Already small",
		"#hashtag stays",
		"```sh",
		"# a shell comment, not a heading",
		"echo $HOME",
		"```",
		marker,
		"text mentioning " + marker + " stays",
		"~~~",
		"## inside tilde fence",
		"~~~",
		"## after the fence",
	}, "\n")
	got, err := Insert([]byte(marker+"\n"), marker, entry("L", t0, body))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"#### Big heading",
		"#### Section",
		"#### Sub",
		"#### Already small",
		"#hashtag stays",
		"```sh",
		"# a shell comment, not a heading",
		"echo $HOME",
		"```",
		"text mentioning " + marker + " stays",
		"~~~",
		"## inside tilde fence",
		"~~~",
		"#### after the fence",
	}, "\n") + "\n"
	if !strings.HasSuffix(string(got), want) {
		t.Errorf("got:\n%s\nwant it to end with:\n%s", got, want)
	}
	if n := len(Inspect(got, marker).MarkerLines); n != 1 {
		t.Errorf("marker lines after insert: %d", n)
	}
}

func TestInsertMarkerProblems(t *testing.T) {
	for file, count := range map[string]int{"# Log\n": 0, marker + "\n" + marker + "\n": 2} {
		_, err := Insert([]byte(file), marker, entry("L", t0, "x"))
		var me *MarkerError
		if !errors.As(err, &me) || me.Count != count {
			t.Errorf("%q: got %v, want a MarkerError with count %d", file, err, count)
		}
	}
}

func TestInsertMarkerIsLastLine(t *testing.T) {
	got, err := Insert([]byte("# Log\n"+marker), marker, entry("L", t0, "body"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# Log\n"+marker+"\n\n### L — ") || !strings.HasSuffix(string(got), "body\n") {
		t.Errorf("got %q", got)
	}
}

func TestEntryWithoutRound(t *testing.T) {
	e := Entry{Label: "twokey vdev", Time: t0, RunID: "r", Step: "decision", Body: "x"}
	got, _ := Insert([]byte(marker+"\n"), marker, e)
	if !strings.Contains(string(got), "- **twokey:** run `r`, step `decision`.\n") {
		t.Errorf("got %q", got)
	}
}

func writeLog(t *testing.T, content string) (path, backups string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "log.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(dir, "log-backups")
}

// Two inserts, ordered by completion time, leave the newer one on top. A
// change made to the file between the two inserts is kept, because each
// insert reads the file again.
func TestInsertFileOrderAndRereads(t *testing.T) {
	path, backups := writeLog(t, "# Log\n\n"+marker+"\n")
	if _, err := InsertFile(path, marker, entry("Older", t0, "a"), backups); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), "# Log", "# Log (edited by hand)", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := InsertFile(path, marker, entry("Newer", t0.Add(time.Minute), "b"), backups)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, "(edited by hand)") {
		t.Error("the hand edit was lost")
	}
	if strings.Index(s, "### Newer") > strings.Index(s, "### Older") {
		t.Errorf("newer entry must be on top:\n%s", s)
	}
	if h, _ := Hash(path); h != hash {
		t.Errorf("returned hash %s, file hash %s", hash, h)
	}
}

func TestInsertFileKeepsThreeBackups(t *testing.T) {
	path, backups := writeLog(t, marker+"\n")
	for i := range 5 {
		if _, err := InsertFile(path, marker, entry("L", t0.Add(time.Duration(i)*time.Second), "x"), backups); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := os.ReadDir(backups)
	if len(files) != 3 {
		t.Fatalf("want 3 backups, got %d", len(files))
	}
	if files[0].Name() != "20261114T093007.000Z-log.md" {
		t.Errorf("oldest kept backup: %s", files[0].Name())
	}
}

func TestInsertFileRetriesWhenBusy(t *testing.T) {
	path, backups := writeLog(t, marker+"\n")
	var waits []time.Duration
	sleep = func(d time.Duration) { waits = append(waits, d) }
	defer func() { rename, sleep = os.Rename, time.Sleep }()

	fails := 2
	rename = func(from, to string) error {
		if fails > 0 {
			fails--
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: busyErr}
		}
		return os.Rename(from, to)
	}
	if _, err := InsertFile(path, marker, entry("L", t0, "x"), backups); err != nil {
		t.Fatalf("should succeed after 2 retries: %v", err)
	}
	if len(waits) != 2 {
		t.Errorf("waits = %v", waits)
	}

	waits = nil
	rename = func(from, to string) error { return &os.LinkError{Op: "rename", Old: from, New: to, Err: busyErr} }
	_, err := InsertFile(path, marker, entry("L", t0.Add(time.Second), "y"), backups)
	var be *BusyError
	if !errors.As(err, &be) || len(waits) != 5 {
		t.Errorf("want a BusyError after 5 retries, got %v after %d waits", err, len(waits))
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".log.md.twokey-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
