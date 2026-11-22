import { describe, expect, test } from "bun:test";
import { mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { isTestPath, setLane } from "./factory.ts";

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
