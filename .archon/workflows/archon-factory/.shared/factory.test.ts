import { $ } from "bun";
import { afterEach, describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { checkpoint, isTestPath, setLane, trimDiff } from "./factory.ts";

describe("trimDiff", () => {
  const part = (path: string, body: string) =>
    `diff --git a/${path} b/${path}\nindex 1..2 100644\n--- a/${path}\n+++ b/${path}\n@@ -0,0 +1 @@\n+${body}\n`;

  const limits = { longestLine: 100, fileBytes: 1000 };

  test("keeps every file's part when none looks like data or is huge", () => {
    const diff = part("a.go", "package a") + part("b.go", "package b");
    expect(trimDiff(diff, limits)).toBe(diff.trimEnd());
  });

  test("keeps a long file of short lines, such as tests", () => {
    const tests = part("a_test.go", Array(20).fill("x := 1").join("\n+"));
    expect(trimDiff(tests, limits)).toBe(tests.trimEnd());
  });

  test("names a file with a long line instead of showing it, and keeps the rest", () => {
    const data = part("testdata/run.json", "x".repeat(500));
    const diff = part("a.go", "package a") + data + part("b.go", "package b");
    const got = trimDiff(diff, limits);
    expect(got).toContain("+package a");
    expect(got).toContain("+package b");
    expect(got).not.toContain("xxxx");
    expect(got).toContain(`- testdata/run.json (${data.length} bytes of diff)`);
    expect(got).toContain("read the file if it matters");
  });

  test("names a file whose diff is over the byte limit, even with short lines", () => {
    const huge = part("gen.go", Array(200).fill("x := 1").join("\n+"));
    expect(trimDiff(huge, limits)).toContain("- gen.go (");
  });

  test("gives an empty diff for no changes", () => {
    expect(trimDiff("", limits)).toBe("");
  });
});

// A git repo with one commit, as the current directory, and an artifacts folder.
async function repo(): Promise<{ dir: string; artifacts: string }> {
  const dir = await mkdtemp(join(tmpdir(), "factory-repo-"));
  const artifacts = join(dir, ".artifacts");
  await mkdir(artifacts);
  process.chdir(dir);
  await $`git init --quiet -b main && git config user.email t@example.com && git config user.name t`.quiet();
  await writeFile(".gitignore", ".artifacts/\n");
  await writeFile("a.go", "package a\n");
  await $`git add --all && git commit --quiet -m init -m "Co-Authored-By: Someone <s@example.com>"`.quiet();
  return { dir, artifacts };
}

async function log(): Promise<string[]> {
  return (await $`git log ${"--format=%s"}`.quiet().text()).trim().split("\n");
}

describe("checkpoint", () => {
  const start = process.cwd();
  afterEach(() => process.chdir(start));

  test("commits a stage's changes with its message, and removes the message", async () => {
    const { artifacts } = await repo();
    await writeFile("a_test.go", "package a\n");
    await writeFile(join(artifacts, "commit-tests.txt"), "test(a): a failing test\n");
    expect(await checkpoint(artifacts, "tests")).toMatch(/^[0-9a-f]+ test\(a\): a failing test$/);
    expect(await log()).toEqual(["test(a): a failing test", "init"]);
    expect(existsSync(join(artifacts, "commit-tests.txt"))).toBe(false);
  });

  test("commits nothing for a stage that changed nothing", async () => {
    const { artifacts } = await repo();
    await writeFile(join(artifacts, "commit-self-review.txt"), "refactor(a): tidy\n");
    expect(await checkpoint(artifacts, "self-review")).toBe("self-review: nothing to commit");
    expect(await log()).toEqual(["init"]);
  });

  test("fails when a stage changed files but wrote no message", async () => {
    const { artifacts } = await repo();
    await writeFile("a.go", "package a\n\nfunc A() {}\n");
    await expect(checkpoint(artifacts, "implement")).rejects.toThrow("wrote no commit message");
  });

  test("records a review round even when the fixer changed nothing", async () => {
    const { artifacts } = await repo();
    await writeFile(join(artifacts, "commit-review.txt"), "fix(a): review round\n\nDisputed: the finding about A.\n");
    expect(await checkpoint(artifacts, "review")).toMatch(/fix\(a\): review round$/);
    expect(await log()).toEqual(["fix(a): review round", "init"]);
  });

  test("commits the formatter's changes with the previous commit's trailers", async () => {
    const { artifacts } = await repo();
    await writeFile("a.go", "package a\n// formatted\n");
    await checkpoint(artifacts, "gate");
    const message = (await $`git log -1 ${"--format=%B"}`.quiet().text()).trim();
    expect(message).toBe("style: apply mise run fmt\n\nCo-Authored-By: Someone <s@example.com>");
  });

  test("refuses a stage it doesn't know", async () => {
    const { artifacts } = await repo();
    await expect(checkpoint(artifacts, "deploy")).rejects.toThrow("unknown stage");
  });
});

describe("isTestPath", () => {
  const cases: [string, boolean][] = [
    ["internal/cli/cli_test.go", true],
    ["testdata/gh/run-list.json", true],
    ["internal/x/testdata/fuzz/FuzzParse/abc", true],
    ["src/app.test.ts", true],
    ["src/app.spec.js", true],
    ["tests/test_parse.py", true],
    ["test_parse.py", true],
    ["__tests__/a.js", true],
    ["internal/cli/cli.go", false],
    ["README.md", false],
    ["contest/main.go", false],
    ["latest_version.go", false],
  ];
  for (const [path, want] of cases) {
    test(path, () => expect(isTestPath(path)).toBe(want));
  }
});

describe("setLane", () => {
  const now = new Date("2026-11-22T17:53:00.123Z");

  test("creates the file and the folder", async () => {
    const dir = join(await mkdtemp(join(tmpdir(), "factory-")), ".dex");
    await setLane(dir, "abc123", "design", "run1", now);
    expect(JSON.parse(await readFile(join(dir, "lanes.json"), "utf8"))).toEqual({
      abc123: { lane: "design", run: "run1", updated: "2026-11-22T17:53:00Z" },
    });
  });

  test("keeps other tasks and replaces this one's", async () => {
    const dir = await mkdtemp(join(tmpdir(), "factory-"));
    await writeFile(join(dir, "lanes.json"), JSON.stringify({ other: { lane: "done", run: "r0", updated: "x" } }));
    await setLane(dir, "abc123", "design", "run1", now);
    await setLane(dir, "abc123", "build", "run1", now);
    const lanes = JSON.parse(await readFile(join(dir, "lanes.json"), "utf8"));
    expect(lanes.other).toEqual({ lane: "done", run: "r0", updated: "x" });
    expect(lanes.abc123.lane).toBe("build");
  });

  test("serialises concurrent writers and leaves no lock or temp file", async () => {
    const dir = await mkdtemp(join(tmpdir(), "factory-"));
    const tasks = Array.from({ length: 10 }, (_, i) => `t${i}`);
    await Promise.all(tasks.map((t) => setLane(dir, t, "build", "run", now)));
    const lanes = JSON.parse(await readFile(join(dir, "lanes.json"), "utf8"));
    expect(Object.keys(lanes).sort()).toEqual(tasks.sort());
    expect(await readdir(dir)).toEqual(["lanes.json"]);
  });
});
