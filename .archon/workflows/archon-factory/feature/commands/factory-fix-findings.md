---
description: Fix an adversarial reviewer's findings.
argument-hint: <dex task id>
---

An adversarial reviewer did not ship this change. Their verdict and findings:

$review.output

Read AGENTS.md, then fix every blocker and major finding, and every minor one that is cheap to fix. For a bug, write a test that fails because of it first, then fix it. If you think a finding is wrong, don't change the code for it; explain why in your reply so the next reviewer sees it.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't run `git commit`; the workflow commits this review round on the run's branch, even if you changed nothing, so the review stays in the history.

Write the round's commit message to `$ARTIFACTS_DIR/commit-review.txt` with the Write tool, following AGENTS.md's rules for commits. Unless it says otherwise, use Conventional Commits: a `type(scope): summary` line under 72 characters (usually `fix(scope): address review findings`), a blank line, then a body wrapped at 72 characters that lists every finding, each with its severity, file and problem in a few words, followed by what you did about it, or why you think it's wrong. Never put a dex or other tracker task ID in it.

Reply with each finding and what you did about it.
