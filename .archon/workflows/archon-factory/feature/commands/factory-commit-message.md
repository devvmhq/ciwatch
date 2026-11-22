---
description: Write the commit message for a finished change.
argument-hint: <dex task id>
---

The change below has passed review and the project's gate, and will be committed as one commit.

The task, as dex JSON:

$task.output

The change, as a diff against the commit the work started from:

$final-diff.output

Read AGENTS.md's rules for commits and follow them. Unless it says otherwise, use Conventional Commits: a `type(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences saying what changed and why, wrapped at 72 characters. Never put a task ID from dex or any other tracker in the message.

Write the message, and nothing else, to `$ARTIFACTS_DIR/commit-message.txt` with the Write tool. Then reply with exactly the same message and nothing before or after it: no preamble, no code fence, no notes. Your reply is shown to the human approving the commit as the message itself.
