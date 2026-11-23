# ciwatch

ciwatch watches every GitHub Actions run for a commit until it finishes, and shows what failed. It's meant for the minutes after a push, when you'd otherwise keep `gh run watch` open in one tab and dig through `gh run view --log-failed` in another.

It isn't usable yet: the commands are being built one at a time.

## What it will do

- Find every workflow run for a commit, including runs that start a little after the push, and watch them until each completes.
- Report each workflow's and each job's conclusion, as a compact table or as JSON with `--json`.
- Keep watching through network errors, retrying with backoff.
- For a cancelled run, name the first run after it of the same workflow on the same branch, for another commit, if there is one, and print the command to watch it.
- For a failed job, show the last 40 lines of each failed step, not the whole log, and with `--go-tests` the failing Go tests' names and the lines around them too.
- For a failed fuzz job, list the failing targets and fetch their crash inputs into `testdata/fuzz/<Target>/` when you ask.
- Offer to rerun a flaky job's failures.
- Exit 0 only when every run passed, with a different code for failed, cancelled, and couldn't check.

## Usage

```sh
ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] [--rerun-failed] <commit>
```

ciwatch polls until every run for the commit has completed, and keeps looking for new runs for 60 seconds after the push so runs that start late are included. If the commit's runs were created longer ago than that (a commit pushed before, or pushed again), it looks for 60 seconds from when ciwatch started instead, so the new push's runs are caught and an old commit waits that long once. It writes a progress line to stderr whenever a run changes, then prints the final table (or the `--json` document) to stdout. With `--once` it checks a single time instead, and exits 4 if runs are still going. A `gh` call that fails with a network error or a temporary GitHub error (HTTP 502, 503 or 504) is retried up to 8 times, waiting 1s, 2s, 4s and so on up to 30s between tries, and ciwatch says on stderr what it is waiting for; only when the retries run out does it exit 4. The exception is the jobs fetch: if fetching a run's jobs runs out of retries, ciwatch warns on stderr, shows `(jobs unavailable)` for that run, and keeps the exit code the runs' conclusions give rather than exiting 4.

For each cancelled run on a branch, ciwatch makes one more call, `gh run list --created ">=<its createdAt>"` for the same workflow (found by its ID) on the same branch, and takes the earliest run in the result that is for a different commit and was created strictly after the cancelled run. If there is one, the cancelled run's table row ends in `(newer run <id> for <short sha>)`, `--json` adds `newer_run` (`id` and `sha`) to the run, and the last stderr lines, one per such run in table order, say `ciwatch: run <id> (<workflow>) was cancelled; to watch the newer run: ciwatch <sha>`. ciwatch doesn't claim the newer run caused the cancellation, and doesn't watch it on its own. Otherwise the run is reported as plainly cancelled. Either way a cancelled run exits 3, unless another run failed, which exits 1. If the look for a newer run runs out of retries, ciwatch warns on stderr and reports the run as plainly cancelled. If the call returns 100 runs, the oldest may have been cut off, so ciwatch warns `can't name the run after run <id> (<workflow>): 100 or more runs since it` and leaves the run plainly cancelled. Timestamps are whole seconds, so a run of another commit created in the same second isn't named. `gh run list --created` needs a gh recent enough to have the flag; it was tested with gh 2.103.3.

For each failed run (`failure` or `timed_out`), ciwatch lists the run's artifacts once and takes those named `crash-<Target>` (a Go fuzz target, `Fuzz…`) as fuzz crash inputs. They are listed under the table as `<Target> (crash-<Target>, run <id>)`, `--json` gives them as `fuzz_crashes` on the run, and a stderr line says to rerun with `--fetch-crashes`. With `--fetch-crashes`, each is downloaded with `gh run download` into a temporary directory and its files are copied into `testdata/fuzz/<Target>/` beside the one `_test.go` file in the checkout that declares `func <Target>(f *testing.F)`. A file that already exists there is never overwritten; if every file was already there, the input is listed as `already present`. If no file or more than one declares the target, ciwatch warns and skips that download. When an input isn't fetched, the last line on stderr says what to do next. A failed listing or download is a warning and never changes the exit code. A run with more than 100 artifacts is checked only for the first 100.

When a run fails, the last stderr line (after the crash hint, if there is one) says to rerun with `--rerun-failed`. With that flag, ciwatch calls `gh run rerun <id> --failed` once for each run that ended in `failure` or `timed_out` (never a cancelled or passing run), then goes back to watching the commit until every rerun has a new attempt and every run has completed. A rerun keeps the run's ID and raises its attempt number, which ciwatch reads from `gh run list`; the progress lines add `(attempt N)` from the second attempt on, and `--json` gives `attempt` on each run. The table, the JSON and the exit code are of the new attempts only; the first attempt's logs, tests and crash inputs are not shown. Each run is rerun at most once per invocation, so a second failure is reported, not rerun, and so is a run first seen failing after the reruns. A rerun call that fails is a warning on stderr, is not retried, and ciwatch doesn't wait for a new attempt of that run: its original attempt is reported. `--rerun-failed` can't be combined with `--once`, which doesn't wait (exit code 2). It was tested with gh 2.103.3.

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
