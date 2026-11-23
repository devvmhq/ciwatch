---
description: Fix a bug that a failing test reproduces.
argument-hint: <dex task id>
---

Your test reproduces the bug:

$red.output

Now fix the cause, not the symptom, so the test passes. Follow AGENTS.md. Edit files with the editor tools, not `sed`, `perl`, `python` or heredocs. Don't weaken or delete the test, and keep the fix to the bug the task describes; note other paths the same cause might break in your reply rather than widening the fix.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't run `git commit`; the workflow commits each step on the run's branch. Write the commit message for the fix to `$ARTIFACTS_DIR/commit-fix.txt` with the Write tool, following AGENTS.md's rules for commits (unless it says otherwise, Conventional Commits: a `fix(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences wrapped at 72 characters, saying what caused the bug and how it's fixed). Never put a dex or other tracker task ID in it.

Reply with the cause and the fix.
