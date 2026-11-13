package schedule

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

var loc = time.FixedZone("test", 2*3600)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseSlot(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		daily bool
		value string
		ok    bool
	}{
		{[]string{"02:00"}, true, "02:00", true},
		{[]string{"23:59"}, true, "23:59", true},
		{[]string{"2026-11-14 13:30"}, false, "2026-11-14 13:30", true},
		{[]string{"2026-11-14", "13:30"}, false, "2026-11-14 13:30", true},
		{[]string{"24:00"}, false, "", false},
		{[]string{"tomorrow"}, false, "", false},
		{[]string{"2026-02-30 10:00"}, false, "", false},
	} {
		daily, value, err := ParseSlot(tc.args)
		if (err == nil) != tc.ok || daily != tc.daily || value != tc.value {
			t.Errorf("ParseSlot(%q) = %v, %q, %v", tc.args, daily, value, err)
		}
	}
	if _, err := ParseDay("2026-11-20"); err != nil {
		t.Error(err)
	}
	if _, err := ParseDay("20.11.2026"); err == nil || !strings.Contains(err.Error(), "Use YYYY-MM-DD") {
		t.Errorf("err = %v", err)
	}
}

func TestAddRemove(t *testing.T) {
	var s Schedule
	if !s.Add(true, "13:30") || !s.Add(true, "02:00") || s.Add(true, "02:00") || !s.Add(false, "2026-11-14 09:00") {
		t.Fatal("add")
	}
	if !reflect.DeepEqual(s.Daily, []string{"02:00", "13:30"}) {
		t.Errorf("daily %v", s.Daily)
	}
	if s.Remove(true, "05:00") || !s.Remove(true, "02:00") || !s.Remove(false, "all") || len(s.Daily)+len(s.Once) != 0 {
		t.Errorf("remove: %+v", s)
	}
}

