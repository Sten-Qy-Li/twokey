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
| D4 | **Autopilot pushes fixes automatically** to the project's working branch, with no Enter press. twokey does the push itself after its checks pass (section 8). Agents never push. |
| D5 | **Roles are fixed in v0.x.** Codex: blind audit, dispute re-check. Claude: blind audit, convergence, fix. Keep a small runner interface, so another agent could be added later. |
| D6 | **Two modes.** `stepper` (v0.1): waits for Enter before and after each step. `autopilot` (v0.2): runs unattended and pauses only for the reasons in section 10. |
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
│ sync (script): fast-forward the branch to the upstream, if one is set      │
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

**Sync** (D39). The owner's branch may catch up with another branch, `[project] upstream` (for example `main`, where teammates' merge requests land), but only by fast-forward. With `upstream = ""` (the default) this step does nothing. Otherwise, at the start of each round:
1. `git fetch`. Local `HEAD` must equal `<remote>/<branch>`. The one exception is a fast-forward toward the upstream whose push failed; it is pushed now. Otherwise pause with `PAUSE_REMOTE_MOVED`.
2. Compare `<remote>/<branch>` with `<remote>/<upstream>` (`git rev-list --left-right --count`). The decision is a pure function (`internal/upstream`):
   - **Equal, or the branch is ahead:** nothing to do.
   - **The branch is behind** (an ancestor of the upstream): in stepper mode, ask first. Then `git merge --ff-only <the upstream's commit>`, push with the push rules (section 8), and say what moved. The round audits the new commit. The report lists the fast-forward.
   - **Diverged:** pause with `PAUSE_DIVERGED`. The owner merges the branch into the upstream through a merge request with a merge commit (not squash), then runs `twokey resume`. A squash merge leaves the branch's commits out of the upstream, so the two stay diverged for good. `resume` runs this step again and continues once they no longer diverge.

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

**Order of checks (the first match wins)**

| # | Condition | Result |
|---|---|---|
| 1 | An invariant failed in this round | `PAUSE_INVARIANT` |
| 2 | Convergence disputed a blocking finding that Codex raised, and there's no dispute result yet | `RUN_DISPUTE` |
| 3 | The dispute step "maintained" any finding and the owner hasn't ruled yet | `PAUSE_ADJUDICATE` (show both sides) |
| 4 | A confirmed `needs_decision` blocking finding has no decision from the owner | `PAUSE_DECISION` |
| 5 | A confirmed blocking finding has recurred for the second time (`recurs_of` lineage ≥ 2) | `PAUSE_RECURRING` |
| 6 | The confirmed blocking list is empty | `STOP_CONVERGED` |
| 7 | `round >= max_rounds` | `STOP_MAX_ROUNDS` (no fix after the last audit round; `resume --more-rounds N` continues) |
| 8 | Otherwise | `FIX(list)`: the confirmed blocking list, plus confirmed P3 `repo_fix` items if `fix_p3 = true` |

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
- A protected branch that moved during the step may be a teammate's merge or an agent's push, and twokey can't tell which, because the agents use the owner's git login (D40). If that is the only problem, pause with `PAUSE_PROTECTED_MOVED`: show the branch, the old and new SHA, and the new commits (subject and author, up to 20), or say that the history was rewritten. Together with other problems, the pause is `PAUSE_INVARIANT` and lists all of them. Either way, `resume` asks the owner to confirm the change (only `y` counts) and then takes the new SHA as the baseline.
- `state.json` must still be what twokey last wrote (D33).

**Protected branches outside agent steps** (D40). Before each agent step and after each push, twokey reads the protected branches again. A change since the last check happened while no agent was running (between steps or rounds, during the checks, a push or a pause), so it can't be an agent's push. twokey prints a note, adds it to the report, and takes the new SHA as the baseline.

