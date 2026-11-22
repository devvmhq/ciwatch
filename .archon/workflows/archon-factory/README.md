# archon-factory

An [Archon](https://archon.diy) workflow pack that takes one dex task from design to a local commit, the same way in every project. Each AI step does one job, and the checks between the steps are code, so a step that skips the protocol fails the run. A human approves the design and the finished change, and nothing is ever pushed.

## The workflows

**`feature`** builds a feature in a worktree of its own:

1. **Design** (Opus, read-only) from the dex task, then a wait for your approval. Your comment goes to the builder, and a rejection ends the run.
2. **Tests first** (Sonnet): the tests the design names. A check then fails the run if no test file changed or if the suite already passes.
3. **Implement** and **self-review** (Sonnet), in the same session as the tests.
4. **Adversarial review** (Opus, read-only), at most 3 rounds. Each round, a fresh reviewer gets the diff and returns `ship` with findings, and a fixer (Sonnet) acts on them when it's `false`. A run that reaches the cap fails for you to judge. Minor findings don't block, so read them at the final review.
5. **The gate:** `mise run fmt`, `mise run lint` and `mise run test`.
6. **The commit message** (Sonnet), written from the final diff to the project's AGENTS.md rules.
7. **Your review:** the diff's stat, the commit message and the last review, then a wait for your approval.
8. **Commit and fast-forward:** one commit on the run's branch, then the main checkout's current branch is fast-forwarded to it, and the dex task is completed with the commit. If the fast-forward can't happen, the run says so and you merge by hand.

**`fix`** fixes a bug the same way, without the design step or your approvals. It writes a test that reproduces the bug and checks that it fails, fixes the bug, runs at most 2 review rounds, passes the gate and commits.

## Lanes

Each run records its task's lane in the project's `.dex/lanes.json`, keyed by the dex task ID, with the run ID and the time. The lanes are `design`, `design-approval`, `build`, `review`, `gate`, `human-review` and `done` for `feature`, and `reproduce`, `build`, `review`, `gate` and `done` for `fix`. A run that fails leaves its task in the lane where it stopped. The lanes live beside dex's own file because dex drops metadata it doesn't know the next time it writes a task.

## Using it in a project

The pack is meant to be vendored: installed once, then copied into the project and committed, so a clone has the exact workflows its commits were built with.

```sh
archon plugin add devvmhq/archon-factory@v0.1.0
archon plugin copy devvmhq/archon-factory    # writes .archon/workflows/archon-factory/
```

The project then needs:

- **A git remote with its default branch pushed.** Archon creates each run's worktree only when it can find the base branch on the remote; runs never push to it.
- **These tools in its `mise.toml`**, beside the gate's own: `bun = "1.4.5"` for the pack's script steps and `"npm:@zeeg/dex" = "0.16.0"` for the tasks. Archon itself comes from `brew install coleam00/archon/archon`, with `archon provider add claude`.
- **`fmt`, `lint` and `test` tasks in `mise.toml`**, which make up the gate. The fail-first check counts Go `_test.go` files, `.test.`/`.spec.` files, Python `test_*.py` files and anything under `testdata/`, `test/`, `tests/` or `__tests__/` as tests.
- **`AGENTS.md`** with the project's conventions, commits included. Every AI step reads it.
- **`.dex/` and `.archon/.env` in `.gitignore`.**

Commit the copy, along with a line in `.archon/config.yaml` or the commit message saying which tag it came from, since `plugin copy` doesn't record it. To upgrade, run `archon plugin update devvmhq/archon-factory@<tag>`, delete the copy, copy it again, and review the diff.

## Running a task

From the project's checkout:

```sh
.archon/workflows/archon-factory/bin/factory feature <dex-id>    # or: ... fix <dex-id>
```

`factory` starts the run in the background from the branch you're on, unpushed commits included, and prints its run ID. To follow and answer it:

```sh
archon workflow attention                       # what's waiting for you
archon workflow approve <run> "comment"         # or: archon workflow reject <run> "reason"
archon workflow logs <run> --follow
archon workflow get <run> --json                # state; .status, not the exit code
archon workflow get <run> --verbose             # includes the run's cost
```

`archon workflow approve` continues the run in the terminal you ran it in. `archon workflow wait` exits 0 even when the run fails, so read `status` from `archon workflow get --json` instead.

A finished run leaves its worktree under `~/.archon/workspaces/<project>/worktrees/`, along with its branch. Archon's bundled `archon-worktree-cleanup` workflow looks like the way to remove them, but the pack hasn't tried it yet.

## Layout

| Path | What |
| --- | --- |
| `archon-plugin.json` | the pack's manifest: its name and its two entrypoints |
| `feature/`, `fix/` | each workflow's YAML, its prompts (`commands/`) and its script (`scripts/factory.ts`) |
| `.shared/factory.ts` | the steps that aren't AI: dex, lanes, the fail-first check, the diff, the gate and the commit |
| `bin/factory` | starts a run for a dex task |
| `bin/check` | the pack's own check (`mise run check`): the tests, the copies, shellcheck and Archon's validation |

A packaged workflow finds prompts and scripts only in its own folder, so `feature/` and `fix/` each hold a copy of `scripts/factory.ts` and of the three prompts they share. `bin/check` fails when the copies differ.
