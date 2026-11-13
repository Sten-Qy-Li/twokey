package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// pushBlockBase is the push URL prefix every remote gets in the agents'
// environment. It names a git remote helper that doesn't exist, so a push
// fails at once, without a network call, and the error says why.
const pushBlockBase = "twokey-agents-may-not-push::"

// PushBlockEnv returns the environment entries that make git push fail for
// the agents while fetch and ls-remote still work (DECISIONS.md D62):
// url.<pushBlockBase>.pushInsteadOf = "" rewrites every push URL, since
// every URL starts with "". getenv reads GIT_CONFIG_COUNT, so settings
// the owner already passes that way are kept.
func PushBlockEnv(getenv func(string) string) []string {
	n, err := strconv.Atoi(getenv("GIT_CONFIG_COUNT"))
	if err != nil || n < 0 {
		n = 0
	}
	return []string{
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=url.%s.pushInsteadOf", n, pushBlockBase),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=", n),
	}
}

// PushBlocked reports whether a push URL, as "git remote get-url --push"
// prints it, is the blocked one.
func PushBlocked(url string) bool {
	return strings.HasPrefix(strings.TrimSpace(url), pushBlockBase)
}

// PushBypasses lists the git settings that would get a push past the
// block: a remote's own pushurl (pushInsteadOf doesn't rewrite it), and
// any other pushInsteadOf (the longest match wins). twokey doesn't run
// agents while one is set.
func (g Repo) PushBypasses(ctx context.Context) ([]string, error) {
	args := []string{"config", "--get-regexp", `^(remote\..*\.pushurl|url\..*\.pushinsteadof)$`}
	r := g.run(ctx, args...)
	if r.Err != nil {
		if r.ExitCode == 1 { // nothing matched
			return nil, nil
		}
		return nil, &Error{Args: args, ExitCode: r.ExitCode, Stderr: r.Stderr, Err: r.Err}
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// UnsetHint says how to remove one line of PushBypasses.
func UnsetHint(line string) string {
	key, _, _ := strings.Cut(line, " ")
	return fmt.Sprintf("git config --unset-all %s (add --global if it is in your global config)", key)
}
