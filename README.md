# ciwatch

ciwatch watches every GitHub Actions run for a commit until it finishes, and shows what failed. It's meant for the minutes after a push, when you'd otherwise keep `gh run watch` open in one tab and dig through `gh run view --log-failed` in another.

It isn't usable yet: the commands are being built one at a time.

## What it will do

- Find every workflow run for a commit, including runs that start a little after the push, and watch them until each completes.
- Report each workflow's and each job's conclusion, as a compact table or as JSON with `--json`.
- Keep watching through network errors, retrying with backoff.
- Notice a run that a newer push cancelled, say so, and offer to watch the newer commit.
- For a failed job, show the failing test's name and the lines around it, not the whole log.
- For a failed fuzz job, list the failing targets and fetch their crash inputs into `testdata/fuzz/<Target>/` when you ask.
- Offer to rerun a flaky job's failures.
- Exit 0 only when every run passed, with a different code for failed, cancelled, and couldn't check.

## Building

ciwatch needs [mise](https://mise.jdx.dev) and the [GitHub CLI](https://cli.github.com), logged in:

```sh
mise install
mise run build
```

The gate every change passes is `mise run fmt && mise run lint && mise run test`.
