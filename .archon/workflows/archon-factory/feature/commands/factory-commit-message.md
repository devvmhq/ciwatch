---
description: Write the merge commit message that lands a finished change.
argument-hint: <dex task id>
---

The change below has passed review, a polish step for the review's minor findings, and the project's gate. Its branch holds one commit for each step (the tests, the implementation, the self-review, each review round, the polish), and it will land on the main branch with a merge commit. You're writing that merge commit's message: the one place that sums up the whole change.

The task, as dex JSON:

$task.output

The whole change, as a diff against the commit the work started from (a file whose diff is very large, such as a recorded fixture, is named at the end instead of shown):

$final-diff.output

The last adversarial review, which shipped it, with its minor findings and its backlog notes:

$review-loop.output

What the polish step fixed and what it left:

$polish.output

Read AGENTS.md's rules for commits and follow them. Unless it says otherwise, use Conventional Commits: a `type(scope): summary` line under 72 characters for the change as a whole, a blank line, then a body in plain, joined sentences saying what changed and why, wrapped at 72 characters. Then, only when there's something to list:

- a paragraph headed `Left from review:` listing each minor finding the polish step left, in a line or two with its file;
- a paragraph headed `Backlog:` listing the review's backlog notes, each in a line or two with why it's off the feature's path (its `why_off_path`), as possible future tasks.

Never put a task ID from dex or any other tracker in the message.

Write the message, and nothing else, to `$ARTIFACTS_DIR/commit-message.txt` with the Write tool. Then reply with exactly the same message and nothing before or after it: no preamble, no code fence, no notes. Your reply is shown to the human approving the merge as the message itself.
