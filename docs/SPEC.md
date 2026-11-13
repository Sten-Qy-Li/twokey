# twokey specification

This is what twokey must do. It started as the build brief (sections 1–17) and is adapted for Go. `docs/DECISIONS.md` records why things are built this way, and every change to this file. "The owner" is the person who runs twokey on their project.

If an installed CLI disagrees with this file, the CLI wins: record the difference in `DECISIONS.md` and update this file.

---

## 1. What twokey is

`twokey` is a small command-line tool that runs a cross-checked audit-and-fix loop between two coding agents on one repository, until the repository is ready for a given milestone (for the owner: a university course checkpoint).

Today the owner runs this loop by hand. Codex CLI audits the repository against the "sources of truth" (the instructors' requirements) and writes a log entry. Claude Code audits it too and writes a log entry. Claude Code then checks Codex's claims and merges the two lists in a convergence step, fixes what's needed, commits and pushes. Then the loop starts again. On one day this took 21 handoffs, each started by a nearly identical prompt. twokey takes over the handoffs. The owner provides parameters once, and twokey fills them into prompt templates, runs each step, waits, decides the next step, and stops or asks them when it should.

The name comes from the two-person rule: the run only ends when both keys turn, which means both auditors clear the same commit.

**What makes it trustworthy:** the control flow is ordinary code, not a prompt that promises to loop. AI agents do the auditing and fixing. The script decides what happens next, and it checks what the agents did instead of trusting their reports.

---

## 2. Decisions already made

| # | Decision |
|---|---|
| D1 | **Blind, parallel audits.** Codex and Claude audit the same commit at the same time. Neither sees the other's current-round work until the convergence step. |
| D2 | **twokey is the only writer of the log file.** Agents never edit it. Their log entries come back inside their JSON answers. twokey inserts them one at a time, holding a lock and using an atomic replace (section 7). This rules out two agents writing to the file at nearly the same moment. |
| D3 | **Stop rule.** Stop when, for the same commit, no confirmed P1 or P2 finding of kind `repo_fix` or `needs_decision` remains (section 4). The run is capped at 4 audit rounds by default. |
| D4 | **Autopilot pushes fixes automatically** to the project's working branch, with no Enter press. twokey does the push itself after its checks pass (section 8). Agents never push, and their git can't (D62). |
| D5 | **Roles are fixed in v0.x.** Codex: blind audit, dispute re-check. Claude: blind audit, convergence, fix. Keep a small runner interface, so another agent could be added later. |
| D6 | **Two modes.** `stepper` (v0.1): shows each step's result and waits for Enter before the next step. `autopilot` (v0.2): runs unattended. It never waits at a prompt: it answers decisions and rulings itself (configurable, D61), and stops only for the safety reasons in section 10. In either mode, `twokey round` runs one round and stops after its push (DECISIONS.md D58). |
| D7 | **Go, one binary** (`go 1.27.0`, toolchain pinned in `go.mod`, `CGO_ENABLED=0`). Runtime dependencies: `golang.org/x/sys` and one TOML parser that rejects unknown keys (`github.com/BurntSushi/toml`), both pinned; nothing else without the owner's OK. CI runs `govulncheck`. Windows-first. Avoid Windows-only assumptions where it's cheap, but macOS/Linux are untested in v0.x. (Was Python; see DECISIONS.md D1.) |
| D8 | **The audits share the main checkout, read-only.** They don't use git worktrees. In the owner's projects, the requirement files (`local-only_*`), `node_modules` and `.env` are git-ignored, so a worktree wouldn't contain them. Isolation comes from separate scratch folders, separate container prefixes and port ranges, and twokey's checks afterwards. |
| D9 | **Each step is a fresh agent session.** Convergence doesn't continue the Claude audit session, and fix doesn't continue the convergence session. Sessions are resumed only to correct an invalid answer or a failed check (section 9). |
| D10 | **P3 findings aren't fixed by default** (`fix_p3 = false`). They're listed in the final report. |

---

## 3. The loop

```
preflight
  │
  ▼
┌──────────────────────────── round N (commit C) ────────────────────────────┐
│ upstream (script): compare the branch with the upstream, if one is set     │
│ audits: Codex audit  ║  Claude audit      (parallel, blind, no repo edits) │
│   → both results held in staging, then published together                  │
│ convergence (Claude): verify, merge, confirm or dispute, look for more     │
│ dispute (Codex), only if Codex-raised P1/P2 findings were disputed         │
│ gate (script): STOP / PAUSE / FIX                                          │
│ fix (Claude): edit + commit, no push                                       │
│ verify (script): invariants + project checks                               │
│ push (script): push branch, confirm remote                                 │
└────────────────────────────────────────────────────────────────────────────┘
  │ new commit C' → round N+1
  ▼
final report (+ a twokey entry in the log)
```

- **Round** = one pair of blind audits of one commit, plus what follows. The round counter goes up each time a new pair of audits starts.
- After each agent step, twokey checks the invariants (section 8) **before** it publishes the result or moves on. If that check pauses the run, `resume` publishes and logs the step's answer without running the agent again (D41).
- In `stepper` mode, every arrow above waits for Enter. In `autopilot`, the only stops are the pauses in section 10.

**One round** (`twokey round`, D58). One round of the loop uses many tokens, so the owner can run one at a time: the blind audits, convergence, the dispute if needed, the fix, the checks and the push, then twokey stops. No next round starts.
   - The next `twokey round` continues the same run as round N+1, so its audits check the last fix, and the history and finding IDs carry on. A round that stopped part way (for example `q` before the fix, or a pause) is finished from where it stopped, without new audits; so is the last round of a run that stopped at `max_rounds`.
   - A new run starts only when the latest run converged. If HEAD and `<remote>/<branch>` are still the commit it converged on, there is nothing new to audit, and nothing runs.
   - `max_rounds` doesn't stop a single round: the gate's row 7 is off, so the round always gets its fix. When a round goes past `max_rounds`, the run's limit becomes that round's number, so the prompts' "round N of at most M" stays true.
   - Between rounds, time passes, so the next round needs the same checks as a new run (preflight, section 8) before it starts on HEAD.
   - The run's status is then `round_done`, `report.md` says that the fix is pushed but hasn't been audited yet, and the exit code is 5.

**Upstream** (D39, D57). The owner's branch may be compared with another branch, `[project] upstream` (for example `main`, where teammates' merge requests land). twokey never moves the branch to it: bringing the upstream's commits in is the owner's call. With `upstream = ""` (the default) this step does nothing. Otherwise, at the start of each round, after `git fetch`, twokey compares the round's commit with `<remote>/<upstream>` (`git rev-list --left-right --count`; the decision is a pure function in `internal/upstream`):
   - **Equal, or the branch is ahead:** an `[OK]` line.
   - **The branch is behind** (an ancestor of the upstream), or **diverged:** a `[WARN]` that says how many commits each side lacks, and what the owner can do: `git merge --ff-only <remote>/<upstream>` and `git push` when behind; when diverged, first merge the branch into the upstream through a merge request with a merge commit (not squash, which keeps them diverged). The round goes on and audits the branch as it is.
   - Either way, the audits get the comparison in their prompts (`${upstream_status}`, D42). `doctor` shows the same warning.

---

## 4. The gate (a pure function, fully unit-tested)

Inputs: the round number, `max_rounds`, both blind audit results, the convergence result, the dispute result (if any), the owner's decisions and rulings, how often each finding has recurred, and whether the invariants passed.

**Definitions**
- *Blocking* finding: severity P1 or P2 **and** kind `repo_fix` or `needs_decision`.
- A blocking finding from a blind audit counts as **resolved** if:
  - (a) convergence disputed it and the original auditor withdrew it in the dispute step; or
  - (b) only Claude's blind audit raised it and convergence (also Claude) disputed it, which counts as Claude withdrawing its own finding; or
  - (c) the owner ruled "drop".
- *Confirmed blocking list* = the blocking findings in the convergence result with `status = confirmed`, plus disputed ones the owner ruled "fix", minus any the owner dropped.
- *Skipped* (autopilot only, D61): findings autopilot left for the owner this round, because they need an answer it can't give (a decision without a recommended option, a finding that keeps coming back). They aren't fixed, and they keep the round from converging.

**Order of checks (the first match wins)**

