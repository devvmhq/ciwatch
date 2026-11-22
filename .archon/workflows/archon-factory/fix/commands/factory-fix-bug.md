---
description: Fix a bug that a failing test reproduces.
argument-hint: <dex task id>
---

Your test reproduces the bug:

$red.output

Now fix the cause, not the symptom, so the test passes. Follow AGENTS.md. Don't weaken or delete the test. If the same cause can break other paths, add a test for each.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't commit.

Reply with the cause and the fix.
