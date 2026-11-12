// Package upstream decides how the working branch stands to the upstream
// branch it catches up with (SPEC section 3, "Sync"). It is pure logic; the
// engine runs git and acts on the answer.
package upstream

import "fmt"

// Relation is how <remote>/<branch> stands to <remote>/<upstream>.
type Relation int

// The relations, and what twokey does about each.
const (
	Equal    Relation = iota // same commit: nothing to do
	Ahead                    // the branch has commits the upstream lacks: nothing to do
	Behind                   // the branch is an ancestor of the upstream: fast-forward it
	Diverged                 // each has commits the other lacks: pause (PAUSE_DIVERGED)
)

// Classify takes the commit counts of "git rev-list --left-right --count
// <branch>...<upstream>": ahead = commits only in the branch, behind =
// commits only in the upstream.
func Classify(ahead, behind int) Relation {
	switch {
	case ahead == 0 && behind == 0:
		return Equal
	case behind == 0:
		return Ahead
	case ahead == 0:
		return Behind
	default:
		return Diverged
	}
}

// Describe says in one sentence how branch stands to upstream.
func Describe(rel Relation, ahead, behind int, branch, upstream string) string {
	switch rel {
	case Equal:
		return fmt.Sprintf("%s is even with %s.", branch, upstream)
	case Ahead:
		return fmt.Sprintf("%s is %s ahead of %s.", branch, commits(ahead), upstream)
	case Behind:
		return fmt.Sprintf("%s is %s behind %s.", branch, commits(behind), upstream)
	default:
		return fmt.Sprintf("%s and %s have diverged: %s has %s that %s lacks, and %s has %s that %s lacks.",
			branch, upstream, branch, commits(ahead), upstream, upstream, commits(behind), branch)
	}
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