| # | Condition | Result |
|---|---|---|
| 1 | An invariant failed in this round | `PAUSE_INVARIANT` |
| 2 | Convergence disputed a blocking finding that Codex raised, and there's no dispute result yet | `RUN_DISPUTE` |
| 3 | The dispute step "maintained" any finding and the owner hasn't ruled yet | `PAUSE_ADJUDICATE` (show both sides) |
| 4 | A confirmed `needs_decision` blocking finding that isn't skipped has no decision from the owner | `PAUSE_DECISION` (autopilot answers it, D61) |
| 5 | A confirmed blocking finding that isn't skipped has recurred for the second time (`recurs_of` lineage ≥ 2) | `PAUSE_RECURRING` (autopilot skips it, D61) |
| 6 | The confirmed blocking list is empty | `STOP_CONVERGED` |
| 6a | Every finding on the confirmed blocking list is skipped | `PAUSE_SKIPPED`: nothing can be fixed until the owner answers (D61) |
| 7 | `round >= max_rounds`, except in `twokey round` (D58) | `STOP_MAX_ROUNDS` (no fix after the last audit round; `resume --more-rounds N` continues) |
| 8 | Otherwise | `FIX(list)`: the confirmed blocking list without the skipped findings, plus confirmed P3 `repo_fix` items if `fix_p3 = true` |

Notes:
- P1/P2 `external_evidence` findings never block. They go into the final report as a to-do list for the owner and the team.
- The `verdict` field in agent answers is for information only. If it says "ready" but blocking findings exist, the findings win. Note the mismatch in the report.
- After a pause is resolved, run the gate again with the new input.

---

## 5. Agent steps

| Step | Agent | Session | May change the repo? | Input (paths in the prompt) | Output schema |
|---|---|---|---|---|---|
| `audit` | Codex and Claude, in parallel | fresh | No | requirements file, earlier rounds' published results, decisions | `audit.schema.json` |
| `convergence` | Claude | fresh | No | both audit results, earlier merged lists, decisions | `convergence.schema.json` |
| `dispute` | Codex | fresh | No | the disputed findings with convergence's reasons | `dispute.schema.json` |
| `fix` | Claude | fresh | Yes: edits and commits on the branch. **No push.** | the convergence result, the list to fix, decisions | `fix.schema.json` |

Every prompt is the shared preamble plus a step template (section 14), rendered by twokey's own placeholder expander (rules in section 14). A missing placeholder is an error, not an empty string. The rendered prompt is saved in the run folder before the step runs.

---

## 6. JSON schemas

Codex's `--output-schema` needs strict schemas, so for both agents:
- every object has `"additionalProperties": false`;
- every property is listed in `required`;
- "not applicable" is written as `""` or `[]`, never as a missing field.

Use the same schema files for both agents. Validate every answer in twokey too, with its own small validator for required keys, types, enums and ID patterns. Don't trust the CLIs alone. The schemas sent to the agents use only keywords both CLIs are sure to accept (`type`, `properties`, `required`, `additionalProperties`, `enum`, `items`); ID patterns and option counts are checked by twokey's validator (DECISIONS.md D18).

**Finding (used in audit results):**
```json
{
  "id": "R2-CX-03",
  "title": "short title",
  "severity": "P1 | P2 | P3",
  "kind": "repo_fix | needs_decision | external_evidence",
  "evidence": "file:line references, commands run and their results",
  "fix_hint": "what a fix should do",
  "options": ["only for needs_decision: 2-4 options, else []"],
  "recommended_option": "one of options, or \"\""
}
```
ID format: `R{round}-CX-{nn}` (Codex), `R{round}-CC-{nn}` (Claude), `R{round}-M-{nn}` (merged/convergence). twokey rejects duplicate or malformed IDs.

**audit.schema.json:** `{ audited_commit (40-hex), verdict ("not_ready" | "ready_for_human_review"), summary, findings: [Finding], log_entry_markdown }`

**convergence.schema.json:** `{ audited_commit, verdict, summary, findings: [MergedFinding], log_entry_markdown }`. A MergedFinding is a Finding plus:
- `sources`: subset of `["codex", "claude", "convergence"]`
- `merged_from`: original IDs
- `status`: `"confirmed" | "disputed" | "noted"`. Use `noted` for P3 and `external_evidence` items.
- `dispute_reason`: required when disputed, else `""`
- `recurs_of`: ID of a finding from an earlier round that this repeats, else `""`

Rule for convergence: every blocking finding from either blind audit must appear in `merged_from` of exactly one merged finding. That merged finding must be `confirmed` (still P1/P2) or `disputed`. Lowering it below P2 counts as disputed. twokey checks this coverage.

**dispute.schema.json:** `{ rechecks: [{ id, decision ("withdraw" | "maintain"), evidence }], log_entry_markdown }`

**fix.schema.json:** `{ base_commit, commits: [sha], done: [{ id, change, verified_how }], not_done: [{ id, reason }], log_entry_markdown }`

twokey also checks the facts behind the answer:
- `audited_commit` and `base_commit` equal the commit it gave the agent;
- every SHA in `commits` exists, descends from `base_commit`, and is on the branch.

---

## 7. The log file (single writer)

Project setting `log_file` (for example `local-only_logs.md`). Format rules come from the file's own header and current entries. In short:

- Newest entry at the top, inserted directly under the marker line (configurable). The marker matches only a **whole line** that equals it exactly; a header that mentions the marker inside a sentence doesn't count (DECISIONS.md D15).
- Layout: marker line, blank line, `### <label> — <UTC>`, blank line, body, blank line, then the previous entry's `### ` line.
- Keep the file's encoding and line endings (a typical log is UTF-8 without BOM, with LF). Detect and preserve; never convert. If the file mixes LF and CRLF, new lines use the marker line's ending.

**twokey's insert procedure:**
1. Hold the run lock (section 13). Only one twokey process per repository.
2. **Re-read the file now** (the owner may have edited it). Find the marker. If the marker is missing or appears more than once, stop: `PAUSE_INVARIANT`.
3. Build the entry:
   - heading `### {label} — {completion UTC as YYYY-MM-DDTHH:MM:SSZ}`. `label` is `{label_prefix} - {model name}` when the CLI reports which model ran: the model ID is looked up in `[model_names]` (for example `gpt-6.1-sol` → `GPT-6.1-Sol`), and an unknown ID is used as it is. When the CLI doesn't report the model, `label` is the agent's `label` from config (e.g. `Codex - GPT-6.1-Sol`). If the label from the reported model differs from the configured `label`, the report flags the mismatch (DECISIONS.md D14);
   - first body line `- **twokey:** run \`{run_id}\`, round {n}, step \`{step}\`.`;
   - then the agent's `log_entry_markdown`. Demote any line starting with `# `, `## ` or `### ` to `#### `, except inside fenced code blocks, where a `# comment` must survive (D16). Remove every line that is exactly the marker (D17).
4. Save a backup of the current file in the run folder. Keep the last 3.
5. Write the new content to a temp file in the same folder, sync it to disk, then rename it over the log (`os.Rename`, which replaces the file in one step on Windows too). On Windows, an access-denied or sharing-violation error (file open in another program) gets 5 retries with backoff, then `PAUSE_INVARIANT`.
6. Record the new file's SHA-256 as the expected hash.

**After parallel audits:** publish both results first, then insert the two entries **one after the other**, ordered by completion time, so the newer one ends up on top. Pass both final headings to later prompts, so agents can refer to entries by heading as they do today.

**twokey's own entries** (label `twokey v{version}`): the owner's decisions and rulings when they give them, and the final run summary.

**Agents never edit the log.** twokey records the file's hash before each agent step and checks it afterwards. A mismatch means `PAUSE_INVARIANT`. The message says "the log changed during step X. If you edited it yourself, run `twokey resume`."

---

## 8. Invariants (check, don't trust)

Take a snapshot before each step and compare after it. Use plain `git` commands through `os/exec`, with `core.quotepath=off` so paths come back as UTF-8, and `GIT_TERMINAL_PROMPT=0` so git never waits for a password.

