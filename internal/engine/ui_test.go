package engine_test

import (
	"io"
	"strings"
	"testing"

	"github.com/Sten-Qy-Li/twokey/internal/engine"
)

// Only an explicit yes confirms a protected branch change (D40). The
// question is asked in autopilot too.
func TestConfirm(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{" yes \n", true},
		{"y", true}, // the last line without a line break
		{"\n", false},
		{"n\n", false},
		{"yeah\n", false},
		{"", false}, // no input
	} {
		u := engine.NewTextUI(strings.NewReader(tc.input), io.Discard, true)
		if got := u.Confirm("main moved"); got != tc.want {
			t.Errorf("Confirm with input %q = %v, want %v", tc.input, got, tc.want)
		}
	}
}