**Preflight (before round 1)**
- The repo is on `branch`; `git status --porcelain` is empty.
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
- Run `git fetch` again. If `<remote>/<branch>` moved since the round started (someone else pushed), pause with `PAUSE_REMOTE_MOVED`. Never merge or rebase automatically, except a fast-forward of `<branch>` to `<upstream>` when `upstream` is set (section 3, "Sync"; D39).
- Then run `git push <remote> HEAD:refs/heads/<branch>`: no force, no push options, never another branch.
- Afterwards, `git ls-remote <remote> refs/heads/<branch>` must equal local `HEAD`. A protected branch that moved since the last check is noted and becomes the baseline (D40).
- The sync step's fast-forward uses the same rules.

---

## 9. Running the CLIs

**General**
- Find the executables with `exec.LookPath`. On Windows, a CLI may be a `.cmd` shim (on the development machine both are real `.exe` files).
- **Always send the prompt through stdin**, never as an argument. This avoids Windows command-line length limits and the quoting problems of `.cmd` shims. (Claude caps piped stdin at 10 MB, which is far more than any prompt.)
- Write stdout and stderr straight to files in the run folder, not pipes, so two parallel processes can't block each other. All text is UTF-8.
- Start each agent in its own Windows **job object** that kills its processes when the job closes. Killing the job ends the agent and every process it started, and the children die even if twokey itself crashes. On other systems, use a process group. A job object doesn't stop Docker containers, because the Docker daemon runs them; see "Scratch, containers and ports".
- Print a heartbeat line every 60 s while steps run, e.g. `[..] round 2: codex-audit 14 min, claude-audit 11 min`.
- While a run is active, ask Windows not to sleep (`SetThreadExecutionState` through `golang.org/x/sys/windows`), and release that at the end.

**Screen and browser control** are off by default for both agents (`desktop_tools = false`): no computer use, and no control of the owner's real browser with their logins. Agents may still run headless test browsers, such as Playwright with a throwaway profile in their scratch folder. `desktop_tools = true` turns screen and browser control back on for that agent. The exact arguments are checked with `doctor --smoke` (D10).

**Extra read folders** (`[project] extra_read_dirs`, for example a slides folder the requirements refer to) are given to both agents. Claude gets `--add-dir=<folder>` for each. Codex's `--add-dir` makes a folder *writable*, so twokey doesn't use it for these; Codex's sandbox can normally read outside its workspace. The prompt lists the folders as read-only. `doctor --smoke` checks that each agent can read a file in each folder (D11).

**Codex (`codex exec`)**, from the docs on 12 Nov 2026, checked against the installed codex-cli 0.165.0 (D3–D7):
- Runs non-interactively. The sandbox is **read-only by default**. `--sandbox workspace-write` allows writes; `--sandbox danger-full-access` removes the sandbox. **`--full-auto` is no longer supported**; don't use it.
- `workspace-write` blocks the network unless `-c sandbox_workspace_write.network_access=true`. Extra writable folders (the scratch folder) are passed with `--add-dir <folder>` (D4).
- **Known Windows problem:** openai/codex issue #34958 reports that on native Windows, `codex exec --sandbox workspace-write` can reject writes inside the working directory. `doctor --smoke` must test the configured sandbox for real: a write to the scratch folder, plus one network request.
- `--json` makes stdout a JSONL event stream (`thread.started` with `thread_id`, `turn.started`, `turn.completed` with usage, `turn.failed`, `item.*`, `error`). `--output-schema <file>` makes the final answer match the schema. `-o <file>` writes the final message to a file.
- twokey passes `-c notify=[]`, so the owner's Codex `notify` program doesn't run after every turn of an unattended step (D9).
- Resume: `codex exec resume <SESSION_ID> -`. The prompt is read from stdin only with the explicit `-` (D6). `resume` has no `--sandbox`, `-C` or `--add-dir`: twokey passes the sandbox as `-c sandbox_mode="<mode>"`, the writable folders as `-c sandbox_workspace_write.writable_roots=[...]`, and starts the process in the repo folder (D5). `doctor --smoke` checks that a resumed session keeps its sandbox.
- twokey never passes `--worktree`, `--ephemeral` (sessions must be resumable) or `--dangerously-bypass-approvals-and-sandbox`, and the config refuses them in `extra_args` (D7).
- Exit codes aren't documented. Treat a step as successful only if there was no `turn.failed`/`error` event **and** the `-o` file holds JSON that passes validation.
- Shape: `codex exec --json --output-schema <schema> -o <staging>/<step>.json --sandbox <mode> --add-dir <scratch> -c notify=[] [-c ...] [-m <model>] -C <repo> -`

