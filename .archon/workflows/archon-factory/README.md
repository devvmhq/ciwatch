# archon-factory

An [Archon](https://archon.diy) workflow pack that takes one dex task from design to a change landed on the local main branch, the same way in every project. Each AI step does one job, and the checks between the steps are code, so a step that skips the protocol fails the run. Each step is committed on the run's branch, which lands with a merge commit, so the history keeps the failing tests, the implementation and every review round. A human approves the design and the finished change, and nothing is ever pushed.

## The workflows

**`feature`** builds a feature in a worktree of its own:

1. **Design** (Opus, read-only) from the dex task, with **acceptance cases** (`AC-1`, `AC-2`…: concrete inputs and their expected results) and **non-goals**, then a wait for your approval. Together they are the spec the change is built and reviewed against. A task that depends on guessing at open-ended input gets a first version that guesses at nothing, with the heuristic proposed as a separate, optional follow-up task. Your comment goes to the builder, and a rejection ends the run.
2. **Tests first** (Sonnet): at least one test per acceptance case. A check then fails the run if no test file changed or if the suite already passes. The tests commit records the approved spec, so every later step, and a `review` run that adopts the branch, reads the same one.
3. **Implement** and **self-review** (Sonnet), in the same session as the tests.
4. **Adversarial review** (Opus, read-only), at most 3 rounds, bounded by the spec. A blocker or major is something that breaks an acceptance case or the spec; anything outside the spec, rarer inputs included, is a **backlog** note for a possible future task and never blocks. Each round, a fresh reviewer gets the diff and the branch's history, and a fixer (Sonnet) fixes its blockers and majors when it doesn't ship. If a round's majors come from the previous round's fix, the loop stops early (**churn**) and asks you: reject to stop and redesign, or approve to carry on with them open. A run that reaches the cap fails for you to judge.
5. **Polish** (Sonnet): the review's minor findings, fixed inside the run and committed as their own step.
6. **The gate:** `mise run fmt`, `mise run lint` and `mise run test`.
7. **The merge message** (Sonnet), written from the whole change to the project's AGENTS.md rules, ending with the minor findings polish left (`Left from review:`) and the review's backlog notes (`Backlog:`).
8. **Your review:** the diff's stat, the merge message and the last review, then a wait for your approval. A fix task for the leftovers or the backlog happens only if you ask for one.
9. **Land:** the run's branch is merged into the main checkout's current branch with `git merge --no-ff`, and the dex task is completed with the merge commit. If the merge conflicts, it's aborted and the run says so, so you merge by hand.

Along the way, each stage commits what it changed on the run's branch, with a message its AI step writes to AGENTS.md's rules:

| Commit | After |
| --- | --- |
| `test(…)`: the failing tests | the red check (this commit fails the tests on purpose) |
| the implementation | implement |
| the self-review's fixes | self-review, if it changed anything |
| `fix(…)`: one per review round | each fixer round, listing every blocker and major and what was done about it, or why it was disputed; committed even when nothing changed |
| `fix(…)`: polish | polish, if it changed anything |
| `style: apply mise run fmt` | the gate, if the formatter changed anything |

The merge commit keeps the main branch's first-parent history passing the gate at every commit, so `git bisect --first-parent` skips the red test commits.

Each round's reviewer gets the branch's commit messages, so it sees the spec and every earlier round's findings with the answers to them. It checks that those are really fixed before looking further, and doesn't raise an answered finding again unless the answer is wrong.

**`review`** continues a `feature` or `fix` run whose review hit its cap. It adopts that run's branch in a new run (`archon workflow run --adopt`) and runs up to 3 more review rounds, polish, the gate, your review and the landing merge, like the end of `feature`. An adopted run reads project workflows from the adopted branch, whose vendored copy may predate `review`, so `factory review` runs the installed pack's `devvmhq/archon-factory:review`; the pack must be installed with `archon plugin add`.

**`fix`** fixes a bug the same way, without the design step: the spec is the task's description of the bug and the test that reproduces it. It writes that test and checks that it fails, fixes the bug, runs at most 2 review rounds (with the same churn stop), polish and the gate, committing each step, then waits for your review like `feature` before landing, so no change reaches the main branch unseen.

## Lanes

Each run records its task's lane in the project's `.dex/lanes.json`, keyed by the dex task ID, with the run ID and the time. The lanes are `design`, `design-approval`, `build`, `review`, `polish`, `gate`, `human-review` and `done` for `feature`, and `reproduce`, `build`, `review`, `polish`, `gate`, `human-review` and `done` for `fix`. A run that fails leaves its task in the lane where it stopped. The lanes live beside dex's own file because dex drops metadata it doesn't know the next time it writes a task.

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
.archon/workflows/archon-factory/bin/factory review <dex-id> <run-id>    # after a run hits its review cap
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

## Before a push: squash each task

Locally, each task lands as its stack and a merge commit, so you can review it step by step. The pushed history has one commit per task instead. On a clean checkout of the branch you're about to push, run:

```sh
.archon/workflows/archon-factory/bin/squash     # squashes everything past the branch's upstream
git log --oneline @{upstream}..                 # check it, then push
```

Each task's merge commit since the upstream becomes one ordinary commit with the same files and the merge's message, with the stack's commit subjects listed under `Built in steps:` before the trailers. Other commits are kept as they are. The old head is saved under `refs/factory/pre-squash/`, each task's branch keeps its stack, and the dex tasks linked to the merges are relinked to the new commits. `squash` never pushes.

## Leftovers

A finished run leaves its worktree under `~/.archon/workspaces/<project>/worktrees/`, along with its branch. Archon's bundled `archon-worktree-cleanup` workflow looks like the way to remove them, but the pack hasn't tried it yet.

## Layout

| Path | What |
| --- | --- |
| `archon-plugin.json` | the pack's manifest: its name and its two entrypoints |
| `feature/`, `fix/`, `review/` | each workflow's YAML, its prompts (`commands/`) and its script (`scripts/factory.ts`) |
| `.shared/factory.ts` | the steps that aren't AI: dex, lanes, the fail-first check, the diff, the gate, each stage's commit and the landing merge |
| `bin/factory` | starts a run for a dex task |
| `bin/squash` | squashes each task's stack before a push (`.shared/squash.ts`) |
| `bin/check` | the pack's own check (`mise run check`): the tests, the copies, shellcheck and Archon's validation |

A packaged workflow finds prompts and scripts only in its own folder, so `feature/`, `fix/` and `review/` each hold a copy of `scripts/factory.ts` and of the three prompts they share. `bin/check` fails when the copies differ.
