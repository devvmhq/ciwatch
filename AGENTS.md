# AGENTS.md

ciwatch watches every GitHub Actions run for a commit until it finishes, and shows what failed: each workflow's and job's conclusion, the failing tests' names and the lines around them, fuzz crash inputs, and an exit code that says whether everything passed. It's a Go CLI that talks to GitHub only by running `gh` with `--json`. Read [README.md](README.md) for what it does.

## Layout

| Path | What |
| --- | --- |
| `cmd/ciwatch/` | the entry point; the only `os.Exit` and the only use of the real stdin, stdout and stderr |
| `internal/cli/` | the command line: arguments, output (a compact table, or `--json`), exit codes |
| `testdata/` | recorded `gh` output that the tests feed in |
| `mise.toml`, `.golangci.yml` | pinned tools, tasks and linters |

## Tasks

`mise run <task>`: `fmt`, `lint`, `test`, `build`.

## Rules

- **The gate:** `mise run fmt && mise run lint && mise run test`, all passing, before starting a task and before marking it done; a gate failing at the start gets fixed first.
- **Fail-first tests:** write the test for a change first and see it fail for the right reason, then make it pass.
- **Ask first** before anything outward-facing (pushing, publishing, posting) and before deleting anything you didn't create.
- **A task that turns out wrong or incomplete:** say so and propose the change; don't quietly do something else.

## Go conventions

- **Fail by returning:** functions return errors; `cli.Run` turns them into a message on stderr and an exit code, and only `cmd/ciwatch` exits.
- **Exit codes:** 0 only when every run passed; a distinct non-zero code each for failed, cancelled, couldn't check, and usage (2). They're constants in `internal/cli`.
- **Streams and context are passed in:** functions write to the `io.Writer` they're given and take their caller's `ctx`; forbidigo enforces both.
- **`gh` is a command, never a library:** every call goes through one runner type with a field tests replace, always with `--json` and the fields named. Test seams are fields, never package variables.
- **Time is a seam too:** waiting and backoff go through a clock the tests control, so no test sleeps on the real clock.
- **Dependencies:** the standard library only. Adding one: ask.
- **Every `//nolint`** names its linters and says why.

## Testing

- **Tests feed recorded `gh` output from `testdata/`**, captured from real runs, and never call GitHub.
- **Test behaviour through each package's API**, with table tests next to the code.

## Commits and tasks

- **One commit when each task is complete**, after the gate passes, with whatever `mise run fmt` changed in it. Conventional Commits: `type(scope): summary`, types `feat`, `fix`, `docs`, `test`, `refactor`, `build`, `ci`, `chore`.
- **Task IDs from any tracker never go in commit messages, code comments or docs.**
- **AGENTS.md stays short:** one line per path or rule.