**After every agent step** (any kind, including after a timeout, a kill or a crash found on resume):
- Remove the Docker resources with that step's prefix (section 9, "Scratch, containers and ports"). This runs before the other checks.
- `git ls-remote` for the branch and the protected branches must match the snapshot, so an agent that pushed by itself is caught at once (`PAUSE_INVARIANT`; D20). If the working branch moved on the remote to a commit that isn't in local history, someone else pushed: twokey warns and leaves it to the push step (`PAUSE_REMOTE_MOVED`; D30).
- A protected branch that moved during the step is someone else's change, for example a teammate's merge, because agents can't push (D62, which replaces D40's pause). twokey notes it, adds it to the report, takes the new SHA as the baseline and goes on. If the step also broke a rule (for example it changed git's push settings), the move is listed with the other problems under `PAUSE_INVARIANT`, with the new commits (subject and author, up to 20), or a note that the history was rewritten.
- git's push settings (any `remote.<name>.pushurl` or `url.<base>.pushInsteadOf`) are unchanged (D62).
- `state.json` must still be what twokey last wrote (D33).

**Protected branches outside agent steps** (D40). Before each agent step and after each push, twokey reads the protected branches again. A change since the last check happened while no agent was running (between steps or rounds, during the checks, a push or a pause), so it can't be an agent's push. twokey prints a note, adds it to the report, and takes the new SHA as the baseline.

**Agents can't push** (D62). Every agent process gets `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` and `GIT_CONFIG_VALUE_<n>` with `url.twokey-agents-may-not-push::.pushInsteadOf = ""` (after any entries the owner's environment already passes that way). Every push URL starts with the empty string, so git sends every push, by remote name or by URL, to the remote helper `twokey-agents-may-not-push`, which doesn't exist: the push fails at once, without a network call. Fetch and `ls-remote` are unchanged. A remote's own `pushurl` isn't rewritten by `pushInsteadOf`, and a longer `pushInsteadOf` wins, so twokey refuses to run agents while either is set (preflight, before each agent step, `doctor`), and one that appears during a step is a violation. twokey's own git commands, including its push, don't get the block. `doctor --smoke` checks that each agent's commands see it. The block doesn't stop an agent that deliberately overrides git's config on its own command line or reaches the remote with another tool; the `ls-remote` checks still catch a push to the working branch.

**Preflight (before round 1)**
- The repo is on `branch`; `git status --porcelain` is empty.
- No git setting gets a push past the agents' block: no `remote.<name>.pushurl` and no `url.<base>.pushInsteadOf` in any config file (D62).
- After `git fetch`, local `HEAD` equals `<remote>/<branch>`, and `<remote>/<upstream>` exists when `upstream` is set.
- The requirements file and log marker exist; `codex` and `claude` are found.
- No other twokey lock exists.
- Snapshot the remote SHAs of the `protected_branches`: the first baseline.

**After `audit`, `convergence` and `dispute` steps (no repo changes allowed)**
- `HEAD` and the current branch are unchanged.
- `git status --porcelain` is identical to the snapshot. Ignored files may change.
- The `git for-each-ref` output is identical, so there are no new branches, tags or stash entries. Remote-tracking refs are left out: an agent's `git fetch` is harmless, and pushes are caught on the remote (D29).
- The log file hash is unchanged.
- twokey's own files in the run folder are unchanged (keep a manifest of hashes).

**After `fix`**
- The branch is unchanged, and the new `HEAD` descends from the round's commit, with at least one new commit.
- `git status --porcelain` is empty, and the log hash is unchanged.
- Refs other than `refs/heads/<branch>` are unchanged.
- Then run the project's `verify.commands` in order (e.g. lint, format check, typecheck, tests, build) with a timeout, saving output to `verify.log`. Run each one through the system shell (`cmd /C` on Windows, `sh -c` elsewhere) so `npm.cmd` works; they come from the owner's config, so they're trusted. If one fails, resume the fix session **once** with the failure output ("fix this, commit, don't push") and verify again. A second failure means `PAUSE_VERIFY_FAILED`.
- If `commits` is empty (the agent changed nothing), pause with `PAUSE_NO_PROGRESS`.

**Push (autopilot or stepper)**
- Run `git fetch` again. If `<remote>/<branch>` moved since the round started (someone else pushed), pause with `PAUSE_REMOTE_MOVED`. Never merge or rebase (D57).
- Then run `git push <remote> HEAD:refs/heads/<branch>`: no force, no push options, never another branch.
- Afterwards, `git ls-remote <remote> refs/heads/<branch>` must equal local `HEAD`. A protected branch that moved since the last check is noted and becomes the baseline (D40).

---

## 9. Running the CLIs

**General**
- Find the executables with `exec.LookPath`. On Windows, a CLI may be a `.cmd` shim (on the development machine both are real `.exe` files).
- **Always send the prompt through stdin**, never as an argument. This avoids Windows command-line length limits and the quoting problems of `.cmd` shims. (Claude caps piped stdin at 10 MB, which is far more than any prompt.)
- Write stdout and stderr straight to files, not pipes, so two parallel processes can't block each other. All text is UTF-8. The files go to the staging folder first (see "Blindness"), named `r<round>-<step>.<try>.<kind>`, and are copied into the run folder with the answer: `r1/codex-audit.1.events.jsonl`, `r1/claude-audit.2.stdout.json`, `*.stderr.log`. Tries are counted across resumes, so no try overwrites another's files (D45).
- Start each agent in its own Windows **job object** that kills its processes when the job closes. Killing the job ends the agent and every process it started, and the children die even if twokey itself crashes. twokey starts the agent suspended and lets it run only once it is in the job, so not even its first child can escape. When the agent exits, anything it left running is killed too. On other systems, use a process group. A job object doesn't stop Docker containers, because the Docker daemon runs them; see "Scratch, containers and ports".
- Print a heartbeat line every 60 s while steps run, e.g. `[..] round 2: codex-audit 14 min, claude-audit 11 min`.
- **Which model ran** (D44): Claude reports it as the keys of `modelUsage`; twokey takes the one with the highest cost. Codex's `--json` events never name the model, so a Codex log heading uses `[codex] model` when it is set, the model Codex reroutes to when it says so, and otherwise `[codex] label`. The report flags a heading that differs from the configured label.
- While a run is active, ask Windows not to sleep (`SetThreadExecutionState` with `ES_SYSTEM_REQUIRED`, from one goroutine locked to its thread, D64), and release that at the end. This doesn't keep the display on, and it can't stop a sleep the owner asks for, such as closing a laptop's lid.
- Each agent's environment carries the push block (section 8, D62).

**Screen and browser control** are off by default for both agents (`desktop_tools = false`): no computer use, and no control of the owner's real browser with their logins. Agents may still run headless test browsers, such as Playwright with a throwaway profile in their scratch folder. `desktop_tools = true` turns screen and browser control back on for that agent. The exact arguments are checked with `doctor --smoke` (D10, D43):
- Codex: `-c plugins.<id>.enabled=false` for the bundled plugins `browser@openai-bundled`, `computer-use@openai-bundled` and `unified-computer-use@openai-bundled`, plus `-c mcp_servers.<name>.enabled=false` for the MCP servers `node_repl` and `cua_repl` when `codex mcp list --json` shows them enabled. Codex splits `-c` keys on dots and keeps quotes, so the plugin IDs are written unquoted. An override for an MCP server that isn't configured makes Codex fail ("invalid transport"), which is why twokey asks first, once per run.
- Claude: `--no-chrome`. Claude Code's computer use is a macOS preview that isn't available with `-p`, so there is nothing else to turn off.

**Extra read folders** (`[project] extra_read_dirs`, for example a slides folder the requirements refer to) are given to both agents. Claude gets `--add-dir=<folder>` for each. Codex's `--add-dir` makes a folder *writable*, so twokey doesn't use it for these; Codex's sandbox can normally read outside its workspace. The prompt lists the folders as read-only. `doctor --smoke` checks that each agent can read a file in each folder (D11).

