package runner

import (
	"testing"
	"time"
)

var patterns = []string{"hit your", "usage limit", "rate limit", "try again"}

func TestFindLimit(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.Local) // a Saturday
	at := func(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.Local) }
	tests := []struct {
		name, text string
		want       time.Time
	}{
		{"codex today", "You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at 3:45 PM.", at(14, 15, 45)},
		{"codex later today", "You’ve hit your usage limit. Try again at 9:05 AM.", at(15, 9, 5)},
		{"codex with date", "You’ve hit your usage limit. Try again at Nov 16th, 2026 1:30 AM.", at(16, 1, 30)},
		{"codex later", "You’ve hit your usage limit. Try again later.", time.Time{}},
		{"claude clock", "You've hit your session limit · resets 3:45pm", at(14, 15, 45)},
		{"claude hour only", "You've hit your Opus limit · resets 11pm", at(14, 23, 0)},
		{"claude weekday", "You've hit your weekly limit · resets Mon 12:00am", at(16, 0, 0)},
		{"claude date", "spend limit reached · resets Dec 1 · rate limit", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"old epoch", "Claude AI usage limit reached|1794643200", time.Unix(1794643200, 0)},
		{"rate limit", "stream error: rate limit exceeded: too many requests", time.Time{}},
	}
	for _, tt := range tests {
		l := FindLimit(tt.text, patterns, now)
		if l == nil {
			t.Errorf("%s: no limit found", tt.name)
			continue
		}
		if !l.Reset.Equal(tt.want) {
			t.Errorf("%s: reset %v, want %v", tt.name, l.Reset, tt.want)
		}
	}
	if l := FindLimit("tests failed: 3 of 40", patterns, now); l != nil {
		t.Errorf("not a limit: %+v", l)
	}
}

func TestFindLimitMessage(t *testing.T) {
	l := FindLimit("some output\nYou've hit your session limit · resets 3pm\nmore", patterns, time.Now())
	if l == nil || l.Message != "You've hit your session limit · resets 3pm" {
		t.Errorf("message: %+v", l)
	}
}
