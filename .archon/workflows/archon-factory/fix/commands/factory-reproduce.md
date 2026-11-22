---
description: Reproduce a bug with a failing test, without fixing it.
argument-hint: <dex task id>
---

You are reproducing a bug before anyone fixes it.

The task, as dex JSON:

$task.output

Read AGENTS.md first, then find the code at fault. Write the smallest test that fails because of the bug and will pass once it's fixed. Use recorded inputs in `testdata/` where the bug depends on real data. Don't fix the bug or change any non-test code.

Run the project's tests and confirm that the new test fails for the reason the task describes. A separate check runs the suite after you, and it fails the run if no test files changed or if the suite passes.

Reply with the test, how it fails, and the cause of the bug as you understand it.
