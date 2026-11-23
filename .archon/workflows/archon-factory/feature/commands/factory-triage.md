---
description: Triage the review's backlog notes for the final review: task, note or drop, each ready to become a dex task.
argument-hint: <dex task id>
---

The change for this task has passed review, polish and the gate. The last adversarial review left these backlog notes, each with why it's off the approved feature's path:

$review-loop.output

The task, as dex JSON:

$task.output

Number the backlog notes 1, 2, 3… in the order given, and recommend one of these for each:

- **task**: worth doing as its own small dex task; a user would notice the difference.
- **note**: worth keeping in the merge message, but not worth a task now.
- **drop**: not worth keeping.

Give a one-sentence reason. For every note, whatever you recommend, write a dex task name (imperative, under 60 characters) and a short description with one or two draft acceptance cases (`AC-1: …`) and its non-goals, so the human can pick any of them. Read AGENTS.md if you need the project's terms.

Write the list with the Write tool to `$ARTIFACTS_DIR/triage.json`, as `{"items":[{"n":1,"note":"…","recommendation":"task","reason":"…","task_name":"…","task_description":"…"}]}`, with `"items":[]` when there are no backlog notes. Don't change any other file. Then reply with the same list for the human, one entry per note: `<n>. [<recommendation>] <note> — <reason> (task: <task_name>)`, or `No backlog notes.` The human creates tasks by writing `tasks: 1,3` (or `tasks: all`) in their approval comment.
