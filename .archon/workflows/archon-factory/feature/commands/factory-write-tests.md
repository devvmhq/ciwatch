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

Read AGENTS.md first and follow its conventions. Create and edit every file with the editor tools (Write, Edit), throwaway and generated files included: never write a file from the shell (`cat >`, `echo >`, `printf >`, `tee`, heredocs, `sed -i`, `perl`, `python`); a hook blocks those. The one exception is recording a fixture with a read-only `gh` command straight into `testdata/`. Golden files and fixtures come from real output, or are written by hand from it, never from running the code under test. Never read a recorded log (`testdata/`, `.log`) whole: grep it or read the lines you need.

Write at least one test for each of the design's acceptance cases, naming the case it covers (`AC-<n>`) in the test's name or a comment beside it, plus any test the approver asked for. Don't write tests for the design's non-goals. Each must fail now, because the behaviour it checks doesn't exist yet, and pass once the change is made. Test behaviour through the package's public surface, not its internals. Put fixtures in `testdata/`. When a test needs a symbol that doesn't exist yet, write the test against the name the design gives it and let it fail to compile; don't add the implementation, not even a stub.

Then run the project's tests and confirm that the new tests fail for the reason you expect. A separate check runs the suite after you, and it fails the run if no test files changed or if the suite passes.

The workflow commits each step on the run's branch. Write the commit message for these tests to `$ARTIFACTS_DIR/commit-tests.txt` with the Write tool, following AGENTS.md's rules for commits. Unless it says otherwise, use Conventional Commits: a `test(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences wrapped at 72 characters, saying which tests you added, which acceptance case each covers, and how each fails before the change. The workflow adds the approved acceptance cases and non-goals to this message itself, so don't copy them in. Never put a dex or other tracker task ID in it, and don't run `git commit` yourself.

Reply with the tests you wrote and how each one fails now.
