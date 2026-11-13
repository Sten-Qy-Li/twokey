package schedule

import (
	"encoding/binary"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf16"
)

// Task is what the Task Scheduler task runs.
type Task struct {
	Exe    string // twokey.exe, absolute
	Repo   string // the repository root: the working folder
	Config string // the config file, absolute
}

// TaskXML builds the task for a schedule (DECISIONS.md D65). The settings
// were checked on the owner's machine with a disabled test task:
//   - it runs only while the owner is logged on (InteractiveToken), in their
//     session, with their logins, SSH key and Docker Desktop; no password
//     is stored, and the task registers without admin rights;
//   - it wakes the computer, doesn't start on battery, and isn't stopped
//     when the computer switches to battery (that would kill a round);
//   - a start missed while the computer was off isn't made up later;
//   - a second start while one runs is allowed, so twokey itself records
//     the skip;
//   - 24 hours is a backstop: twokey's own timeouts are shorter.
//
// Times are local, without a zone, as Task Scheduler expects. The file is
// UTF-16 with a byte order mark, as schtasks /XML reads it.
func TaskXML(s Schedule, t Task, enabled bool, now time.Time) []byte {
	var trig strings.Builder
	end := ""
	if s.Until != "" {
		end = fmt.Sprintf("\n      <EndBoundary>%sT23:59:59</EndBoundary>", s.Until)
	}
	for _, hm := range s.Daily {
		fmt.Fprintf(&trig, `
    <CalendarTrigger>
      <StartBoundary>%sT%s:00</StartBoundary>%s
      <Enabled>true</Enabled>
      <ScheduleByDay>
        <DaysInterval>1</DaysInterval>
      </ScheduleByDay>
    </CalendarTrigger>`, now.Format(day), hm, end)
	}
	for _, o := range s.Once {
		start, err := time.ParseInLocation(slot, o, now.Location())
		if err != nil || !start.After(now) || (s.Until != "" && start.Format(day) > s.Until) {
			continue // past one-time starts would never fire
		}
		fmt.Fprintf(&trig, `
    <TimeTrigger>
      <StartBoundary>%s</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>`, start.Format("2006-01-02T15:04:05"))
	}
	return taskXML(trig.String(), t, "schedule fire", enabled, "", "twokey runs one round of the loop at each start time. Change it with twokey schedule, not here.")
}

// CheckTaskXML builds a one-time task that runs a slot's checks at start,
// without agents ("twokey schedule test"). Task Scheduler deletes it an
// hour after it expires.
func CheckTaskXML(t Task, start time.Time) []byte {
	trig := fmt.Sprintf(`
    <TimeTrigger>
      <StartBoundary>%s</StartBoundary>
      <EndBoundary>%s</EndBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>`, start.Format("2006-01-02T15:04:05"), start.Add(time.Hour).Format("2006-01-02T15:04:05"))
	return taskXML(trig, t, "schedule fire --test", true, "\n    <DeleteExpiredTaskAfter>PT1H</DeleteExpiredTaskAfter>",
		"twokey's test of a scheduled start: it checks what a round needs, without agents.")
}

func taskXML(triggers string, t Task, command string, enabled bool, extra, description string) []byte {
	x := html.EscapeString
	doc := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%s</Description>
  </RegistrationInfo>
  <Triggers>%s
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>true</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>%t</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>true</WakeToRun>
    <ExecutionTimeLimit>PT24H</ExecutionTimeLimit>%s
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s --config "%s"</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, x(description), triggers, enabled, extra, x(t.Exe), command, x(t.Config), x(t.Repo))
	return utf16File(strings.ReplaceAll(doc, "\n", "\r\n"))
}

// utf16File encodes text as UTF-16, little endian, with a byte order mark.
func utf16File(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2+2*i:], u)
	}
	return out
}
