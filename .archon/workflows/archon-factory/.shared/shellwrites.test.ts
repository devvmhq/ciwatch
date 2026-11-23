import { describe, expect, test } from "bun:test";

import { shellWrite } from "./shellwrites.ts";

describe("shellWrite", () => {
  const blocked: [string, string][] = [
    ["cat > out.txt <<'EOF'\nx\nEOF", "a heredoc"],
    ["cat <<EOF > a.go\nx\nEOF", "a heredoc"],
    ["echo hi > notes.md", "a redirect into notes.md"],
    ["printf '%s' x >> log.txt", "a redirect into log.txt"],
    ["go test ./... 2> errs.txt", "a redirect into errs.txt"],
    ["go run ./cmd/gen > testdata/golden.json", "a redirect into testdata/golden.json"],
    ["ls | tee files.txt", "tee into files.txt"],
    ["sed -i '' 's/a/b/' x.go", "sed -i"],
    ["perl -pi -e 's/a/b/' x.go", "perl -i"],
  ];
  for (const [command, reason] of blocked) {
    test(`blocks ${command.split("\n")[0]}`, () => expect(shellWrite(command)).toBe(reason));
  }

  const allowed = [
    "go test ./... 2>&1 | tail -5",
    "mise run lint > /dev/null 2>&1",
    "grep -E 'a>b|c' x.go",
    'echo "x > y"',
    "git log --format='%h > %s'",
    "cat <<< 'here string'",
    "gh run view 123 --log-failed --job 456 > testdata/logs/run.log",
    "gh api repos/o/r/actions/runs/1/artifacts > testdata/gh/artifacts.json",
    "diff <(sort a) <(sort b)",
    "cmd >&2",
  ];
  for (const command of allowed) {
    test(`allows ${command}`, () => expect(shellWrite(command)).toBeNull());
  }

  test("a gh recording outside testdata is still blocked", () => {
    expect(shellWrite("gh run view 1 > /tmp/run.log")).toBe("a redirect into /tmp/run.log");
  });
});
