---
description: Review and tidy your own implementation before the adversarial review.
argument-hint: <dex task id>
---

Review your own change as if someone else wrote it, before an adversarial reviewer sees it. Read the whole diff (`git add --intent-to-add --all && git diff HEAD`) and check:

- It does what the approved design says, and nothing it leaves out of scope.
- Errors are handled and reported with enough context to act on; nothing is ignored.
- The tests cover the failure paths, not only the happy path.
- Names, comments and docs read clearly and match the code. Remove dead code and debugging output.
- AGENTS.md's conventions are followed.

Fix what you find, then run `mise run fmt`, `mise run lint` and `mise run test` and leave them passing. Don't commit.

Reply with what you found and fixed, or say that you found nothing.
