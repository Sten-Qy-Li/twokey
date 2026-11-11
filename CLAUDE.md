# CLAUDE.md

Context for AI coding sessions on `twokey`. Read `docs/SPEC.md` (what to build) and `docs/DECISIONS.md` (why it is built this way) before changing behavior. The milestones are in SPEC section 16.

## Hard rules

### Privacy (the repo is public)
Never commit:
- personal names, emails, OS usernames or home-directory paths;
- course URLs, team names, requirement files, log contents or GitLab URLs from the owner's projects;
- tokens or any credential.

The owner's GitHub handle may appear only in the Go module path (and import paths) and the README. The SPEC says "the owner". Project details are used only at runtime, from each project's own git-ignored config.
Commits use the repo-local git identity (GitHub handle + GitHub noreply email). Don't change it. Commit messages end with the `Co-Authored-By: Claude …` trailer.

### Never without the owner's OK
- A real agent run, or `twokey doctor --smoke` (it makes real calls and spends the owner's usage).
- Opening `~/.codex/auth.json` or any Claude credential file. Never.
- Final release tags (`v0.1.0`, ...), GitHub repo settings, force-pushing, deleting anything outside this repository.
- Installing anything on the owner's machine outside this repository.

Standing OK: push to `main`, and push `-rc` tags once CI is green.

### Don't guess facts
CLI flags, versions and pins must be checked before use (`codex exec --help`, `codex exec resume --help`, `claude --help`, the module proxy). If an installed CLI disagrees with the SPEC, the CLI wins: add a `docs/DECISIONS.md` entry, update the SPEC, and tell the owner. New tool and module versions must be at least 7 days old (D2).

### Tests use fakes only
Tests use fake agents and throwaway git repositories with a local bare remote (`internal/testutil`). They never call a real agent CLI or Docker, and never read or write the developer's home, git config or projects: use `t.TempDir()`, `t.Setenv` and `testutil.IsolateGit`. No real agent calls in CI, ever.

## Keep the docs current
When a decision changes behavior, update `docs/SPEC.md` and add a short entry to `docs/DECISIONS.md` (what and why). `assets/config.toml`, `config.Default()` and SPEC section 11 must match; tests check this.

## Language for users
All text shown to users (messages, errors, prompts, README) is plain English: short sentences, one idea per line. Labels are ASCII: `[OK]`, `[WARN]`, `[FAIL]`, `[PAUSE]`, `[..]`. Every `[FAIL]` says what to do next. No stack traces or raw Go errors on screen.

## Build and test

```sh
go build ./...
go vet ./...
go test ./...
golangci-lint run
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go build -o bin/twokey.exe ./cmd/twokey   # local build; bin/ is git-ignored
```

## Conventions
- Pure Go, `CGO_ENABLED=0`. Runtime dependencies: `golang.org/x/sys` and `github.com/BurntSushi/toml` only. Ask before adding another.
- Pure logic (the gate, config validation, log insertion, template rendering, help-text parsing) lives in functions without I/O and gets table-driven tests. OS calls stay in thin wrappers behind interfaces (`proc.Runner`).
- External commands go through `proc.Runner`; git goes through `internal/gitx`.
- Data files (config template, prompt templates, schemas) live in `assets/` and are embedded by the root package (D27).
- GitHub Actions are pinned to full commit SHAs, with the tag in a comment.
