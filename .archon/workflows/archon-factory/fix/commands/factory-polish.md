---
description: Fix the minor findings the shipping review left, then stop.
argument-hint: <dex task id>
---

The adversarial review has finished with this change. Its last verdict, with the findings it left:

$review-loop.output

Usually the reviewer shipped and only minor findings are left. If instead it stopped for churn (`churn` is true) and the human chose to carry on, leave its blocker and major findings alone: they go to the human's final review as they are.

Read AGENTS.md, then fix the minor findings, and only those. Keep each fix small and inside the approved spec. Don't act on the `backlog` notes; they're for future tasks. Edit files with the editor tools, not `sed`, `perl`, `python` or heredocs. If a finding would take more than a small change, or you think it's wrong, leave it and say why.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't run `git commit`; the workflow commits this step on the run's branch. If you changed anything, write the commit message to `$ARTIFACTS_DIR/commit-polish.txt` with the Write tool, following AGENTS.md's rules for commits (unless it says otherwise, Conventional Commits: a `type(scope): summary` line under 72 characters, usually `fix(scope): polish from review`, a blank line, then a body wrapped at 72 characters listing each finding you fixed). Never put a dex or other tracker task ID in it. If you changed nothing, write no message.

Reply with a short list: each minor finding, and whether you fixed it or left it and why. The merge message carries what you left.
