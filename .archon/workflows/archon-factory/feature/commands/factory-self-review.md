---
description: Review and tidy your own implementation before the adversarial review.
argument-hint: <dex task id>
---

Review your own change as if someone else wrote it, before an adversarial reviewer sees it. Read the whole diff (`git add --intent-to-add --all && git diff HEAD`) and check:

- It does what the approved design says, and nothing it leaves out of scope.
- Errors are handled and reported with enough context to act on; nothing is ignored.
- The tests cover the failure paths, not only the happy path.
- Names, comments and docs read clearly and match the code. Remove dead code and debugging output.
- AGENTS.md's conventions are followed.

Fix what you find, then run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't run `git commit`; the workflow commits each step on the run's branch. If you changed anything, write the commit message for your fixes to `$ARTIFACTS_DIR/commit-self-review.txt` with the Write tool, following AGENTS.md's rules for commits (unless it says otherwise, Conventional Commits: a `type(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences wrapped at 72 characters, saying what the self-review found and changed). Never put a dex or other tracker task ID in it. If you changed nothing, write no message.

Reply with what you found and fixed, or say that you found nothing.
