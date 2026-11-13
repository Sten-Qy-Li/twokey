// Package schedule starts single rounds at set times with Windows Task
// Scheduler ("twokey schedule", SPEC section 12, DECISIONS.md D65, D66).
// The schedule lives in <run_dir>/schedule.json; the task is built from it.
package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Files in the run root.
const (
	FileName    = "schedule.json"
	HistoryFile = "schedule-history.jsonl"
	LogDir      = "schedule-logs"
)

// Schedule is what "twokey schedule" keeps.
type Schedule struct {
	On    bool     `json:"on"`
	Daily []string `json:"daily,omitempty"` // "HH:MM", local time
	Once  []string `json:"once,omitempty"`  // "YYYY-MM-DD HH:MM", local time
	Until string   `json:"until,omitempty"` // "YYYY-MM-DD": the last day a round may start; "" = no end
	// Since is when the schedule was last switched on; slots before it
	// can't have been missed.
	Since time.Time `json:"since,omitzero"`
	// LastLooked is the last "twokey schedule status", for "since you last
	// looked".
	LastLooked time.Time `json:"last_looked,omitzero"`
	// Program is the twokey.exe the starts run, a tagged release build
	// (D68): set by "schedule program", or by the first "schedule on".
	Program string `json:"program,omitempty"`
}

const (
	clock = "15:04"
	day   = "2006-01-02"
	slot  = day + " " + clock
)

// ParseSlot reads a start time: "HH:MM" for every day, or "YYYY-MM-DD
// HH:MM" (or the two as separate arguments) for once.
func ParseSlot(args []string) (daily bool, value string, err error) {
	s := strings.TrimSpace(strings.Join(args, " "))
	if t, err := time.Parse(clock, s); err == nil {
		return true, t.Format(clock), nil
	}
	if t, err := time.Parse(slot, s); err == nil {
		return false, t.Format(slot), nil
	}
	return false, "", fmt.Errorf("%q isn't a time. Use HH:MM for every day (for example 02:00), or YYYY-MM-DD HH:MM for once (for example 2026-11-14 13:30)", s)
}

// ParseDay reads an end date, YYYY-MM-DD.
func ParseDay(s string) (string, error) {
	t, err := time.Parse(day, strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("%q isn't a date. Use YYYY-MM-DD, for example 2026-11-20", s)
	}
	return t.Format(day), nil
}

// Add adds a start time. It reports false if the schedule already has it.
func (s *Schedule) Add(daily bool, value string) bool {
	list := &s.Once
	if daily {
		list = &s.Daily
	}
	if slices.Contains(*list, value) {
		return false
	}
	*list = append(*list, value)
	slices.Sort(*list)
	return true
}

// Remove removes a start time, or all of them for "all". It reports
// whether anything was removed.
func (s *Schedule) Remove(daily bool, value string) bool {
	n := len(s.Daily) + len(s.Once)
	if value == "all" {
		s.Daily, s.Once = nil, nil
	} else if daily {
		s.Daily = slices.DeleteFunc(s.Daily, func(v string) bool { return v == value })
	} else {
		s.Once = slices.DeleteFunc(s.Once, func(v string) bool { return v == value })
	}
	return len(s.Daily)+len(s.Once) < n
}

// Ended reports whether the end date is behind now.
func (s Schedule) Ended(now time.Time) bool {
	return s.Until != "" && now.Format(day) > s.Until
}

// Times returns the planned starts in (from, to], in order, up to the end
// date.
func (s Schedule) Times(from, to time.Time) []time.Time {
	loc := to.Location()
	var out []time.Time
	add := func(t time.Time) {
		if t.After(from) && !t.After(to) && (s.Until == "" || t.Format(day) <= s.Until) {
			out = append(out, t)
		}
	}
	for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); !d.After(to); d = d.AddDate(0, 0, 1) {
		for _, hm := range s.Daily {
			if c, err := time.ParseInLocation(clock, hm, loc); err == nil {
				add(time.Date(d.Year(), d.Month(), d.Day(), c.Hour(), c.Minute(), 0, 0, loc))
			}
		}
	}
	for _, o := range s.Once {
		if t, err := time.ParseInLocation(slot, o, loc); err == nil {
			add(t)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return slices.Compact(out)
}

// Next returns the next planned start after now, or false if there is none.
func (s Schedule) Next(now time.Time) (time.Time, bool) {
	limit := now.AddDate(0, 0, 2) // a daily slot comes within a day
	for _, o := range s.Once {
		if t, err := time.ParseInLocation(slot, o, now.Location()); err == nil && t.After(limit) {
			limit = t
		}
	}
	if ts := s.Times(now, limit); len(ts) > 0 {
		return ts[0], true
	}
	return time.Time{}, false
}

// Planned returns the slot a start at now belongs to: the latest planned
// start in the last 30 minutes (Task Scheduler may start a little late),
// or false for a start outside the schedule.
func (s Schedule) Planned(now time.Time) (time.Time, bool) {
	ts := s.Times(now.Add(-30*time.Minute), now.Add(time.Minute))
	if len(ts) == 0 {
		return time.Time{}, false
	}
	return ts[len(ts)-1], true
}

// Describe says when rounds start, in one line.
func (s Schedule) Describe() string {
	var parts []string
	if len(s.Daily) > 0 {
		parts = append(parts, "every day at "+andList(s.Daily))
	}
	for _, o := range s.Once {
		if t, err := time.Parse(slot, o); err == nil {
			parts = append(parts, "once on "+t.Format("Mon 2 Jan 15:04"))
		}
	}
	if len(parts) == 0 {
		return "no start times"
	}
	text := strings.Join(parts, "; ")
	if s.Until != "" {
		if t, err := time.Parse(day, s.Until); err == nil {
			text += "; until " + t.Format("Mon 2 Jan")
		}
	}
	return text
}

func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// TaskName is the Task Scheduler task for a repository: the folder name,
// plus a short hash of the full path, so two clones don't share a task.
func TaskName(root string) string {
	h := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return fmt.Sprintf("twokey %s %s", filepath.Base(root), hex.EncodeToString(h[:3]))
}

// Load reads the schedule. A missing file is an empty schedule, switched off.
func Load(runRoot string) (Schedule, error) {
	var s Schedule
	data, err := os.ReadFile(filepath.Join(runRoot, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s is damaged: %w", filepath.Join(runRoot, FileName), err)
	}
	return s, nil
}

// Save writes the schedule.
func Save(runRoot string, s Schedule) error {
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(runRoot, FileName+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(runRoot, FileName))
}