**Claude Code (`claude -p`)**, from the docs on 12 Nov 2026, checked against the installed Claude Code 2.1.311 (D3, D8):
- `--output-format json` returns one JSON object with `result`, `session_id`, `total_cost_usd` (an estimate) and, with `--json-schema '<schema>'`, a `structured_output` field. **Check that `structured_output` exists before using it.** It's missing if the run ended without valid output.
- Exit code 0 means success. A failure inside the run is printed as the result on stdout.
- **Don't use `--bare`.** It's recommended for scripts, but it never reads the subscription login. Without it, `-p` loads the project's `.claude/settings*.json` permission rules, hooks and `CLAUDE.md`, which is what the owner wants.
- Permissions: since v2.1.302, a `-p` run starts in Manual mode, so anything needing approval is denied. Pass the configured `--permission-mode` (default `auto`), plus `--permission-prompts none` (v2.1.259+), which tells Claude nobody can approve and removes `AskUserQuestion`. Denials are listed in `permission_denials`. Copy them into the report, so the owner can add allow rules.
- `--add-dir` gives access to the scratch folder and the extra read folders. It takes several values, so twokey writes `--add-dir=<folder>` to stop it from swallowing the next argument. `--resume <session_id>` continues a session. `--max-budget-usd` exists (`[claude] max_budget_usd`, 0 = off).
- twokey picks the session ID itself and passes `--session-id <uuid>`, so `state.json` holds the ID before the step starts, and a crash can't lose it (D8).
- `--no-chrome` keeps Claude away from the owner's real Chrome unless `desktop_tools = true` (D10).
- twokey never passes `--bare`, `--dangerously-skip-permissions`, `--worktree` or `--no-session-persistence`, and the config refuses them in `extra_args` (D7).
- A `-p` run waits up to 10 minutes for background work the agent started. Templates tell agents to stop their background processes before answering.
- Shape: `claude -p --output-format json --json-schema <schema-json> --permission-mode <mode> --permission-prompts none --session-id <uuid> --add-dir=<scratch> [--add-dir=<read folder>...] --no-chrome [--model <m>]`, run from the repo folder, prompt on stdin. `--json-schema` takes inline JSON only, so twokey passes the minified schema; `doctor --smoke` checks that it survives Windows quoting.

**Invalid answer** (no or invalid JSON, wrong commit, broken coverage rule): resume the same session **once** with a short message listing the validation errors. A second failure means `PAUSE_AGENT_FAILED`.

