---
description: Write the failing tests for an approved design, before any implementation.
argument-hint: <dex task id>
---

You are writing the tests for an approved change, before the change exists.

The task, as dex JSON:

$task.output

The approved design:

$design.output

The approver's comment (follow it where it differs from the design):

$approve-design.output.text

Read AGENTS.md first and follow its conventions.

Write the tests the design names, plus any the approver asked for. Each must fail now, because the behaviour it checks doesn't exist yet, and pass once the change is made. Test behaviour through the package's public surface, not its internals. Put fixtures in `testdata/`. When a test needs a symbol that doesn't exist yet, write the test against the name the design gives it and let it fail to compile; don't add the implementation, not even a stub.

Then run the project's tests and confirm that the new tests fail for the reason you expect. A separate check runs the suite after you, and it fails the run if no test files changed or if the suite passes.

Reply with the tests you wrote and how each one fails now.
