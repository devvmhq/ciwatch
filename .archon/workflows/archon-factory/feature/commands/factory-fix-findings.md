---
description: Fix an adversarial reviewer's findings.
argument-hint: <dex task id>
---

An adversarial reviewer did not ship this change. Their verdict and findings:

$review.output

Read AGENTS.md, then fix every blocker and major finding, and every minor one that is cheap to fix. For a bug, write a test that fails because of it first, then fix it. If you think a finding is wrong, don't change the code for it; explain why in your reply so the next reviewer sees it.

Run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't commit.

Reply with each finding and what you did about it.
