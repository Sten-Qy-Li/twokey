# Design decisions

Short entries: what was decided and why. Newest at the bottom. When a decision changes, add a new entry that replaces the old one (don't rewrite history).

## D1. Go instead of Python (2026-11-12)
The brief chose Python 3.11+. The owner asked whether another language would do the job as well or better, and chose Go. Speed was not the reason: twokey spends almost all its time waiting for agents. The reasons:
- One binary, with nothing else to install. On the development machine the only Python was the Microsoft Store build. A test showed that it redirects new folders made directly under `%LOCALAPPDATA%` into its private package folder, which is where staging lives (section 9). Folders under `%TEMP%` were not redirected.
- Windows job objects kill an agent and everything it started, even if twokey itself crashes.
- Parallel audits, timeouts and the heartbeat fit Go's goroutines and contexts.
- The owner already maintains a Go CLI (kube-onramp), so its CI, linting and pinning rules carry over.

Runtime dependencies are `golang.org/x/sys` and one TOML parser that can reject unknown keys. `github.com/BurntSushi/toml` was chosen over `pelletier/go-toml/v2` because it has no dependencies of its own; `MetaData.Undecoded()` lists unknown keys, and `IsDefined()` finds missing ones. The SPEC was translated: `tomllib` → BurntSushi/toml, `string.Template` → twokey's own expander (D13), pytest/ruff → `go test`/golangci-lint, `ctypes` → `x/sys/windows`, `taskkill` → job objects.

## D2. Pins and the 7-day rule (2026-11-12)
- Go: `go 1.27.0` with `toolchain go1.27.4`, the latest stable release on go.dev/dl (same as kube-onramp).
- `github.com/BurntSushi/toml` v1.6.0 (2025-12-18, the latest).
- `golang.org/x/sys` v0.49.0 (2026-10-06). v0.50.0 came out on 2026-11-06, so it was only 6 days old. It is added to `go.mod` when the first code uses it (job objects, keep-awake); until then `go mod tidy` leaves it out.
- CI tools: golangci-lint v2.15.0 and govulncheck v1.9.0 (2026-10-11). Actions are pinned to full commit SHAs, with the tag in a comment: `actions/checkout` v7.0.1, `actions/setup-go` v7.0.0, `golangci/golangci-lint-action` v9.3.0. These are the pins kube-onramp checked on 2026-11-11.
- Like kube-onramp: new versions of tools and modules must be at least 7 days old when pinned. A malicious release is usually found and pulled within hours.

## D3. Installed CLIs, checked on 2026-11-12
`codex-cli 0.165.0` (a native `codex.exe`), Claude Code `2.1.311` (a native `claude.exe`), git `2.53.0.windows.1`. `codex exec --help`, `codex exec resume --help` and `claude --help` were read; their text is kept in `internal/doctor/testdata/`, and tests check that every option twokey uses is there. Where they differ from the brief, the CLI wins (D4–D8). `doctor` checks the same options on the installed CLIs, plus the allowed values of `--sandbox`, `--permission-mode` and `--permission-prompts`.

## D4. Codex: the scratch folder is passed with `--add-dir`
`codex exec` has `--add-dir <DIR>` ("additional directories that should be writable"). It replaces `-c sandbox_workspace_write.writable_roots=[...]` for new sessions, so Windows paths don't have to be written as TOML inside an argument.

## D5. Codex: `exec resume` has no `--sandbox`, `-C` or `--add-dir`
On a resume, twokey passes `-c sandbox_mode="<mode>"` and `-c sandbox_workspace_write.writable_roots=[...]`, and starts the process in the repository folder. `doctor --smoke` checks that a resumed session keeps its sandbox.

## D6. Codex: `exec resume` reads stdin only with `-`
The help says: "Prompt to send after resuming the session. If `-` is used, read from stdin". The brief said a missing prompt is enough. twokey always passes `codex exec resume <id> -`.

## D7. Options twokey never passes
Codex: `--worktree`, `--ephemeral` (sessions must be resumable), `--dangerously-bypass-approvals-and-sandbox`; `--full-auto` no longer exists. Claude: `--bare` (it skips the subscription login and the project's settings), `--dangerously-skip-permissions`, `--worktree`, `--no-session-persistence`. The config refuses these in `extra_args`, and also refuses options twokey sets itself (such as `--model`, `--sandbox`, `--add-dir`), with a message that points to the matching setting.

## D8. Claude: twokey picks the session ID; `--add-dir=<folder>`
`--session-id <uuid>` lets twokey write the ID to `state.json` before the step starts, so a crash can't lose it. `--add-dir <directories...>` takes several values, so twokey writes `--add-dir=<folder>` to keep it from swallowing the next argument.

## D9. Codex `notify` is off for twokey's runs
The owner's Codex config runs a desktop-app helper after every turn (`notify = [...]`). twokey sends its own notifications, so it passes `-c notify=[]`. The owner's normal Codex use is unchanged. Approved by the owner on 2026-11-12.

## D10. Screen and browser control are off by default
Both agents may otherwise control the screen (computer use) or the owner's real browser, with the owner's logins. In an unattended step that could disturb the owner's work or act in their accounts. `[codex] desktop_tools` and `[claude] desktop_tools` default to `false`: twokey turns off Codex's computer-use and browser plugins with `-c` options, and passes `--no-chrome` to Claude and denies its computer-use tools. The exact arguments are confirmed with `doctor --smoke` (M2). Headless test browsers (for example Playwright with a throwaway profile in the scratch folder) stay allowed: earlier audits used them, and they never touch the real browser. Approved by the owner on 2026-11-12.

## D11. Extra read folders go to both agents
`[project] extra_read_dirs` lists folders the requirements refer to, such as lecture slides. Claude gets `--add-dir=<folder>` for each; without it, a read outside the repo needs approval, and nobody can approve. Codex's `--add-dir` grants *write* access, so it isn't used here; Codex's sandbox normally lets it read outside its workspace. The prompt lists the folders as read-only. `doctor --smoke` checks that each agent can read a file in each folder. Requested by the owner on 2026-11-12.

## D12. Docker cleanup by step prefix
A job object doesn't stop containers, because the Docker daemon runs them, not the agent. After every agent step, and after any timeout, kill or crash (found on resume), twokey removes the Compose projects, containers, networks and volumes whose names start with the step's prefix. The prefix is per step, not per agent (`tk-<run>-r<round>-<step>-<agent>-`, lowercase), so a cleanup can't touch a parallel step's resources. Docker's `name` filter matches anywhere in a name, so twokey lists names and checks the prefix itself. `doctor` checks that Docker is running, as a `[WARN]` only: not every project needs containers. Requested by the owner on 2026-11-12.

## D13. Template placeholders
Only `${name}` is a placeholder, and a missing name is an error. `$${` produces a literal `${`. A bare `$word` (such as `$HOME` or `$env:TEMP` in a shell snippet) and any `$` not followed by `{` stay as they are. Python's `string.Template` would have treated `$HOME` as a placeholder. Requested by the owner on 2026-11-12.

## D14. Log heading labels come from the model that ran
When the CLI reports which model ran, the heading is `<label_prefix> - <model name>`, with the name looked up in `[model_names]` (unknown IDs are used as they are). Otherwise the configured `label` is used. The report flags any difference between the two, so a label the owner forgot to update is easy to spot. Codex's label is `Codex - GPT-6.1-Sol`, because the owner's Codex config runs `gpt-6.1-sol`. Whether and where each CLI reports the model is checked in M2. Requested by the owner on 2026-11-12.

## D15. The log marker matches whole lines only
The owner's log header mentions the marker inside a sentence ("Insert each new entry directly under the `<!-- ... -->` line"). A substring search finds it twice; a whole-line search finds it once. twokey compares each line, without its line ending, to the marker.

## D16. Heading demotion skips fenced code blocks
Agents' log entries may contain shell snippets, where `# comment` is not a heading. Demoting it would change the snippet. Approved by the owner on 2026-11-12, like D17–D21.

## D17. Only whole-line copies of the marker are stripped
With D15, a marker mentioned inside a sentence can't confuse the next insert, so it is left alone.

## D18. Agent-facing schemas use a small set of keywords
The schemas sent to the CLIs use only `type`, `properties`, `required`, `additionalProperties`, `enum` and `items`, which both CLIs' structured-output modes accept. ID patterns, option counts and the coverage rule are checked by twokey's own validator. `doctor --smoke` can show whether Codex accepts more (for example `pattern`).

## D19. Which disputes go to Codex
A disputed merged finding goes to the dispute step if any ID in its `merged_from` came from Codex's audit, even if Claude raised it too.

## D20. `git ls-remote` after every agent step
The branch and the protected branches are checked on the remote after every agent step, not only at push time, so an agent that pushed by itself is caught at once.

## D21. Recurrence lineage
If X has `recurs_of` Y and Y has `recurs_of` Z, X's lineage is 2, and the gate pauses with `PAUSE_RECURRING`. A finding that recurs once (lineage 1) is fixed again.

## D22. M1 includes verify, push and basic answers
The brief listed verify commands, push with checks and the decisions/rulings UI under M3. The fake end-to-end scenarios in section 15 need them, and so does a real stepper round in v0.1. M1 builds them with plain text prompts; M3 adds notifications, autopilot and polish. `render` and `unlock` also move to M1, because they are small once templates and the lock exist. Approved by the owner on 2026-11-12.

## D23. Line endings in this repository
Git for Windows converts line endings by default (`core.autocrlf`). `.gitattributes` keeps LF everywhere and marks `**/testdata/**` as binary-safe (`-text`), so fixtures keep their exact bytes. Log-insertion tests depend on exact CRLF and BOM bytes. Tests also set `GIT_CONFIG_NOSYSTEM=1` and their own `GIT_CONFIG_GLOBAL`, so the developer's git config can't change the results.

## D24. The repository is public: no personal details
Committed files contain no personal names, emails, home-directory paths, course URLs, team names, requirement files or log contents. The SPEC says "the owner" where the brief used a name. The owner's GitHub handle appears only in the module path and the README. The brief itself stays local (`.gitignore`). Prompt templates also say "the owner", so they work for anyone.

## D25. twokey's own folders
Staging: `os.UserCacheDir()/twokey/staging` (`%LOCALAPPDATA%\twokey\staging` on Windows). Scratch: `os.TempDir()/twokey` (`%TEMP%\twokey`). `doctor` checks that both are writable by creating and deleting a probe file. Tests pass their own folders through options; nothing reads environment variables for this.

## D26. Config rules
- Required keys (no default): `[project] branch`, `[checkpoint] label`, `[checkpoint] requirements_file`, `[verify] commands`. A missing branch must not silently become a default, and `verify.commands` must be a conscious choice (an empty list is allowed; `doctor` warns).
- Unknown keys and sections are errors. Type errors and syntax errors are rewritten into plain sentences that name the key or the line.
- New keys beyond the brief: `[project] extra_read_dirs` (D11), `label_prefix` and `[model_names]` (D14), `desktop_tools` (D10), `[claude] max_budget_usd` (0 = off).
- `twokey init` writes the file with mode 0600 (it may hold an ntfy topic later), refuses to overwrite, and fills in `branch` and `protected_branches` from git when it can.
- A test keeps `assets/config.toml`, `config.Default()` and the copy in SPEC section 11 identical.

## D27. Data files live in `assets/` and are embedded by the root package
`go:embed` can't reach parent folders, so `assets.go` (package `twokey`, at the module root) embeds `assets/`. The same layout as kube-onramp's D6: contributors find the config template, and later the prompt templates and schemas, in one place.

## D28. Lint: `std-error-handling` instead of `_, _ =`
twokey prints to an `io.Writer` in many places. golangci-lint's `std-error-handling` exclusion covers unchecked `fmt.Fprint*` results, so the code doesn't need `_, _ =` on every line. gosec's G115, G204, G301, G304, G306, G702 and G703 are excluded for the same reasons as in kube-onramp: running the user's tools and writing the user's files is twokey's job. G115 flags integer conversions in Win32 calls (the PID is range-checked first); G702 and G703 are G204 and G304 found by taint analysis.

## D29. The ref comparison leaves out remote-tracking refs
An auditor may run `git fetch`, which moves `refs/remotes/*` but changes nothing the owner cares about. Counting it as a violation would pause runs for no reason. A push, which also moves a remote-tracking ref, is caught on the remote itself with `git ls-remote` (D20).

## D30. A teammate's push is not an agent's push
If the working branch moves on the remote during a step, twokey asks whether the new commit is in local history. If it is, an agent pushed (`PAUSE_INVARIANT`). If it isn't, someone else pushed: twokey warns, lets the step finish, and the push step pauses with `PAUSE_REMOTE_MOVED`. Protected branches stay strict: any change is `PAUSE_INVARIANT`.

## D31. Exit codes
0 = converged, or the owner stopped the run (q or Ctrl+C) to resume later; 1 = preflight failed or something broke; 2 = wrong command line; 3 = paused, waiting for the owner; 4 = stopped after the last round with findings left. Scripts can tell "done" from "needs you".

## D32. Two more pause reasons, and what resume does per reason
`PAUSE_PUSH_FAILED`: `git fetch` or `git push` failed (network, login). `PAUSE_INTERRUPTED`: a fix step was interrupted and left changes; twokey never discards the owner's tree. Resume first resolves the reason: failed agent steps and failed checks run again; `PAUSE_NO_PROGRESS` reruns the fix if HEAD hasn't moved; `PAUSE_REMOTE_MOVED` continues once the remote branch is part of local history; `PAUSE_INVARIANT` continues once the tree is clean, because the owner has looked at it.

## D33. state.json is checked against what twokey last wrote
twokey rewrites `state.json` during every step, so the run-folder manifest leaves it out. Instead, twokey keeps the hash of the file it last wrote and compares after each step. An agent that edits it still causes `PAUSE_INVARIANT`.

## D34. Decisions and rulings are asked in both modes
SPEC section 10 says a pause waits at the terminal prompt. So autopilot skips only the Enter presses between steps; a decision or ruling is still asked at the prompt. Without input (end of file, for example when stdin is not a terminal), the run pauses and `pending.md` says what is needed.

## D35. fix_p3 includes "noted" P3 items
The convergence template tells Claude to mark P3 items "noted", so "confirmed P3 repo_fix" (SPEC section 4, row 8) would never match. With `fix_p3 = true`, P3 repo_fix items that are not disputed or dropped are fixed.

## D36. Dispute answers use the merged IDs
The dispute prompt lists each disputed merged finding with the Codex IDs it came from, and asks Codex to answer per merged ID (for example `R1-M-02`). The rechecks must cover exactly the disputed merged IDs.

## D37. The fake runner's scenario format
Per round a folder `r<N>` holds `<step>.json` (the answer), `<step>.<attempt>.json` (the answer when the session is resumed), and `<step>.actions.json` (what the fake agent does first: `write`, `commit`, `append`, `push`, `teammate_push`, `no_answer`). `${commit}` and `${new_commits}` are filled in, with the same expander as the prompts. Scenarios live in test code, so each test shows its whole story.

## D38. Until M2, run and resume need --fake-agents
The real Codex and Claude runners come in M2. Without `--fake-agents`, `twokey run` says so and exits with code 2, so nobody starts a real run by accident.

## D39. The branch catches up with an upstream only by fast-forward (2026-11-12)
The owner's workflow: twokey works on the owner's branch. Teammates' merge requests go into another branch, the upstream, at any time. The owner merges the branch into the upstream through a merge request with a merge commit, never squash. The branch catches up with the upstream only by fast-forward: no merge commits, no rebase, no force push.
- New key `[project] upstream`, default `""` (off, as before). It may not equal `branch`; it may be a protected branch, because twokey only reads it.
- Each round starts with a sync step. Equal or ahead: nothing. Behind: `git merge --ff-only`, then a push with the normal push rules. Diverged: `PAUSE_DIVERGED`, telling the owner to merge the branch into the upstream with a merge commit (a squash merge makes the divergence permanent); `resume` runs the sync again.
- This is the one exception to "never merge or rebase automatically". A fast-forward can't lose work or rewrite history, and it is what the owner did by hand before each run.
- The step runs at the start of each round, not during one: the round's commit must stay fixed while the agents work on it.
- If the fast-forward's push fails, HEAD is already ahead of the remote branch, on the way to the upstream. The sync step accepts exactly that case on resume and pushes, so the owner doesn't have to push by hand.

## D40. Protected branches: judged by when they moved (2026-11-12)
D30 kept protected branches strict: any change was `PAUSE_INVARIANT`, which resumed only once the owner "undid what was wrong", and the push compared them with their preflight SHAs. With D39's workflow, a teammate's merge into the upstream during a run is expected and can't be undone, so the run got stuck for good. twokey can't tell a teammate's merge from an agent's push: the agents use the owner's git login. What it does know is when an agent was running.
- **During an agent step** (between the snapshots around it): only the owner can judge it. If that is the only problem, pause with `PAUSE_PROTECTED_MOVED`, showing the branch, the old and new SHA, and the new commits with subject and author. Together with other problems, the pause stays `PAUSE_INVARIANT` and lists everything. Either way, `resume` asks the owner to confirm; only `y` counts, and no input keeps the run paused, as with decisions (D34). Then the new SHA is the baseline.
- **Outside agent steps** (between steps or rounds, during verify, a push or a pause): no agent can have done it. twokey reads the protected branches before every agent step and after every push, notes any change in the terminal and the report, and takes it as the new baseline. So a teammate's merge between steps never pauses the run. The push no longer compares with the preflight SHAs.
- The working branch and other refs are checked exactly as before (D20, D29, D30).
- A heuristic could tell more cases apart (an agent can only push commits it has locally), but an agent can also push from a clone in its scratch folder, so twokey asks instead of guessing.
- The fake runner's `teammate_push` takes an optional `teammate_branch`, so tests can move a protected branch.

## D41. Resume publishes an answer that a check paused (2026-11-12)
Found while planning D40. When the check after convergence, dispute or fix paused the run, the step's status was already "done", but its answer wasn't published or logged. On resume, the loop skipped the step. After convergence, the gate then ran on an empty answer and could report "converged" by mistake. Now a step counts as done only once its answer is published and logged. On resume, a step that answered but wasn't published isn't run again: twokey publishes and logs its answer, repeats the fix's checks on the branch, and goes on. An audit or step whose log entry already exists isn't logged twice. A failed check's retry of the fix session publishes and logs its new answer in place of the first.

## D42. The agents learn how the commit stands to the upstream; the report says when to merge (2026-11-12)
- New placeholder `${upstream_status}`, used in the audit and convergence templates. The sync step (D39) records `<remote>/<upstream>` and how many commits the round's commit is ahead of it. The text says whether a deployment built from the upstream runs the audited commit. Both blind audits get the same text, so they judge checks against a live deployment the same way, instead of one calling a missing feature on the live site a bug and the other calling it "not deployed yet". Recording it in the round keeps the text the same for both audits, also after a resume, although the upstream may move during the round.
- After a converged run, if the branch has commits the upstream lacks, the report ends with "Next: merge `<branch>` into `<upstream>` (merge commit, not squash). The next run fast-forwards.", and the terminal shows it. Only after a converged run: after max rounds, findings are left and the report already says to consider another round.

## D43. How screen and browser control are turned off (2026-11-12, M2)
Checked against Codex's source at tag `rust-v0.165.0` and `codex mcp list --json` on the owner's machine. On the owner's machine, Codex's screen and browser control come from the bundled plugins `browser@openai-bundled`, `computer-use@openai-bundled` and `unified-computer-use@openai-bundled`, and from the MCP servers `node_repl` (in the Codex config) and `cua_repl` (from a plugin).
- Codex splits `-c` keys on every dot and keeps quote characters, so `-c 'plugins."browser@openai-bundled".enabled=false'` silently does nothing. The unquoted `-c plugins.browser@openai-bundled.enabled=false` works, because the IDs have no dots. Turning these plugins off also removes `cua_repl`.
- `-c mcp_servers.<name>.enabled=false` works for a server that exists, but makes Codex fail ("invalid transport") for one that doesn't. So twokey runs `codex mcp list --json` once per run, with the plugin overrides, and turns off only the listed desktop servers.
- Claude Code's computer use is a macOS research preview that isn't available with `-p`, so `--no-chrome` is all Claude needs. This replaces D10's plan to deny Claude's computer-use tools; `--disallowed-tools` isn't used.
- `doctor --smoke` runs with the same arguments, so it shows that both CLIs accept them.

## D44. Which model ran
Claude's result names the models in `modelUsage`; twokey takes the one with the highest cost, because Claude Code may use a small model for side tasks. Codex's `--json` events never name the model (the source has no such field), so a Codex heading uses `[codex] model` if set, the reroute target if Codex reports a reroute, and otherwise `[codex] label`. The report's notes flag every heading that differs from the configured label (D14).

## D45. Running a CLI: job objects, staging, counting tries
- On Windows, twokey starts the agent suspended, puts it in a job object that kills its processes on close, and only then resumes it, so even its first child stays in the job. The thread is resumed with documented APIs (a thread snapshot, `OpenThread`, `ResumeThread`), not `NtResumeProcess`. When the agent exits, what it left running is killed. Elsewhere, a process group.
- A Claude session ID is a random version-4 UUID chosen by twokey and saved in `state.json` before the step starts (D8). A fresh try gets a new one, because Claude refuses an ID it already has.
- All raw output (events, stdout, stderr, Codex's `-o` file and its schema file) goes to staging first, so a parallel auditor can't read the other's work in the run folder. It is published with the answer.
- Tries are counted across resumes (`attempts` in `state.json` is no longer reset), so every try's files have their own names. The fake runner's `<step>.<attempt>.json` uses the same count.
- Per step, `state.json` keeps tokens and Claude's cost estimate; a resumed Claude session replaces its own earlier numbers, because Claude reports the session's whole total.

## D46. Usage limits
The wording comes from the sources: Codex says "You’ve hit your usage limit. … Try again at 3:45 PM." (or a date, or "later"), Claude says "You've hit your session limit · resets 3:45pm" (or weekly, or a weekday). The default patterns add `hit your`, since Claude's message doesn't contain "usage limit". Patterns are checked only when a step failed. A step waits at most 3 times. A fix that changed the tree or committed before its limit pauses instead of starting again.

## D47. Timeouts
An audit, convergence or dispute step that runs out of time gets one fresh try; a second timeout pauses with `PAUSE_TIMEOUT` (SPEC section 10 named the reason only for the fix). The fix pauses at once. For a fix that was cut short by a timeout or a limit, the check after the step accepts uncommitted changes, so the pause names the real cause and shows `git status`; pushes, protected branches, the log and twokey's files are still checked.

## D48. doctor --smoke
Each agent gets one small task in a fresh session: write a file in its scratch folder, make one HTTP request (Codex only, when `[codex] network` is on) and read the first 4 bytes of one file in each extra read folder; then a resumed call writes a second file. twokey checks the files on disk and compares the bytes itself, and checks that `git status` is unchanged. It uses the real runner with the real arguments, so it also shows that the schema options and the screen-and-browser-off options are accepted. The smoke test needs the config and both CLIs; other `[FAIL]` lines (such as a dirty tree) don't block it.

## D49. Real runs are on
D38 is replaced: `twokey run` and `resume` call the real CLIs unless `--fake-agents` is given. The owner's go-ahead is still needed before the first real run (CLAUDE.md). The CLI tests run only in folders that aren't repositories, so they can't start a real run.