**Codex (`codex exec`)**, from the docs on 12 Nov 2026, checked against the installed codex-cli 0.165.0 (D3–D7):
- Runs non-interactively. The sandbox is **read-only by default**. `--sandbox workspace-write` allows writes; `--sandbox danger-full-access` removes the sandbox. **`--full-auto` is no longer supported**; don't use it.
- `workspace-write` blocks the network unless `-c sandbox_workspace_write.network_access=true`. Extra writable folders (the scratch folder) are passed with `--add-dir <folder>` (D4).
- **Known Windows problem:** openai/codex issue #34958 reports that on native Windows, `codex exec --sandbox workspace-write` can reject writes inside the working directory. `doctor --smoke` must test the configured sandbox for real: a write to the scratch folder, plus one network request.
- `--json` makes stdout a JSONL event stream (`thread.started` with `thread_id`, `turn.started`, `turn.completed` with usage, `turn.failed`, `item.*`, `error`). `--output-schema <file>` makes the final answer match the schema. `-o <file>` writes the final message to a file.
- twokey passes `-c notify=[]`, so the owner's Codex `notify` program doesn't run after every turn of an unattended step (D9).
- `[codex] web_search` (`""` = Codex's default, which is cached results) is passed as `-c web_search="<mode>"`. Codex's web search runs on OpenAI's side, so the local sandbox's network limits don't affect it (D52).
- **Sandbox check** (D51): commands that Codex audits use may fail under the sandbox's restricted token on Windows. At the start of a run, and in `doctor`, twokey runs `git ls-remote <remote> refs/heads/<branch>` and `docker version` with `codex sandbox -c sandbox_mode=... -- <command>`, started in the repository folder (no model call; `codex sandbox` accepts `-C` only with `--permission-profile`), each first outside the sandbox. What fails only inside is saved in `state.json`, and Codex's prompts name it (`${sandbox_limits}`) and say to record those checks as not verified instead of retrying.
- Resume: `codex exec resume <SESSION_ID> -`. The prompt is read from stdin only with the explicit `-` (D6). `resume` has no `--sandbox`, `-C` or `--add-dir`: twokey passes the sandbox as `-c sandbox_mode="<mode>"`, the writable folders as `-c sandbox_workspace_write.writable_roots=[...]`, and starts the process in the repo folder (D5). `doctor --smoke` checks that a resumed session keeps its sandbox.
- twokey never passes `--worktree`, `--ephemeral` (sessions must be resumable) or `--dangerously-bypass-approvals-and-sandbox`, and the config refuses them in `extra_args` (D7).
- Exit codes aren't documented. Treat a step as successful only if there was no `turn.failed`/`error` event **and** the `-o` file holds JSON that passes validation. (The source at tag `rust-v0.165.0` exits with 1 after a failed turn, and writes the `-o` file only after a completed one. `error` events also report retries, so they alone don't fail a step.)
- From the events twokey also reads the thread ID (`thread.started`), token usage (`turn.completed`), commands the sandbox refused (`command_execution` items with status `declined`, for the report), and a model reroute.
- Shape: `codex exec --json --output-schema <schema> -o <staging>/<step>.json --sandbox <mode> --add-dir <scratch> -c notify=[] [-c ...] [-m <model>] -C <repo> -`

**Claude Code (`claude -p`)**, from the docs on 12 Nov 2026, checked against the installed Claude Code 2.1.311 (D3, D8):
- `--output-format json` returns one JSON object with `subtype`, `is_error`, `result`, `session_id`, `total_cost_usd` (an estimate), `usage`, `modelUsage` (per model), `permission_denials` and, with `--json-schema '<schema>'`, a `structured_output` field. **Check that `structured_output` exists before using it.** It's missing if the run ended without valid output (`subtype` `error_max_structured_output_retries`), and twokey also treats `is_error: true` as no answer. A resumed session reports the whole session's totals, so twokey replaces that session's earlier numbers instead of adding to them.
- Exit code 0 means success. A failure inside the run is printed as the result on stdout.
- **Don't use `--bare`.** It's recommended for scripts, but it never reads the subscription login. Without it, `-p` loads the project's `.claude/settings*.json` permission rules, hooks and `CLAUDE.md`, which is what the owner wants.
- Permissions: since v2.1.302, a `-p` run starts in Manual mode, so anything needing approval is denied. Pass the configured `--permission-mode` (default `auto`), plus `--permission-prompts none` (v2.1.259+), which tells Claude nobody can approve and removes `AskUserQuestion`. Denials are listed in `permission_denials`. Copy them into the report, so the owner can add allow rules.
- `--add-dir` gives access to the scratch folder and the extra read folders. It takes several values, so twokey writes `--add-dir=<folder>` to stop it from swallowing the next argument. `--resume <session_id>` continues a session. `--max-budget-usd` exists (`[claude] max_budget_usd`, 0 = off).
- twokey picks the session ID itself and passes `--session-id <uuid>`, so `state.json` holds the ID before the step starts, and a crash can't lose it (D8).
- `--no-chrome` keeps Claude away from the owner's real Chrome unless `desktop_tools = true` (D10).
- twokey never passes `--bare`, `--dangerously-skip-permissions`, `--worktree` or `--no-session-persistence`, and the config refuses them in `extra_args` (D7).
- A `-p` run waits up to 10 minutes for background work the agent started. Templates tell agents to stop their background processes before answering.
- Shape: `claude -p --output-format json --json-schema <schema-json> --permission-mode <mode> --permission-prompts none --session-id <uuid> --add-dir=<scratch> [--add-dir=<read folder>...] --no-chrome [--model <m>]`, run from the repo folder, prompt on stdin. `--json-schema` takes inline JSON only, so twokey passes the minified schema; `doctor --smoke` checks that it survives Windows quoting.

**Invalid answer** (no or invalid JSON, wrong commit, broken coverage rule): resume the same session **once** with a short message listing the validation errors. A second failure means `PAUSE_AGENT_FAILED`. twokey shows the errors in a `[WARN]` and keeps them in the step's notes, so the report lists them (D54).

**Tokens and cost** (D53): per step, `state.json` keeps tokens (input counts cached tokens) and Claude's cost estimate, summed over the step's tries. Claude's `usage` covers one call, while `modelUsage` and `total_cost_usd` cover the session; on a resume, Claude usually restores the session's earlier totals and adds the new call. twokey uses the session totals and counts only what a resumed call added.

