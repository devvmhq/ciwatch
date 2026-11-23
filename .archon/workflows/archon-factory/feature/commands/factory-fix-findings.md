---
description: Fix an adversarial reviewer's findings.
argument-hint: <dex task id>
---

An adversarial reviewer did not ship this change. Their verdict and findings:

$review.output

Read AGENTS.md, then fix every blocker and major finding. Leave the minor findings, which a polish step handles once the reviewer ships, and don't act on the backlog notes, which are for future tasks. Keep each fix to what the finding needs: don't widen the change beyond the approved spec. For a bug, write a test that fails because of it first, see it fail, then fix it. Create and edit every file with the editor tools (Write, Edit), throwaway files included, never from the shell (`cat >`, `echo >`, `tee`, heredocs, `sed -i`, `perl`, `python`); a hook blocks those. Never generate a golden file or fixture by running the code under test, and never read a recorded log whole: grep it or read the lines you need. If you think a finding is wrong, don't change the code for it; explain why in the commit message so the next reviewer sees it.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't run `git commit`; the workflow commits this review round on the run's branch, even if you changed nothing, so the review stays in the history.

Write the round's commit message to `$ARTIFACTS_DIR/commit-review.txt` with the Write tool, following AGENTS.md's rules for commits. Unless it says otherwise, use Conventional Commits: a `type(scope): summary` line under 72 characters (usually `fix(scope): address review findings`), a blank line, then a body wrapped at 72 characters that lists every blocker and major finding, each with its severity, file and problem in a few words, followed by what you did about it, or why you think it's wrong. Mention the minor findings and the backlog notes only by count; they're recorded elsewhere. Never put a dex or other tracker task ID in it.

Reply with each finding and what you did about it.
