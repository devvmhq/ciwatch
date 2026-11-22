---
description: Implement an approved design until its failing tests pass.
argument-hint: <dex task id>
---

The tests you wrote fail as expected:

$red.output

Now implement the approved design so they pass. Follow AGENTS.md. Keep to the design and the approver's comment; if you have to depart from either, say where and why in your reply.

- Don't weaken, skip or delete a test to make it pass. If a test is wrong, fix it and say so.
- Add tests for any edge case you find while implementing.
- Run `mise run fmt`, `mise run lint` and `mise run test` and leave them all passing.
- Don't run `git commit`; the workflow commits each step on the run's branch. Write the commit message for the implementation to `$ARTIFACTS_DIR/commit-implement.txt` with the Write tool, following AGENTS.md's rules for commits (unless it says otherwise, Conventional Commits: a `type(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences wrapped at 72 characters, saying what changed and why). Never put a dex or other tracker task ID in it.

Reply with what you changed, file by file, and any departures from the design.
