# twokey

twokey runs a cross-checked audit-and-fix loop between two coding agents, [Codex CLI](https://github.com/openai/codex) and [Claude Code](https://claude.com/claude-code), on one git repository. It keeps going until both agents clear the same commit for a milestone.

The name comes from the two-person rule: the run ends only when both keys turn.

## How it works

Each round:

1. **Blind audits.** Codex and Claude audit the same commit at the same time. Neither sees the other's work.
2. **Convergence.** Claude checks both lists, merges them, and confirms or disputes each serious finding.
3. **Dispute.** If Claude disputed a finding from Codex, Codex checks it again.
4. **Gate.** Plain code decides: stop, pause for the owner, or fix.
5. **Fix.** Claude fixes the confirmed findings and commits. It never pushes.
6. **Verify and push.** twokey checks what the agent did, runs the project's checks, and pushes the branch itself.

The control flow is ordinary code, not a prompt that promises to loop. twokey checks what the agents did instead of trusting their reports: it compares git state, the log file and the remote before and after every step.

## Status

Early development. Working now:

| Command | What it does |
|---|---|
| `twokey init [--templates]` | Creates `local-only_twokey.toml` in the repository and checks that git ignores it. `--templates` copies the prompt templates for editing. |
| `twokey doctor` | Checks git, both agent CLIs and their options, the config, the repository, Docker and notifications. |
| `twokey run --fake-agents <folder>` | Runs the whole loop with scripted agents, in stepper mode or with `--autopilot`. |
| `twokey resume` | Continues a paused or stopped run. |
| `twokey render <step>` | Prints a step's prompt without running it, for tuning the templates. |
| `twokey unlock` | Removes a stale lock. |

Coming next: real Codex and Claude runs (`run` without `--fake-agents`, `doctor --smoke`, `status`, `report`), then autopilot polish and notifications. See [the milestones](docs/SPEC.md#16-milestones-and-releases).

## Install

You need [Go](https://go.dev/dl/) 1.27 or later.

```sh
go install github.com/Sten-Qy-Li/twokey/cmd/twokey@latest
```

Or build from a clone:

```sh
go build -o bin/twokey.exe ./cmd/twokey
```

twokey also needs `git`, Codex CLI and Claude Code on your PATH. Docker is optional.

## Quick start

In the project's repository:

```sh
twokey init      # writes local-only_twokey.toml
# edit local-only_twokey.toml: branch, requirements file, verify commands
twokey doctor    # fix every [FAIL] line, then run it again
```

Make sure git ignores the config file and the run folder. `twokey init` tells you the line to add to `.gitignore` if it doesn't.

## Safety

- Agents never push. twokey pushes only the configured branch, never with force, and only after its checks pass.
- If anyone else pushed to the branch during a round, twokey pauses. It never merges or rebases.
- Protected branches are checked on the remote after every step.
- Agents get their own scratch folders, Docker name prefixes and port ranges. twokey removes each step's Docker resources afterwards.
- Screen control and your real browser are off for the agents by default.

## Documentation

- [docs/SPEC.md](docs/SPEC.md): what twokey does, in detail.
- [docs/DECISIONS.md](docs/DECISIONS.md): why it works this way.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Built with [Claude Code](https://claude.com/claude-code).