**Blindness:**
- Codex's `-o` file and Claude's stdout go to a **staging folder outside the repository**, at `%LOCALAPPDATA%\twokey\staging\<run-id>\` (Go's `os.UserCacheDir()`; `~/.cache/twokey/staging` on Linux).
- Copy results into the run folder only after **both** audits have finished and passed their checks.
- If one audit fails and is retried, the other's result stays in staging until the retry finishes.
- Prompts never mention the staging path.

**Scratch, containers and ports:**
- Each agent step gets its own scratch folder outside the repo: `%TEMP%\twokey\<run-id>\r<round>\<step>-<agent>\`.
- Each agent step gets its own Docker prefix, `tk-<run>-r<round>-<step>-<agent>-`, all lowercase, where `<run>` is the run's timestamp (for example `tk-20261114t0930z-r2-audit-cx-`). Compose project names allow only lowercase letters, digits, `-` and `_`, so the prefix uses only those. Each agent also gets its own port range from config (defaults: Codex `56000-56499`, Claude `56500-56999`).
- **Docker cleanup** (D12): after every agent step, and after any timeout, kill or crash, twokey removes the step's Docker resources: Compose projects (`docker compose -p <name> down --volumes --remove-orphans`), then containers (`docker rm --force`), networks and volumes whose names start with the step's prefix. Docker's name filters match anywhere in a name, so twokey lists the names and compares the prefix itself. It never touches a name without the prefix. When a run resumes after a crash, the step that was running gets the same cleanup. If Docker isn't running, the cleanup is skipped with a `[WARN]`.
- Delete scratch folders older than 7 days at the start of a run.

---

## 10. Pauses, notifications, limits and resume

**Pause reasons:**
- `PAUSE_DECISION`, `PAUSE_ADJUDICATE`, `PAUSE_RECURRING`
- `PAUSE_INVARIANT`, `PAUSE_VERIFY_FAILED`, `PAUSE_NO_PROGRESS`, `PAUSE_REMOTE_MOVED`
- `PAUSE_AGENT_FAILED` (after one retry)
- `PAUSE_PUSH_FAILED` (`git fetch` or `git push` failed), `PAUSE_INTERRUPTED` (a fix step was interrupted and left changes) (D32)
- `PAUSE_DIVERGED` (the branch and `upstream` have diverged; D39), `PAUSE_PROTECTED_MOVED` (a protected branch moved during an agent step; D40)
- `PAUSE_USAGE_LIMIT` (when waiting isn't possible, see below)
- `PAUSE_TIMEOUT` (fix step only; audit-type steps get one retry first)

**On a pause:**
1. Write `pending.md` in the run folder: what happened, what twokey needs, and the exact command or keys to continue.
2. Send a notification.
3. Wait at the terminal prompt. The owner can answer there, or quit (Ctrl+C) and run `twokey resume` later. This holds in both modes: autopilot doesn't wait between steps, but it does ask for decisions and rulings. Without input (end of file), the run pauses (D34).

**Decisions** (`PAUSE_DECISION`):
- For each finding, show title, evidence, options and the recommendation. The owner picks an option number, types their own text, or picks `drop` (not needed). `drop` resolves the finding (section 4, case c), so it isn't sent to the fix step.
- Save to `decisions.md` in the run folder and add a twokey log entry.
- Every later prompt gets all decisions (`${decisions}`), so the audits don't raise them again.

**Rulings** (`PAUSE_ADJUDICATE`): show the finding, convergence's reason and Codex's evidence for keeping it. The owner picks `fix` or `drop` and may add a note. Save and log it like a decision.

**Notifications:**
- A Windows toast through PowerShell with no extra modules, plus a terminal bell.
- Optional phone push through ntfy: an HTTP POST with `net/http` to the topic in config (M3; until then the topic is ignored).
- Messages say only the run ID, the event and the next action. They never include findings text or course details.
- Send on every pause, on a finish (converged or max rounds), on a crash, and when twokey starts waiting for a usage limit.

**Usage and rate limits:**
- Detect them from Claude's result (`is_error` with a limit message) and from Codex's `turn.failed`/`error` events. The text patterns are a configurable list, because the wording changes.
- If a reset time can be read and the wait is ≤ `limits.max_wait_hours` (default 5), notify "waiting until HH:MM", sleep, then retry.
- An audit, convergence or dispute step reruns from scratch.
- A fix step reruns only if the tree is clean and no new commit was made; otherwise `PAUSE_USAGE_LIMIT`.

**Timeouts** (config, in minutes; defaults: audit 90, convergence 75, dispute 30, fix 120, verify 20): kill the process tree (close the job object) and remove the step's Docker resources.
- An audit, convergence or dispute step gets one retry.
- A fix step gets `PAUSE_TIMEOUT`, because it may have left the tree dirty.

**Resume:** `state.json` records every step with its status, attempt count, session or thread ID, paths and timestamps. Write it atomically after every change.
- `twokey resume` picks up from the last finished step.
- A step left `running` (because of a crash or Ctrl+C) first gets its Docker cleanup. Then it reruns if it's an audit, convergence or dispute step.
- A `fix` left `running` pauses with the current `git status`; twokey never discards the owner's tree. If the tree is clean and HEAD is still the round's commit, the fix simply runs again.
- After a pause, resume first resolves its reason (D32): a failed agent step runs again; a failed check runs the checks again; `PAUSE_NO_PROGRESS` reruns the fix if HEAD hasn't moved; `PAUSE_REMOTE_MOVED` continues only once the remote branch is part of local history (the owner merged or rebased by hand); `PAUSE_INVARIANT` continues once the tree is clean (the owner looked and undid what was wrong); `PAUSE_DIVERGED` runs the sync step again; `PAUSE_PROTECTED_MOVED`, and a `PAUSE_INVARIANT` that includes a protected branch, first ask the owner to confirm the change (D40).
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
upstream = ""                               # e.g. "main": fast-forward branch to it each round; "" = off
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

[verify]
commands = ["npm run lint", "npm run format:check", "npm run typecheck", "npm test", "npm run build"]

[codex]
command = "codex"
label_prefix = "Codex"                      # log heading: "<label_prefix> - <model name>"
label = "Codex - GPT-6.1-Sol"               # log heading when Codex doesn't report its model
model = ""                                  # "" = the CLI's default
sandbox = "workspace-write"                 # see the doctor --smoke result
network = true
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
usage_limit_patterns = ["usage limit", "rate limit", "try again"]

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
- `upstream` is `""` (off) or a branch name other than `branch`. It may be, and usually is, one of the `protected_branches`: twokey only reads it, and fast-forwards `branch` to it (section 3, "Sync").

`twokey init` writes this template to the repository root. It fills in `branch` with the current branch, and `protected_branches` with the remote's default branch, when git knows them. It refuses to overwrite an existing file, and warns if `.gitignore` doesn't ignore both the config file and `run_dir`.

---

## 12. Commands and user experience

| Command | What it does |
|---|---|
| `twokey init` | Creates the config template and checks `.gitignore`. |
| `twokey doctor [--smoke]` | Checks git, both CLIs and their versions, the flags twokey uses and their allowed values (via `--help`), the config, the repo preflight (including `git fetch`, and how the branch stands to `upstream`: diverged is a `[WARN]`), the staging and scratch folders, Docker (`[WARN]` only, never `[FAIL]`) and the notification setup. `--smoke` makes one tiny schema-constrained call to each agent; it tests Codex's configured sandbox (scratch write plus one network request), a resume of each session, that each agent can read a file in each extra read folder, and the arguments that turn off screen and browser control. Prints `[OK]`, `[WARN]` or `[FAIL]` lines with the next step for each failure. Exit code 1 if any check failed. |
| `twokey run [--autopilot] [--max-rounds N]` | Starts a run. Refuses if preflight fails. `--fake-agents <folder>` plays scripted answers instead of calling the CLIs (tests; until M2 it is required). |
| `twokey resume [--run ID] [--more-rounds N]` | Continues the latest (or given) run. Takes `--autopilot` and `--fake-agents` like `run`. |
| `twokey status` | One screen: run, round, step, elapsed time, last gate result. |
| `twokey render <step> [--round N]` | Renders a prompt without running it, for tuning templates. |
| `twokey report [--run ID]` | Rebuilds `report.md`. |
| `twokey unlock` | Removes a stale lock after confirming that the PID inside it isn't running. |

**Stepper mode:**
- Before each step it prints the step, the agent and the path of the rendered prompt, then waits: `Enter` = run, `e` = open the prompt in the editor (`notepad` by default) and run the edited version, `q` = quit (resume later).
- Keep edited prompts next to the rendered ones, so the owner can carry improvements back into their templates.
- After each step, it shows a findings table (ID, severity, kind, status, title) and the gate's next action, then waits for Enter.

**Autopilot:** no prompts, except the pauses in section 10. Show the heartbeat, a one-line result per step, and the gate result per round.

Status labels are plain ASCII: `[OK]`, `[WARN]`, `[FAIL]`, `[PAUSE]`, `[..]`.

**Exit codes** of `run` and `resume` (D31): 0 = converged, or the owner stopped the run to resume later; 1 = preflight failed or something broke; 2 = wrong command line; 3 = paused, waiting for the owner (every `PAUSE_*` reason, including `PAUSE_DIVERGED` and `PAUSE_PROTECTED_MOVED`, and a resume where the owner didn't confirm a protected branch change); 4 = stopped after the last round with findings left.

---

## 13. Run folder and final report

```
<run_dir>/
  lock                                  # PID + run ID, held for the whole run
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
1. The outcome: converged, max rounds reached, or paused (why).
2. A table per round: commit, Codex P1/P2/P3 counts, Claude P1/P2/P3 counts, confirmed blocking, action taken.
3. Commits pushed (short SHA and subject).
4. **For the owner / the team:** all `external_evidence` items, merged across rounds.
5. P3 items not fixed.
6. Decisions and rulings.
7. Disputes and how they ended.
8. Permission denials (Claude) and sandbox refusals (Codex), with the rule the owner could add.
9. Time per step; Claude's estimated cost and Codex's token usage.
10. Which log entries to read for detail (by heading).

