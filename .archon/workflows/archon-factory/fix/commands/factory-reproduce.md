---
description: Reproduce a bug with a failing test, without fixing it.
argument-hint: <dex task id>
---

You are reproducing a bug before anyone fixes it.

The task, as dex JSON:

$task.output

Read AGENTS.md first, then find the code at fault. Write the smallest test that fails because of the bug and will pass once it's fixed; it, with the task's description of the bug, is the spec the fix is reviewed against. Use recorded inputs in `testdata/` where the bug depends on real data. Don't fix the bug or change any non-test code. Create and edit every file with the editor tools (Write, Edit), throwaway and generated files included: never write a file from the shell (`cat >`, `echo >`, `printf >`, `tee`, heredocs, `sed -i`, `perl`, `python`); a hook blocks those. The one exception is recording a fixture with a read-only `gh` command straight into `testdata/`. Fixtures come from real output, never from running the code under test. Never read a recorded log (`testdata/`, `.log`) whole: grep it or read the lines you need.

Run the project's tests and confirm that the new test fails for the reason the task describes. A separate check runs the suite after you, and it fails the run if no test files changed or if the suite passes.

The workflow commits each step on the run's branch. Write the commit message for this test to `$ARTIFACTS_DIR/commit-reproduce.txt` with the Write tool, following AGENTS.md's rules for commits. Unless it says otherwise, use Conventional Commits: a `test(scope): summary` line under 72 characters, a blank line, then a body in plain, joined sentences wrapped at 72 characters, saying what the bug is and how the test shows it. Never put a dex or other tracker task ID in it, and don't run `git commit` yourself.

Reply with the test, how it fails, and the cause of the bug as you understand it.