func TestTimes(t *testing.T) {
	s := Schedule{Daily: []string{"02:00", "13:30"}, Once: []string{"2026-11-14 09:00"}, Until: "2026-11-15"}
	got := s.Times(at("2026-11-13 14:00"), at("2026-11-16 23:00"))
	want := []time.Time{at("2026-11-14 02:00"), at("2026-11-14 09:00"), at("2026-11-14 13:30"), at("2026-11-15 02:00"), at("2026-11-15 13:30")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if next, ok := s.Next(at("2026-11-14 02:00")); !ok || !next.Equal(at("2026-11-14 09:00")) {
		t.Errorf("next %v %v", next, ok)
	}
	if _, ok := s.Next(at("2026-11-15 14:00")); ok {
		t.Error("no start after the end date")
	}
	if p, ok := s.Planned(at("2026-11-14 02:07")); !ok || !p.Equal(at("2026-11-14 02:00")) {
		t.Errorf("planned %v %v", p, ok)
	}
	if _, ok := s.Planned(at("2026-11-14 03:00")); ok {
		t.Error("03:00 belongs to no start")
	}
	if s.Ended(at("2026-11-15 23:59")) || !s.Ended(at("2026-11-16 00:00")) {
		t.Error("ended")
	}
	if d := s.Describe(); d != "every day at 02:00 and 13:30; once on Sat 14 Nov 09:00; until Sun 15 Nov" {
		t.Errorf("describe: %s", d)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	if s, err := Load(dir); err != nil || s.On {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	want := Schedule{On: true, Daily: []string{"02:00"}, Until: "2026-11-20", Since: at("2026-11-13 10:00").UTC()}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "is damaged") {
		t.Errorf("err = %v", err)
	}
}

func TestTaskName(t *testing.T) {
	a, b := TaskName(filepath.Join("work", "project")), TaskName(filepath.Join("clone", "project"))
	if !strings.HasPrefix(a, "twokey project ") || a == b || a != TaskName(filepath.Join("WORK", "project")) {
		t.Errorf("%q %q", a, b)
	}
}

func decode(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE || len(b)%2 != 0 {
		t.Fatalf("not UTF-16 LE with a BOM: % x", b[:4])
	}
	u := make([]uint16, (len(b)-2)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	return string(utf16.Decode(u))
}

func TestTaskXML(t *testing.T) {
	s := Schedule{Daily: []string{"02:00"}, Once: []string{"2026-11-12 09:00", "2026-11-14 09:00"}, Until: "2026-11-20"}
	task := Task{Exe: `C:\tools\twokey.exe`, Repo: `C:\work\R&D project`, Config: `C:\work\R&D project\local-only_twokey.toml`}
	x := decode(t, TaskXML(s, task, true, at("2026-11-13 15:00")))
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		"<StartBoundary>2026-11-13T02:00:00</StartBoundary>\r\n      <EndBoundary>2026-11-20T23:59:59</EndBoundary>",
		"<DaysInterval>1</DaysInterval>",
		"<TimeTrigger>\r\n      <StartBoundary>2026-11-14T09:00:00</StartBoundary>",
		"<LogonType>InteractiveToken</LogonType>",
		"<MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>",
		"<DisallowStartIfOnBatteries>true</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<StartWhenAvailable>false</StartWhenAvailable>",
		"<Enabled>true</Enabled>\r\n    <Hidden>false</Hidden>",
		"<WakeToRun>true</WakeToRun>",
		"<ExecutionTimeLimit>PT24H</ExecutionTimeLimit>",
		`<Command>C:\tools\twokey.exe</Command>`,
		`<Arguments>schedule fire --config "C:\work\R&amp;D project\local-only_twokey.toml"</Arguments>`,
		`<WorkingDirectory>C:\work\R&amp;D project</WorkingDirectory>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %q:\n%s", want, x)
		}
	}
	if strings.Contains(x, "2026-11-12T09:00") || strings.Contains(x, "<UserId>") {
		t.Errorf("a past start or a user ID is in the XML:\n%s", x)
	}
	if !strings.Contains(decode(t, TaskXML(s, task, false, at("2026-11-13 15:00"))), "<Enabled>false</Enabled>\r\n    <Hidden>") {
		t.Error("a disabled task")
	}
	test := decode(t, CheckTaskXML(task, at("2026-11-13 15:02")))
	for _, want := range []string{"<StartBoundary>2026-11-13T15:02:00</StartBoundary>", "<EndBoundary>2026-11-13T16:02:00</EndBoundary>",
		"<DeleteExpiredTaskAfter>PT1H</DeleteExpiredTaskAfter>", "<Arguments>schedule fire --test --config "} {
		if !strings.Contains(test, want) {
			t.Errorf("test task lacks %q:\n%s", want, test)
		}
	}
}

// What "schtasks /Query /XML" printed on the owner's machine, shortened:
// one-byte text with \r\r\n line ends, and default settings left out.
func TestParseTaskXML(t *testing.T) {
	disabled := "<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\r\n<Task>\r\r\n  <Triggers>\r\r\n    <TimeTrigger>\r\r\n      <Enabled>true</Enabled>\r\r\n" +
		"    </TimeTrigger>\r\r\n  </Triggers>\r\r\n  <Settings>\r\r\n    <Enabled>false</Enabled>\r\r\n    <WakeToRun>true</WakeToRun>\r\r\n  </Settings>\r\r\n" +
		"  <Actions Context=\"Author\">\r\r\n    <Exec>\r\r\n      <Command>C:\\tools\\twokey.exe</Command>\r\r\n"
	if got := ParseTaskXML(disabled); got != (Info{Exists: true, Enabled: false, Exe: `C:\tools\twokey.exe`}) {
		t.Errorf("disabled: %+v", got)
	}
	enabled := strings.Replace(disabled, "    <Enabled>false</Enabled>\r\r\n", "", 1)
	if got := ParseTaskXML(enabled); !got.Enabled {
		t.Errorf("enabled: %+v", got)
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	start := at("2026-11-14 02:00")
	for _, r := range []Record{
		{Started: start, Outcome: Running},
		{Started: start, Outcome: RoundDone, Round: 2, Lines: []string{"Round 2: fixed R2-M-01"}},
		{Started: start.Add(12 * time.Hour), Outcome: Skipped},
	} {
		if err := Append(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, HistoryFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	h, err := History(dir)
	if err != nil || len(h) != 2 || h[0].Outcome != RoundDone || h[0].Round != 2 || h[1].Outcome != Skipped {
		t.Errorf("history: %+v, %v", h, err)
	}
}