---

## 14. Prompt templates (starter versions)

These are starting points. The owner will tune them, so keep them as plain files. The **preamble** goes in front of every step template.

**Placeholder rules** (D13):
- Only `${name}` is a placeholder. A missing name is an error.
- `$${` produces a literal `${`.
- A bare `$word`, such as `$HOME` or `$env:TEMP` in a shell snippet, stays as it is. So does a `$` not followed by `{`.
- `${step_write_rule}` contains `${branch}`, so it is rendered first.

Placeholders:
- **Every step:** `${run_id}`, `${round}`, `${max_rounds}`, `${agent_name}`, `${other_agent}`, `${repo_path}`, `${branch}`, `${protected_branches}`, `${checkpoint_label}`, `${requirements_path}`, `${extra_read_dirs}`, `${log_file}`, `${commit}`, `${scratch_dir}`, `${container_prefix}`, `${port_range}`, `${step_write_rule}`, `${history}`, `${decisions}`, `${id_prefix}`
- **`convergence`, `fix` and `dispute`:** `${codex_audit_path}`, `${claude_audit_path}`, `${convergence_path}`, `${prior_merged_paths}`, `${fix_list}`, `${p3_rule}`, `${verify_commands}`, `${disputes}`

### preamble.md
```
# Standing rules (twokey run ${run_id}, round ${round} of at most ${max_rounds})

You are one step in an automated audit-and-fix loop run by a script called twokey. ${other_agent} works on the same repository in other steps. The repository's owner reads the results.

Repository: ${repo_path}
Branch: ${branch}. Never switch branches, never touch ${protected_branches}, never create a merge request or pull request.
Milestone: ${checkpoint_label}
Requirements: ${requirements_path} and every source it lists. Fetch the sources yourself in this step. Where sources conflict irreconcilably, the later week wins.
Extra folders you may read but must not change: ${extra_read_dirs}

Hard rules:
1. Do not create, edit or delete ${log_file}. twokey writes the log. Put your entry in your final JSON answer (log_entry_markdown), without a heading line.
2. ${step_write_rule}
3. Put every probe, download, copy, build output and other scratch file in ${scratch_dir}. Never in the repository, and never in twokey's run folder. Do not use git worktree or git stash.
4. Docker: you may start your own containers, Compose projects, networks and volumes, named with the prefix ${container_prefix}, on host ports ${port_range} only. Remove them before you answer; twokey also removes anything with your prefix after this step. Never stop, reset or change containers, volumes or data you did not create in this step. The owner's own stack may be running; leave it alone.
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
- **Fake agents:** `--fake-agents <scenario-folder>` replaces both runners with scripted answers per (round, step). A fake fix step makes a real commit. The scenario format is described in `internal/runner/fake.go`: per round, `<step>.json` answers (with `${commit}` and `${new_commits}`), `<step>.<attempt>.json` for a resumed retry, and `<step>.actions.json` for what the fake agent does first: write files, commit, append to a file, push, or let a teammate push to the branch or, with `teammate_branch`, to another one (D37, D40). End-to-end tests run whole loops in a temp git repo with a local bare "remote", covering: converge in round 1; fix then converge; dispute withdrawn; dispute maintained then ruling; decision pause; recurrence; max rounds; verify failure then success; remote moved; an agent touching the log (expect `PAUSE_INVARIANT`); an agent pushing by itself (expect `PAUSE_INVARIANT`); resume after a pause at a step's checks (D41). With `upstream` set (D39): behind (fast-forward and push), ahead, equal, diverged (`PAUSE_DIVERGED`, then resume after a merge-commit merge), the owner fast-forwarding by hand while paused, and the upstream moving during a round. Protected branches (D40): one moved by someone else during a step (`PAUSE_PROTECTED_MOVED`; only `y` confirms), and one moved together with an agent's push (`PAUSE_INVARIANT`).
- **CI:** GitHub Actions on `windows-latest` and `ubuntu-latest`, running `go vet` and `go test` (plus `go test -race` on Linux), `golangci-lint`, `govulncheck` and a `go mod tidy` check. Actions are pinned to full commit SHAs and tool versions are pinned; new versions must be at least 7 days old (D2). No real agent calls in CI, ever.
- **Smoke test on the owner's machine:** `twokey doctor --smoke`, with their OK.

---

## 16. Milestones and releases

| Milestone | Content | Done when |
|---|---|---|
| **M0** | Repo scaffold, `go.mod` (module `github.com/Sten-Qy-Li/twokey`, binary from `./cmd/twokey`), LICENSE, NOTICE, README, `docs/SPEC.md`, `docs/DECISIONS.md`, `CLAUDE.md`, config loading + validation, `init`, `doctor` without `--smoke`, CI | CI green; `twokey doctor` runs on the owner's machine |
| **M1** | Log insertion, templates and rendering, schemas + validator, the gate, the state machine, git snapshot/invariant helpers, fake-agent runner, verify commands, push with checks, basic text answers for decisions and rulings, `render`, `unlock`, **stepper mode end-to-end with fake agents** (D22) | All unit and fake end-to-end tests green on Windows |
| **M2** | Real runners (Codex, Claude) in job objects, staging/blindness, timeouts and process-tree kill, Docker cleanup, retries with session resume, usage-limit detection, log labels from the reported model, `doctor --smoke`, `status`, `report` | **v0.1.0 (stepper):** one supervised real round on the owner's project, with their go-ahead |
| **M3** | Autopilot: all pauses, the full decisions/rulings UI, notifications (toast, then ntfy), auto-push without Enter, crash resume, keep-awake, final report + twokey log entry | **v0.2.0 (autopilot):** one full autopilot run that the owner watches |

**Timing:** The owner's next checkpoint is on Fri 20 Nov. Aim for v0.1 by Sun 15 Nov, so they can use stepper mode to prepare. Autopilot shouldn't be on the critical path for that checkpoint. If it isn't solid by 18 Nov, they keep using the stepper.

---

## 17. Out of scope for v0.x (possible later)

- Checking GitLab/GitHub pipeline status after a push (needs a token and an API client).
- Answering pauses from the phone.
- Other agents (Gemini etc.) or swapping roles. Keep the runner interface simple enough to allow it.
- Running several projects at once.
- A GUI or dashboard.
- Generating the requirements file.
