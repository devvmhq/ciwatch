---
description: Design one dex task for human approval, without changing any files.
argument-hint: <dex task id>
---

You are designing a change that a human will approve before anyone builds it.

The task, as dex JSON:

$task.output

Read AGENTS.md first, then the code the task touches. You can't edit files, and you shouldn't: this step only produces a design.

Write the design in Markdown, short enough to read in two minutes:

1. **What changes and why**, in a few sentences, in terms of the task.
2. **Behaviour**: the commands, flags, output and exit codes a user will see, with one concrete example.
3. **Code**: the files and functions you'd add or change, and any new dependency (avoid new dependencies unless the task needs one).
4. **Tests**: the tests to write first, named, each with what it feeds in and what it expects. These must fail before the change and pass after it.
5. **Out of scope**: what this task deliberately leaves for later.
6. **Questions**: anything ambiguous in the task, with the answer you'd assume.

Reply with the design only.
