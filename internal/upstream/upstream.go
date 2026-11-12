// Package upstream decides how the working branch stands to the upstream
// branch, the one the owner brings it up to (SPEC section 3, "Upstream").
// It is pure logic; doctor and the engine run git and report the answer.
// twokey never moves the branch (DECISIONS.md D57).
package upstream

import "fmt"

// Relation is how <remote>/<branch> stands to <remote>/<upstream>.
type Relation int

// The relations. Behind and Diverged are warnings: the owner acts on them.
const (
	Equal    Relation = iota // same commit
	Ahead                    // the branch has commits the upstream lacks
	Behind                   // the branch is an ancestor of the upstream
	Diverged                 // each has commits the other lacks
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

// Advice says what the owner can do about a branch that is behind or has
// diverged; "" for the other relations.
func Advice(rel Relation, remote, branch, upstream string) string {
	ff := fmt.Sprintf("git merge --ff-only %s/%s and git push yourself", remote, upstream)
	switch rel {
	case Behind:
		return "to bring those commits in, run " + ff + "."
	case Diverged:
		return fmt.Sprintf("merge %s into %s through a merge request with a merge commit (not squash: a squash merge keeps them diverged). Then run %s.",
			branch, upstream, ff)
	default:
		return ""
	}
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