**Blindness:**
- Codex's `-o` file and Claude's stdout go to a **staging folder outside the repository**, at `%LOCALAPPDATA%\twokey\staging\<run-id>\` (Go's `os.UserCacheDir()`; `~/.cache/twokey/staging` on Linux).
- Copy results into the run folder only after **both** audits have finished and passed their checks.
- If one audit fails and is retried, the other's result stays in staging until the retry finishes.
- Prompts never mention the staging path.

**Scratch, containers and ports:**
- Each agent step gets its own scratch folder outside the repo: `%TEMP%\twokey\<run-id>\r<round>\<step>-<agent>\`.
- Each agent step gets its own Docker prefix, `tk-<run>-r<round>-<step>-<agent>-`, all lowercase, where `<run>` is the run's timestamp (for example `tk-20261114t0930z-r2-audit-cx-`). Compose project names allow only lowercase letters, digits, `-` and `_`, so the prefix uses only those. Each agent also gets its own port range from config (defaults: Codex `56000-56499`, Claude `56500-56999`).
- **Docker cleanup** (D12): after every agent step, and after any timeout, kill or crash, twokey removes the step's Docker resources: Compose projects (`docker compose -p <name> down --volumes --remove-orphans`), then containers with their unnamed volumes (`docker rm --force --volumes`, D55), networks and volumes whose names start with the step's prefix. Docker's name filters match anywhere in a name, so twokey lists the names and compares the prefix itself. It never touches a name without the prefix. When a run resumes after a crash, the step that was running gets the same cleanup. If Docker isn't running, the cleanup is skipped with a `[WARN]`.
- Delete scratch folders older than 7 days at the start of a run.

---

## 10. Pauses, notifications, limits and resume

**Pause reasons:**
- `PAUSE_DECISION`, `PAUSE_ADJUDICATE`, `PAUSE_RECURRING`
- `PAUSE_INVARIANT`, `PAUSE_VERIFY_FAILED`, `PAUSE_NO_PROGRESS`, `PAUSE_REMOTE_MOVED`
- `PAUSE_AGENT_FAILED` (after one retry)
- `PAUSE_PUSH_FAILED` (`git fetch` or `git push` failed), `PAUSE_INTERRUPTED` (a fix step was interrupted and left changes) (D32)
- `PAUSE_SKIPPED` (autopilot: every finding left needs the owner's answer; D61)
- `PAUSE_USAGE_LIMIT` (when waiting isn't possible, see below)
- `PAUSE_TIMEOUT` (fix step only; audit-type steps get one retry first)

**On a pause:**
1. Write `pending.md` in the run folder: what happened, what twokey needs, and the exact command or keys to continue.
2. Send a notification.
3. In stepper mode, a decision or ruling waits at the terminal prompt. The owner can answer there, or quit (Ctrl+C) and run `twokey resume` later. Without input (end of file), the run pauses (D34). Autopilot never waits at a prompt (D60): every pause saves the state, writes `pending.md`, notifies and exits with code 3, without pushing.

**Decisions** (`PAUSE_DECISION`):
- For each finding, show title, evidence, options and the recommendation. The owner picks an option number, types their own text, or picks `drop` (not needed). `drop` resolves the finding (section 4, case c), so it isn't sent to the fix step.
- Save to `decisions.md` in the run folder and add a twokey log entry.
- Every later prompt gets all decisions (`${decisions}`), so the audits don't raise them again.

**Rulings** (`PAUSE_ADJUDICATE`): show the finding, convergence's reason and Codex's evidence for keeping it. The owner picks `fix` or `drop` and may add a note. Save and log it like a decision.

**Automatic decisions** (autopilot with `[autopilot] decide = true`, the default; D61). Autopilot answers for the owner, so a run doesn't wait for someone who isn't there:
- A decision takes the merged finding's `recommended_option` from the convergence step.
- A finding that Codex maintained after its re-check is treated as confirmed and fixed (ruling `fix`); the next round's audits check the result.
- A decision without a recommended option, and a finding that came back a second time (`PAUSE_RECURRING`), are **skipped**: not fixed this round, listed in `pending.md` and the report as waiting for the owner. The rest is fixed. If only skipped findings are left, the round pauses with `PAUSE_SKIPPED`.
- Every automatic choice goes into `decisions.md`, the report and a twokey log entry, marked "automatic": the finding, the options, the choice, and who recommended it (the convergence step, plus each blind-audit finding in `merged_from` that recommended an option, with its agent). Later prompts get it in `${decisions}` like the owner's own.
- `twokey decide <ID> <answer>` answers a skipped finding or replaces an automatic choice: an option number, the owner's own text, `fix` or `drop` (a finding that was disputed, keeps coming back or isn't a decision takes only `fix` or `drop`). It is recorded and logged like a decision, says which automatic choice it replaces, and later steps of the run follow it. It refuses while a run is going (the lock), and on a run that converged.
- With `decide = false`, autopilot pauses at a decision or ruling instead (`PAUSE_DECISION`, `PAUSE_ADJUDICATE`, `PAUSE_RECURRING`), and the owner answers with `twokey resume` in stepper mode or with `twokey decide`.
- Stepper mode always asks the owner.

**Stopping a run** (`twokey stop`, D63). `twokey stop` writes a request into `<run_dir>/stop`. The run reads it at the start of each agent step and every 2 seconds: it lets the running step finish, starts no new agent step (a finished fix still gets its checks and push), saves its state as stopped and exits. `twokey stop --now` also stops the running step, like Ctrl+C: the agent's processes end and its Docker resources are removed. A fix stopped this way may leave changes, which resume shows (`PAUSE_INTERRUPTED`). `twokey resume` or `twokey round` continues the run. Ending twokey from outside (closing its window, Task Manager) skips the clean-up until the next resume.

**Notifications:**
- A Windows toast through PowerShell with no extra modules, plus a terminal bell.
- Optional phone push through ntfy: an HTTP POST with `net/http` to the topic in config (M3, v0.2.0-rc.7; until then the topic is ignored).
- Messages say only the run ID, the event and the next action. They never include findings text or course details.
- Send on every pause, on a finish (converged or max rounds), on a crash, and when twokey starts waiting for a usage limit.

**Usage and rate limits:**
- Detect them from Claude's result (`is_error` with a limit message) and from Codex's `turn.failed`/`error` events, only when the step failed. The text patterns are a configurable list, because the wording changes; the default adds `hit your` for both CLIs' current wording (D46).
- Reset times twokey reads (local clock times): Codex's "Try again at 3:45 PM." or "Try again at Nov 14th, 2026 3:45 PM."; Claude's "resets 3:45pm", "resets 3pm" and "resets Mon 12:00am"; a gateway's "resets Dec 1" (00:00 UTC); and a Unix time after `|`.
- If a reset time can be read and the wait is ≤ `limits.max_wait_hours` (default 5), notify "waiting until HH:MM", sleep (Ctrl+C stops it), then retry. At most 3 waits per step.
- An audit, convergence or dispute step reruns from scratch.
- A fix step reruns only if the tree is clean and no new commit was made; otherwise `PAUSE_USAGE_LIMIT`.
- Otherwise `PAUSE_USAGE_LIMIT`, with the reset time if known. `resume` runs the step again.

**Timeouts** (config, in minutes; defaults: audit 90, convergence 75, dispute 30, fix 120, verify 20): kill the process tree (close the job object) and remove the step's Docker resources.
- An audit, convergence or dispute step gets one fresh retry. A second timeout means `PAUSE_TIMEOUT`, and `resume` runs the step again.
- A fix step gets `PAUSE_TIMEOUT` at once, because it may have left the tree dirty. The pause shows `git status`, and the check after the step doesn't count the uncommitted changes as a violation; everything else is still checked (D47).

**Resume:** `state.json` records every step with its status, attempt count, session or thread ID, paths and timestamps. Write it atomically after every change.
- `twokey resume` picks up from the last finished step.
- A step left `running` (because of a crash or Ctrl+C) first gets its Docker cleanup. Then it reruns if it's an audit, convergence or dispute step.
- A `fix` left `running` pauses with the current `git status`; twokey never discards the owner's tree. If the tree is clean and HEAD is still the round's commit, the fix simply runs again.
- After a pause, resume first resolves its reason (D32): a failed agent step runs again (also after `PAUSE_USAGE_LIMIT` and `PAUSE_TIMEOUT`); a failed check runs the checks again; `PAUSE_NO_PROGRESS` reruns the fix if HEAD hasn't moved; `PAUSE_REMOTE_MOVED` continues only once the remote branch is part of local history (the owner merged or rebased by hand); `PAUSE_INVARIANT` continues once the tree is clean (the owner looked and undid what was wrong). A pause from before D62 that waited for the owner to confirm a protected branch takes the change as someone else's and notes it.
- A step that answered and then paused at its checks isn't run again: resume publishes and logs its answer and goes on (D41).

---

## 11. Configuration

One TOML file per project, kept in the project's root and git-ignored. The default name is `local-only_twokey.toml`; `--config <file>` picks another. Validate the config on load, with clear messages that name the key.

This is the template `twokey init` writes (`assets/config.toml`; a test keeps this copy identical):

```toml
# twokey configuration for one project.
# Keep this file in the project's root, and make sure git ignores it.
# Every setting is explained in docs/SPEC.md, section 11.

[project]
branch = "my-branch"                        # the branch twokey audits, fixes and pushes
remote = "origin"
protected_branches = ["main"]               # twokey checks that these never change
upstream = ""                               # e.g. "main": warn when branch is behind it; "" = off
log_file = "local-only_logs.md"
log_marker = "<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->"
run_dir = "local-only_twokey"               # inside the repo, must be git-ignored
extra_read_dirs = []                        # absolute paths the agents may read, e.g. a slides folder

[checkpoint]
label = "Checkpoint 3"
requirements_file = "local-only_checkpoint-3-instructions.md"

[loop]
mode = "stepper"                            # or "autopilot"
max_rounds = 4
fix_p3 = false

[autopilot]                                 # unattended rounds (--autopilot, schedules)
decide = true                               # take the recommended option, fix what Codex maintains; false = pause for you

[verify]
commands = ["npm run lint", "npm run format:check", "npm run typecheck", "npm test", "npm run build"]

[codex]
command = "codex"
label_prefix = "Codex"                      # log heading: "<label_prefix> - <model name>"
label = "Codex - GPT-6.1-Sol"               # log heading when Codex doesn't report its model
model = ""                                  # "" = the CLI's default
sandbox = "workspace-write"                 # see the doctor --smoke result
network = true
web_search = ""                             # "" = Codex's default (cached); "live" = current pages
desktop_tools = false                       # true = allow screen and real-browser control
extra_args = []
ports = "56000-56499"

[claude]
command = "claude"
label_prefix = "Claude Code"
label = "Claude Code - Claude Opus 5.5"
model = ""
permission_mode = "auto"
desktop_tools = false
max_budget_usd = 0                          # 0 = no limit
extra_args = []
ports = "56500-56999"

[model_names]                               # model ID = name used in log headings
"gpt-6.1-sol" = "GPT-6.1-Sol"
"claude-opus-5-5" = "Claude Opus 5.5"

[timeouts]                                  # minutes
audit = 90
convergence = 75
dispute = 30
fix = 120
verify = 20

[limits]
max_wait_hours = 5
usage_limit_patterns = ["hit your", "usage limit", "rate limit", "try again"]

