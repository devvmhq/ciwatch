---
description: Write the merge commit message that lands a finished change.
argument-hint: <dex task id>
---

The change below has passed review and the project's gate. Its branch holds one commit for each step (the tests, the implementation, the self-review, each review round), and it will land on the main branch with a merge commit. You're writing that merge commit's message: the one place that sums up the whole change.

The task, as dex JSON:

$task.output

The whole change, as a diff against the commit the work started from:

$final-diff.output

The last adversarial review, which shipped it:

$review-loop.output

Read AGENTS.md's rules for commits and follow them. Unless it says otherwise, use Conventional Commits: a `type(scope): summary` line under 72 characters for the change as a whole, a blank line, then a body in plain, joined sentences saying what changed and why, wrapped at 72 characters. If the last review left any findings (they're all minor, since it shipped), end the body with a paragraph headed `Left from review:` that lists each one in a line or two, with its file. Never put a task ID from dex or any other tracker in the message.

Write the message, and nothing else, to `$ARTIFACTS_DIR/commit-message.txt` with the Write tool. Then reply with exactly the same message and nothing before or after it: no preamble, no code fence, no notes. Your reply is shown to the human approving the merge as the message itself.
