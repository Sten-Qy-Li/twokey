package runner

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FindLimit looks for a usage or rate limit in an agent's error text, with
// the configured patterns (case does not matter). It returns nil if none
// matches. The wording changes between CLI versions, so the patterns live
// in the config ([limits] usage_limit_patterns).
func FindLimit(text string, patterns []string, now time.Time) *Limit {
	low := strings.ToLower(text)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || !strings.Contains(low, p) {
			continue
		}
		msg := text
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(strings.ToLower(line), p) {
				msg = line
				break
			}
		}
		msg = strings.TrimSpace(msg)
		if r := []rune(msg); len(r) > 300 {
			msg = string(r[:297]) + "..."
		}
		return &Limit{Message: msg, Reset: ResetTime(text, now)}
	}
	return nil
}

var (
	// Codex: "Try again at 3:45 PM." or "Try again at Nov 14th, 2026 3:45 PM."
	codexAt = regexp.MustCompile(`(?i)try again at ([A-Za-z]{3} \d{1,2}(?:st|nd|rd|th)?, \d{4} )?(\d{1,2}:\d{2} ?[AP]M)`)
	// Claude: "resets 3:45pm", "resets 3pm", "resets Mon 12:00am", "resets Dec 1".
	claudeAt   = regexp.MustCompile(`(?i)resets (?:(mon|tue|wed|thu|fri|sat|sun)[a-z]* )?(\d{1,2}(?::\d{2})? ?[ap]m)`)
	claudeDate = regexp.MustCompile(`(?i)resets ([A-Za-z]{3}) (\d{1,2})\b`)
	ordinal    = regexp.MustCompile(`(\d)(st|nd|rd|th)`)
	epoch      = regexp.MustCompile(`\|(\d{10})\b`)
)

// ResetTime reads when a limit resets from the CLIs' messages. Clock times
// are local. It returns the zero time if the text doesn't say.
func ResetTime(text string, now time.Time) time.Time {
	loc := now.Location()
	if m := codexAt.FindStringSubmatch(text); m != nil {
		clock := strings.ToUpper(strings.ReplaceAll(m[2], " ", ""))
		if m[1] != "" {
			date := ordinal.ReplaceAllString(strings.TrimSpace(m[1]), "$1")
			if t, err := time.ParseInLocation("Jan 2, 2006 3:04PM", date+" "+clock, loc); err == nil {
				return t
			}
		}
		if c, err := time.ParseInLocation("3:04PM", clock, loc); err == nil {
			return nextClock(now, c, -1)
		}
	}
	if m := claudeAt.FindStringSubmatch(text); m != nil {
		clock := strings.ToUpper(strings.ReplaceAll(m[2], " ", ""))
		layout := "3:04PM"
		if !strings.Contains(clock, ":") {
			layout = "3PM"
		}
		if c, err := time.ParseInLocation(layout, clock, loc); err == nil {
			day := -1
			if m[1] != "" {
				day = weekday(m[1])
			}
			return nextClock(now, c, day)
		}
	}
	if m := claudeDate.FindStringSubmatch(text); m != nil {
		if t, err := time.Parse("Jan 2 2006", m[1]+" "+m[2]+" "+strconv.Itoa(now.Year())); err == nil {
			if !t.After(now) {
				t = t.AddDate(1, 0, 0)
			}
			return t // 00:00 UTC on that date
		}
	}
	if m := epoch.FindStringSubmatch(text); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// nextClock returns the next time after now at the clock time of c, on the
// given weekday (-1 = any day).
func nextClock(now, c time.Time, day int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), c.Hour(), c.Minute(), 0, 0, now.Location())
	for i := 0; i < 8; i++ {
		if t.After(now) && (day < 0 || int(t.Weekday()) == day) {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func weekday(s string) int {
	for i, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
		if strings.HasPrefix(strings.ToLower(s), d) {
			return i
		}
	}
	return -1
}