[notify]
toast = true
ntfy_topic = ""                             # "" = off (phone push arrives in v0.2)
```

**Rules** (DECISIONS.md D26):
- `[project] branch`, `[checkpoint] label`, `[checkpoint] requirements_file` and `[verify] commands` must be in the file (`commands = []` is allowed, and `doctor` warns about it). Every other key has the default shown above.
- Unknown keys and sections are errors, so a typo can't be ignored silently.
- `[model_names]` entries are added to the built-in ones shown above.
- `extra_args` may not contain options twokey sets itself or never uses (section 9).
- The two port ranges may not overlap. `branch` may not be one of the `protected_branches`.
- `[codex] web_search` is `""`, `"disabled"`, `"cached"`, `"indexed"` or `"live"`.
- `[autopilot] decide` (default `true`): how autopilot answers decisions and rulings (section 10, "Automatic decisions"; D61).
- `upstream` is `""` (off) or a branch name other than `branch`. It may be, and usually is, one of the `protected_branches`: twokey only reads it and compares `branch` with it; it never moves `branch` (section 3, "Upstream").

`twokey init` writes this template to the repository root. It fills in `branch` with the current branch, and `protected_branches` with the remote's default branch, when git knows them. It refuses to overwrite an existing file, and warns if `.gitignore` doesn't ignore both the config file and `run_dir`.

---

## 12. Commands and user experience

| Command | What it does |
|---|---|
| `twokey init` | Creates the config template and checks `.gitignore`. |
| `twokey doctor [--smoke]` | Checks git, both CLIs and their versions, the flags twokey uses and their allowed values (via `--help`), the config, the repo preflight (including git settings that would get agents past the push block, D62, `git fetch`, and how the branch stands to `upstream`: behind or diverged is a `[WARN]` with the count, D57), what Codex's sandbox can't run (a `[WARN]`, D51), the staging and scratch folders, Docker (`[WARN]` only, never `[FAIL]`) and the notification setup. `--smoke` makes one small schema-constrained call to each agent and resumes it once (D48): it tests the schema options, Codex's configured sandbox (a scratch write plus one network request), that a resumed session keeps its sandbox (a second write), that each agent can read the first bytes of a file in each extra read folder, that the push block reaches each agent's commands (`git remote get-url --push` prints the blocked URL, D62), the arguments that turn off screen and browser control, and that the repository is unchanged afterwards. twokey checks the files and bytes itself instead of trusting the answers, and shows the model each CLI reports and Claude's cost estimate. It needs the config and both CLIs; other `[FAIL]` lines don't block it. Prints `[OK]`, `[WARN]` or `[FAIL]` lines with the next step for each failure. Exit code 1 if any check failed. |
| `twokey run [--autopilot] [--max-rounds N]` | Starts a run. Refuses if preflight fails. `--fake-agents <folder>` plays scripted answers instead of calling the CLIs (tests and demos). |
| `twokey round [--autopilot]` | Runs one round and stops after its push (section 3, "One round"; D58). It continues the latest run, or starts a new one if the latest converged. Takes `--fake-agents` like `run`. |
| `twokey resume [--run ID] [--more-rounds N]` | Continues the latest (or given) run. Takes `--autopilot` and `--fake-agents` like `run`. |
| `twokey decide <ID> <answer>` | Answers a finding of the latest run: one that autopilot skipped, or one it decided for the owner (section 10, D61). |
| `twokey stop [--now]` | Stops the run that is going, before its next agent step, or with `--now` during the current one (section 10, D63). |
| `twokey status [--run ID]` | One screen: run, status (running with its process, paused with the reason), round, each step's status, time and tries, the last gate result, and what to do next. |
| `twokey render <step> [--round N]` | Renders a prompt without running it, for tuning templates. |
| `twokey report [--run ID]` | Rebuilds `report.md`. |
| `twokey unlock` | Removes a stale lock after confirming that the PID inside it isn't running. |
| `twokey schedule add\|remove\|until\|on\|off\|status\|test` | Starts one round in autopilot at set times, with Windows Task Scheduler ("Scheduled rounds" below; D65, D66). |

**Stepper mode:**
- Before each step it prints the step, the agent and the path of the rendered prompt, then waits: `Enter` = run, `e` = open the prompt in the editor (`notepad` by default) and run the edited version, `q` = quit (resume later). The checks after a fix and the push wait the same way, without `e`.
- Keep edited prompts next to the rendered ones, so the owner can carry improvements back into their templates.
- After each step, it shows a findings table (ID, severity, kind, status, title) and the gate's next action. It doesn't wait there: the next step's prompt, or a question for the owner, is the one stop between steps (D56).
- When an answer is sent back for a second try (section 9), a `[WARN]` names the reasons, and the heartbeat shows the step as `(try 2)`.

**Autopilot:** no prompts at all (D60). Show the heartbeat, a one-line result per step, and the gate result per round. Decisions and rulings are answered as in section 10, "Automatic decisions", and every pause exits. While the loop runs, twokey keeps the computer awake (D64).

**Scheduled rounds** (`twokey schedule`, D65, D66). The owner schedules single rounds at set times; while the schedule is on, each start runs `twokey round --autopilot` with no Enter presses and automatic decisions (section 10).
- Commands: `add <HH:MM>` (every day) or `add <YYYY-MM-DD> <HH:MM>` (once); `remove <time>` or `remove all`; `until <YYYY-MM-DD>` (the last day a round may start; after it the schedule switches itself off) or `until none`; `on` (registers the task, from this `twokey.exe`); `off` (disables the task; a round that is running carries on, and `twokey stop` stops it); `status`; `test`. `--config` comes before the other arguments.
- The schedule lives in `<run_dir>/schedule.json`, not in the config file, so twokey never rewrites the owner's commented TOML. One Task Scheduler task per repository, named `twokey <folder> <hash>`, with one trigger per start time, built from the schedule each time it changes while it is on. `schtasks /Create /XML` takes the task as UTF-16 XML (D65):
  - it runs only while the owner is logged on (`InteractiveToken`; a locked screen is fine), in their session, with their logins, SSH key and Docker Desktop; no password is stored, and no admin rights are needed;
  - it wakes the computer (`WakeToRun`), doesn't start on battery, and isn't stopped when the computer switches to battery;
  - a start missed while the computer was off isn't made up later (`StartWhenAvailable` off);
  - a second start while one runs is allowed (`Parallel`), so twokey itself records the skip; `ExecutionTimeLimit` is 24 hours, a backstop.
- Each start runs `twokey schedule fire` in the repository, in a new window. It never waits at a prompt, and it records the start in `<run_dir>/schedule-history.jsonl` (once when it starts, again when it ends), with its console output in `<run_dir>/schedule-logs/`. In order:
  1. The schedule is off: skip. The end date has passed: switch the schedule off and record it.
  2. Skip, changing nothing, if another twokey holds the lock, if the repository isn't on `branch`, if the working tree isn't clean (the owner may be working in it), or if the latest run is paused for a reason that waits for the owner. A lock whose process has ended is removed, as `twokey unlock` would. A later start picks up a pause for a usage limit, a failed push, or a question autopilot answers (`PAUSE_DECISION`, `PAUSE_ADJUDICATE`, `PAUSE_RECURRING`, `PAUSE_SKIPPED`); every other pause is a safety stop.
  3. Run `twokey round --autopilot` (section 3, "One round"), and record what it did: the findings fixed and the commits pushed, automatic decisions with how to change them, findings that wait for the owner, the pause, or the first `[FAIL]` line.
- `twokey schedule status` is the morning view: the schedule and its next start; the task, with a warning if it is missing, disabled while the schedule is on (or the other way round), or runs another `twokey.exe`; a round that is running now; what each start since the owner last looked did, and the planned starts that didn't happen ("the PC was off, asleep, on battery or logged out", 10 minutes after the planned time); and what waits for the owner (a paused run, skipped findings, `report.md`). It then remembers when the owner looked.
- `twokey schedule test` registers a one-time task 2 minutes ahead that runs the start's checks (step 2) without agents and records the result; Task Scheduler deletes it an hour after it expires.
- Schedules need Windows; `status` works everywhere.

Status labels are plain ASCII: `[OK]`, `[WARN]`, `[FAIL]`, `[PAUSE]`, `[..]`.

**Exit codes** of `run` and `resume` (D31): 0 = converged, or the owner stopped the run to resume later; 1 = preflight failed or something broke; 2 = wrong command line; 3 = paused, waiting for the owner (every `PAUSE_*` reason); 4 = stopped after the last round with findings left; 5 = one round (`twokey round`) is done: its fix is pushed but not audited yet (D58).

---

## 13. Run folder and final report

```
<run_dir>/
  lock                                  # PID + run ID, held for the whole run
  schedule.json                         # twokey schedule: start times, on or off, end date (D65)
  schedule-history.jsonl                # one record per scheduled start (D66)
  schedule-logs/                        # each scheduled start's console output
  stop                                  # a request from twokey stop, used up when read (D63)
  templates/                            # optional per-project template overrides
  runs/<YYYYMMDDTHHMMZ>-<label-slug>/
    state.json   decisions.md   pending.md   report.md
    log-backups/
    r1/
      codex-audit.prompt.md   codex-audit.json   codex-audit.events.jsonl   codex-audit.stderr.log
      claude-audit.prompt.md  claude-audit.json  claude-audit.stdout.json   claude-audit.stderr.log
      convergence.*   dispute.*   fix.*   verify.log   push.log   gate.json
