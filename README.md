# ciwatch

ciwatch watches every GitHub Actions run for a commit until it finishes, and shows what failed. It's meant for the minutes after a push, when you'd otherwise keep `gh run watch` open in one tab and dig through `gh run view --log-failed` in another.

It isn't usable yet: the commands are being built one at a time.

## What it will do

- Find every workflow run for a commit, including runs that start a little after the push, and watch them until each completes.
- Report each workflow's and each job's conclusion, as a compact table or as JSON with `--json`.
- Keep watching through network errors, retrying with backoff.
- Notice a run that a newer push cancelled, say so and name the newer commit, and with `--follow` watch it instead.
- For a failed job, show the failing test's name and the lines around it, not the whole log.
- For a failed fuzz job, list the failing targets and fetch their crash inputs into `testdata/fuzz/<Target>/` when you ask.
- Offer to rerun a flaky job's failures.
- Exit 0 only when every run passed, with a different code for failed, cancelled, and couldn't check.

## Usage

```sh
ciwatch [--json] [--once] [--follow] <commit>
```

ciwatch polls until every run for the commit has completed, and keeps looking for new runs for 60 seconds after the push so runs that start late are included. If the commit's runs were created longer ago than that (a commit pushed before, or pushed again), it looks for 60 seconds from when ciwatch started instead, so the new push's runs are caught and an old commit waits that long once. It writes a progress line to stderr whenever a run changes, then prints the final table (or the `--json` document) to stdout. With `--once` it checks a single time instead, and exits 4 if runs are still going. A `gh` call that fails with a network error or a temporary GitHub error (HTTP 502, 503 or 504) is retried up to 8 times, waiting 1s, 2s, 4s and so on up to 30s between tries, and ciwatch says on stderr what it is waiting for; only when the retries run out does it exit 4. The exception is the jobs fetch: if fetching a run's jobs runs out of retries, ciwatch warns on stderr, shows `(jobs unavailable)` for that run, and keeps the exit code the runs' conclusions give rather than exiting 4.

When a cancelled run has a run of the same workflow, with the same event, on the same branch for a different commit that was created after it and no later than a minute after the cancelled run stopped, the first such run (found by the workflow's ID, among the runs created in that window) is the newer push that cancelled it, and ciwatch counts the run as cancelled by it (a run cancelled by hand, with a push only long after, stays plainly cancelled): its table row ends in `(superseded by <sha>)`, `--json` adds `superseded_by` to the run, and the last stderr line is a hint to rerun with `--follow`. With `--follow` ciwatch watches that commit instead, and keeps following if that one is cancelled by a still newer push, up to 20 commits (it warns on stderr if it stops there); the table, the exit code and the JSON `sha` are for the last commit, and `followed_from` lists the commits left behind. Without `--once`, ciwatch moves on to the newer commit only after every run of the current one has completed, and never goes back to a commit it has already watched (it says so on stderr when a cancelled run's newer commit is one it already watched). A cancelled run is not counted as superseded if the same commit has a later run of the same workflow (the commit was pushed again), nor if the look for the newer push got a full page of 20 runs inside its window, since the first of them may be missing. With `--once` each commit is checked once, and ciwatch follows as soon as it finds a cancelled run without waiting for the rest: it names the current commit's runs still in progress on stderr before moving on, and they are not in the table. A run cancelled with nothing newer behind it still just exits 3. If the look for a newer push runs out of retries, ciwatch warns on stderr and treats the run as plainly cancelled.

The `--json` fields and the exit codes are described in [docs/json.md](docs/json.md).

## Building

ciwatch needs [mise](https://mise.jdx.dev) and the [GitHub CLI](https://cli.github.com), logged in:

```sh
mise install
mise run build
```

The gate every change passes is `mise run fmt && mise run lint && mise run test`.

## How it's built

Each change starts as a dex task and is built by the [archon-factory](https://github.com/devvmhq/archon-factory) workflows, vendored in `.archon/workflows/archon-factory`. Those workflows design the change, wait for approval, write failing tests first, implement, review the change adversarially until it can ship, pass the gate, and wait for a final approval before committing. With [Archon](https://archon.diy) installed (`brew install coleam00/archon/archon`, then `archon provider add claude`) and the tools from `mise install`, a task runs with:

```sh
mise run factory -- feature <dex-id>
```
