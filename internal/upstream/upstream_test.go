package upstream

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		ahead, behind int
		want          Relation
	}{
		{0, 0, Equal},
		{1, 0, Ahead},
		{7, 0, Ahead},
		{0, 1, Behind},
		{0, 12, Behind},
		{1, 1, Diverged},
		{3, 5, Diverged},
	}
	for _, tt := range tests {
		if got := Classify(tt.ahead, tt.behind); got != tt.want {
			t.Errorf("Classify(%d, %d) = %d, want %d", tt.ahead, tt.behind, got, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		ahead, behind int
		want          string
	}{
		{0, 0, "origin/b is even with origin/main."},
		{1, 0, "origin/b is 1 commit ahead of origin/main."},
		{0, 3, "origin/b is 3 commits behind origin/main."},
		{2, 1, "origin/b and origin/main have diverged: origin/b has 2 commits that origin/main lacks, and origin/main has 1 commit that origin/b lacks."},
	}
	for _, tt := range tests {
		got := Describe(Classify(tt.ahead, tt.behind), tt.ahead, tt.behind, "origin/b", "origin/main")
		if got != tt.want {
			t.Errorf("Describe(%d, %d) = %q, want %q", tt.ahead, tt.behind, got, tt.want)
		}
	}
}