```

**Templates:** the defaults ship inside the binary (from `assets/` in the repository). If `<run_dir>/templates/<name>.md` exists, it overrides the default. `twokey init --templates` copies the defaults there for editing.

**`report.md`** is also inserted into the log as a twokey entry at the end. It contains:
1. The outcome: converged, max rounds reached, one round done (its fix is pushed but **hasn't been audited yet**, D58), or paused (why).
2. A table per round: commit, Codex P1/P2/P3 counts, Claude P1/P2/P3 counts, confirmed blocking, action taken.
3. Commits pushed (short SHA and subject).
4. **For the owner / the team:** all `external_evidence` items, merged across rounds.
5. P3 items not fixed.
6. Waiting for the owner, if any: findings autopilot skipped, with how to answer each (D61). Then decisions and rulings, the automatic ones marked, with how to change them.
7. Disputes and how they ended.
8. Permission denials (Claude) and sandbox refusals (Codex), with the rule the owner could add.
9. Time per step; Claude's estimated cost and Codex's token usage.
10. Notes: gate notes, protected-branch changes, model reroutes, and log headings that differ from the configured label (D44).
11. Which log entries to read for detail (by heading).
12. Last, after a converged run with `upstream` set, if the branch has commits the upstream lacks: "**Next:** merge `<branch>` into `<upstream>` (merge commit, not squash). Then run git merge --ff-only `<remote>/<upstream>` and git push yourself." The terminal shows it too (D42).

`twokey report` rebuilds `report.md` from `state.json` without adding a log entry.

---

## 14. Prompt templates (starter versions)

These are starting points. The owner will tune them, so keep them as plain files. The **preamble** goes in front of every step template.

**Placeholder rules** (D13):
- Only `${name}` is a placeholder. A missing name is an error.
- `$${` produces a literal `${`.
- A bare `$word`, such as `$HOME` or `$env:TEMP` in a shell snippet, stays as it is. So does a `$` not followed by `{`.
- `${step_write_rule}` contains `${branch}`, so it is rendered first.

Placeholders:
- **Every step:** `${run_id}`, `${round}`, `${max_rounds}`, `${agent_name}`, `${other_agent}`, `${repo_path}`, `${branch}`, `${protected_branches}`, `${checkpoint_label}`, `${requirements_path}`, `${extra_read_dirs}`, `${log_file}`, `${commit}`, `${upstream_status}` (how the commit stood to `<remote>/<upstream>` when the round started, the same text for both blind audits; D42), `${sandbox_limits}` (for Codex: the commands its sandbox can't run, from the check at the start of the run; otherwise "none known."; D51), `${scratch_dir}`, `${container_prefix}`, `${port_range}`, `${step_write_rule}`, `${history}`, `${decisions}`, `${id_prefix}`
- **`convergence`, `fix` and `dispute`:** `${codex_audit_path}`, `${claude_audit_path}`, `${convergence_path}`, `${prior_merged_paths}`, `${fix_list}`, `${p3_rule}`, `${verify_commands}`, `${disputes}`

### preamble.md
```
# Standing rules (twokey run ${run_id}, round ${round} of at most ${max_rounds})

You are one step in an automated audit-and-fix loop run by a script called twokey. ${other_agent} works on the same repository in other steps. The repository's owner reads the results.

Repository: ${repo_path}
Branch: ${branch}. Never switch branches, never touch ${protected_branches}, never create a merge request or pull request.
Milestone: ${checkpoint_label}
Requirements: ${requirements_path} and every source it lists. Fetch the sources yourself in this step. Where sources conflict irreconcilably, the later week wins.
Open the URLs listed in the requirements file directly, not through search results.
Network: if curl or PowerShell can't make an HTTPS request in your sandbox, use Node.js instead (for example node -e "fetch('https://example.com/').then(r => r.text()).then(t => console.log(t))"), or your web search tool.
Sandbox limits: ${sandbox_limits}
Extra folders you may read but must not change: ${extra_read_dirs}

Hard rules:
1. Do not create, edit or delete ${log_file}. twokey writes the log. Put your entry in your final JSON answer (log_entry_markdown), without a heading line.
2. ${step_write_rule}
3. Put every probe, download, copy, build output and other scratch file in ${scratch_dir}. Never in the repository, and never in twokey's run folder. Do not use git worktree or git stash.
4. Docker: you may start your own containers, Compose projects, networks and volumes, named with the prefix ${container_prefix}, on host ports ${port_range} only. Remove them before you answer, with their unnamed volumes (`docker run --rm`, `docker rm -f -v`, `docker compose down -v`); twokey also removes anything with your prefix after this step. Never stop, reset or change containers, volumes or data you did not create in this step. The owner's own stack may be running; leave it alone.
5. Live cluster and production: read-only commands only (get, describe, logs). Never apply, delete, scale, restart or roll out. If a check needs a browser login, don't wait for it; record it as external_evidence.
6. Screen control and the owner's real browser are off for this step. For browser checks, use a headless test browser (for example Playwright) with a throwaway profile in ${scratch_dir}.
7. Never invent evidence about the team, GitLab, browsers or the presentation. If you could not verify something, say so and classify it as external_evidence.
8. Nobody can answer questions during this step. Don't ask. Decide what you can; put real choices in a needs_decision finding with 2-4 options and your recommendation.
9. Stop any background process you started before you answer.
10. Your final answer must be only the JSON object that the schema asks for.

Severity:
- P1: a mandatory requirement is missing or broken, or a defect would fail a graded demonstration.
- P2: a requirement is only partly met; a real defect a grader or user is likely to hit; or documentation that is wrong about a required item.
- P3: polish, wording, unlikely edge cases, nice-to-haves.
Kind:
- repo_fix: fixable by changing files in the repository.
- needs_decision: the owner must choose between options before it can be fixed.
- external_evidence: lives outside the repository (GitLab pipeline history, browser runs, team actions, attendance, logins).

Evidence: cite file:line and the commands you ran with their results. Say what you reproduced and what you only read.

The owner's decisions so far (follow them; don't re-raise them without new evidence):
${decisions}

History of this run:
${history}
The full log ${log_file} holds older history. It is very large: search it (for example by heading) instead of reading all of it.
```

### audit.md
```
# Your task: blind audit (${agent_name})

Audit the repository at commit ${commit}. The question: is it ready for ${checkpoint_label}?

This audit is blind. ${other_agent} is auditing the same commit right now; you won't see its work in this step, and it won't see yours.

Upstream branch: ${upstream_status}

1. Read the requirements and every source they list.
2. Check code, tests, configuration and documentation against each requirement. Run what you need to run, in a copy under your scratch folder whenever it would write anything.
3. If earlier rounds of this run fixed findings (see History), check that each fix works.
4. Give each finding an ID ${id_prefix}-NN (for example ${id_prefix}-01), a severity, a kind, evidence and a fix hint.
5. Write your log entry in the style of earlier entries: Task, Revision, Verdict, Method, findings in priority order, what you checked and what you could not check. Briefly credit what is done well.

verdict = "ready_for_human_review" only if you have no P1/P2 repo_fix or needs_decision findings; otherwise "not_ready".
```

### convergence.md
```
# Your task: convergence check

Two blind audits of commit ${commit} are finished:
- ${codex_audit_path}
- ${claude_audit_path}
One of them is your own earlier audit. Treat it with the same scepticism as the other.

Upstream branch: ${upstream_status}

1. Independently verify every finding that only one audit raised, P1 and P2 first. Reproduce where you can.
2. Merge findings that both audits raised.
3. Use what you learned to look for related problems neither audit found. Add them with source "convergence".
4. Give every blocking finding (P1/P2, repo_fix or needs_decision) from either audit a status:
   - confirmed: real, and still P1 or P2;
   - disputed: wrong, or not P1/P2. Give dispute_reason with evidence.
   Use "noted" for P3 and external_evidence items. If a finding repeats a problem that an earlier round fixed, set recurs_of to that earlier ID (earlier merged lists: ${prior_merged_paths}).
5. Use IDs ${id_prefix}-NN for merged findings, with merged_from listing the original IDs.
6. Log entry: what each audit got right, what it got wrong, what both missed, and the merged list.
```

### dispute.md
```
# Your task: re-check disputed findings

The convergence step (${convergence_path}) disputed these findings from your blind audit of ${commit}:
${disputes}

For each one, read the dispute reason and check again. Answer "withdraw" if you now agree it isn't a P1/P2 problem, or "maintain" with new evidence. This isn't a debate to win; be accurate.
```

### fix.md
```
# Your task: fix

Implement these confirmed findings from ${convergence_path}:
${fix_list}
${p3_rule}

1. Work on ${branch} at ${commit}. Follow the owner's decisions exactly.
2. Update the documentation each fix touches, including any audit or verification sections in the guides.
3. Run the project's checks: ${verify_commands}. Commit with clear messages and keep your usual Co-Authored-By trailer. One or more commits are fine.
4. Do not push. twokey checks your commits and pushes them.
5. Anything that needs a decision you can't make, or work outside the repository (GitLab settings, the cluster, teammates), goes in not_done with the reason. Don't attempt it.
6. Log entry: for each finding, what you changed and how you verified it; then what you did not do and why.
```

`${step_write_rule}`:
- **Audit, convergence and dispute steps:** "This step must not change the repository: no edits to tracked or untracked files, no commits, no branch or stash changes. twokey checks this afterwards and stops the run if anything changed."
- **Fix step:** "You may edit files and commit to ${branch}. Do not push."

`${history}` lists, for each earlier step of this run, the log heading and the path of its published JSON. `${decisions}` is the content of `decisions.md`, or "None yet." `${extra_read_dirs}` lists the folders one per line, or "None."

---

## 15. Testing

- **Unit tests** (`go test`, table-driven) for the pure parts. Pure logic lives in functions without I/O; OS calls stay in thin wrappers that tests can replace:
  - log insertion: marker missing or duplicated, a marker mentioned inside a sentence, LF/CRLF preserved, BOM preserved, heading demotion (not inside code fences), marker stripping, ordering of two inserts, a file edited between reads;
  - template rendering: a missing placeholder is an error; `$${` gives `${`; a bare `$HOME` or `$env:TEMP` stays as it is;
  - schema validation and the convergence coverage rule;
  - **the gate, table-driven, with at least one case per row of section 4** plus combinations: P3-only means converged; P1 `external_evidence` only means converged; a verdict mismatch;
  - state transitions and resume behaviour for every step status;
  - config validation messages;
  - the log heading label: reported model with and without a `[model_names]` entry, no reported model, and the mismatch flag;
  - Docker cleanup with a fake `docker`: only names with the step's prefix are removed, also when another name merely contains the prefix.
- Tests never read or write the developer's real home, git config or projects: they use `t.TempDir()` and `t.Setenv` (including `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM`).
- **Fake agents:** `--fake-agents <scenario-folder>` replaces both runners with scripted answers per (round, step). A fake fix step makes a real commit. The scenario format is described in `internal/runner/fake.go`: per round, `<step>.json` answers (with `${commit}` and `${new_commits}`), `<step>.<attempt>.json` for a resumed retry, and `<step>.actions.json` for what the fake agent does first: write files, commit, append to a file, push, or let a teammate push to the branch or, with `teammate_branch`, to another one (D37, D40), or set git config (D62). A fake agent's push uses the agents' environment, so the push block applies to it. End-to-end tests run whole loops in a temp git repo with a local bare "remote", covering: converge in round 1; fix then converge; dispute withdrawn; dispute maintained then ruling; decision pause; recurrence; max rounds; verify failure then success; remote moved; an agent touching the log (expect `PAUSE_INVARIANT`); an agent's own push fails (D62), while one that adds a `pushurl` gets through and is caught (`PAUSE_INVARIANT`); preflight refuses a `pushurl`; resume after a pause at a step's checks (D41). With `upstream` set (D39, D57): behind and diverged (a warning, nothing moves, the round audits the branch as it is), ahead, equal, and the upstream moving during a round. Protected branches (D40, D62): one moved during a step is noted and accepted, and one moved while an agent got past the push block is part of a `PAUSE_INVARIANT`. Running agents (M2): an audit's timeout gets one fresh try, two mean `PAUSE_TIMEOUT`; a fix's timeout pauses at once; a usage limit with a near reset time is waited out, one without pauses (`PAUSE_USAGE_LIMIT`), and a fix that committed before its limit pauses; Docker cleanup after every step; Claude session IDs chosen up front; usage of a resumed Claude session counted once, whether or not Claude restored its earlier totals; an answer sent back is shown and reported; raw output published; the heartbeat; one stop between steps in stepper mode, with the checks' own prompt; `status` and `report`. One round (D58): it stops after the push and the next one continues the run; nothing runs after a convergence until the branch moves; it fixes past `max_rounds`; it finishes a round that stopped before its fix, and the last round of a run stopped at `max_rounds`; between rounds it needs a clean start. Autopilot (D60, D61): the recommended option is taken and recorded with who recommended it; a decision without a recommendation and a recurring finding are skipped while the rest is fixed; only skipped findings left means `PAUSE_SKIPPED`, also on the next try, until `twokey decide`; a maintained dispute is fixed; with `decide = false` it pauses; `twokey decide` replaces an automatic choice and refuses wrong answers. `twokey stop` (D63) stops before the next agent step, and `--now` cancels the running one; keep-awake is held for the loop (D64). The push block itself is tested with a local bare remote: push by name and by URL fails, fetch and `ls-remote` work, and a `pushurl` or another `pushInsteadOf` gets past it, which is why twokey refuses them.
- **Schedules** (D65, D66): start times, end dates, the next and the missed starts, and the start a late launch belongs to are pure functions with table tests; the task XML is checked for every setting, its UTF-16 encoding and its escaping, and the parser reads what `schtasks /Query /XML` printed on the owner's machine. The commands run against a fake `schtasks.exe` that keeps tasks in memory. A scheduled start runs a whole round with fake agents in a throwaway repository and records it; the skips (a dirty tree, another run, the schedule off, the end date passed, a safety stop) change nothing, and a test start runs no agent. The morning view lists what ran, the starts that didn't happen and what waits for the owner.
- **Real CLIs without calling them:** the command lines are built by pure functions with table tests, and the output parsers and runs are tested with output in the documented formats, written by a fake launcher. The process tree launcher is tested with the test binary as child and grandchild. `doctor --smoke` is tested with fake agents that do what the prompt asks.
- **CI:** GitHub Actions on `windows-latest` and `ubuntu-latest`, running `go vet` and `go test` (plus `go test -race` on Linux), `golangci-lint`, `govulncheck` and a `go mod tidy` check. Actions are pinned to full commit SHAs and tool versions are pinned; new versions must be at least 7 days old (D2). No real agent calls in CI, ever.
- **Smoke test on the owner's machine:** `twokey doctor --smoke`, with their OK.

---

## 16. Milestones and releases

| Milestone | Content | Done when |
|---|---|---|
| **M0** | Repo scaffold, `go.mod` (module `github.com/Sten-Qy-Li/twokey`, binary from `./cmd/twokey`), LICENSE, NOTICE, README, `docs/SPEC.md`, `docs/DECISIONS.md`, `CLAUDE.md`, config loading + validation, `init`, `doctor` without `--smoke`, CI | CI green; `twokey doctor` runs on the owner's machine |
| **M1** | Log insertion, templates and rendering, schemas + validator, the gate, the state machine, git snapshot/invariant helpers, fake-agent runner, verify commands, push with checks, basic text answers for decisions and rulings, `render`, `unlock`, **stepper mode end-to-end with fake agents** (D22) | All unit and fake end-to-end tests green on Windows |
| **M2** | Real runners (Codex, Claude) in job objects, staging/blindness, timeouts and process-tree kill, Docker cleanup, retries with session resume, usage-limit detection, log labels from the reported model, `doctor --smoke`, `status`, `report` | **v0.1.0 (stepper):** one supervised real round on the owner's project, with their go-ahead |
| **M3** | Redefined on 2026-11-13 (D59): one round at a time (`twokey round`); autopilot as the unattended mode, with automatic decisions, `twokey decide` and `twokey stop`; agents can't push; keep-awake; rounds scheduled with Windows Task Scheduler (`twokey schedule`), with a morning view in `schedule status`; notifications (toast and ntfy). Released as v0.2.0-rc.4 to rc.7 | **v0.2.0:** one scheduled night on the rehearsal copy, then one on the owner's project, reviewed by the owner in the morning |

**Timing:** The owner's next checkpoint is on Fri 20 Nov. Aim for v0.1 by Sun 15 Nov, so they can use stepper mode to prepare. Autopilot shouldn't be on the critical path for that checkpoint. If it isn't solid by 18 Nov, they keep using the stepper. For M3, if time gets short, the schedule comes before the notifications: the morning view is enough at first.

---

## 17. Out of scope for v0.x (possible later)

- Checking GitLab/GitHub pipeline status after a push (needs a token and an API client).
- Answering pauses from the phone.
- Other agents (Gemini etc.) or swapping roles. Keep the runner interface simple enough to allow it.
- Running several projects at once.
- A GUI or dashboard.
- Generating the requirements file.
