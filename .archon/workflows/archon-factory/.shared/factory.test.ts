import { $ } from "bun";
import { afterEach, describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  changedRanges,
  checkpoint,
  churnFindings,
  extractSpec,
  inferenceNote,
  isRecordedData,
  isTestPath,
  pickedTasks,
  rebaseOntoSquashed,
  setLane,
  specParagraphs,
  squash,
  squashMessage,
  trimDiff,
} from "./factory.ts";

describe("churn", () => {
  const diff = [
    "diff --git a/internal/cli/cli.go b/internal/cli/cli.go",
    "--- a/internal/cli/cli.go",
    "+++ b/internal/cli/cli.go",
    "@@ -40,2 +40,5 @@ func x() {",
    "+a",
    "@@ -90 +93 @@",
    "-b",
    "+c",
    "diff --git a/README.md b/README.md",
    "--- a/README.md",
    "+++ b/README.md",
    "@@ -10,3 +9,0 @@",
  ].join("\n");

  test("changedRanges reads the new side of each hunk", () => {
    expect(changedRanges(diff)).toEqual(
      new Map([
        ["internal/cli/cli.go", [[40, 44], [93, 93]]],
        ["README.md", [[9, 9]]],
      ]),
    );
  });

  test("a major on or near the previous fix's lines is churn; minors and unplaced findings aren't", () => {
    const ranges = changedRanges(diff);
    const findings = [
      { file: "internal/cli/cli.go", line: 46, severity: "major" },
      { file: "internal/cli/cli.go", line: 60, severity: "major" },
      { file: "internal/cli/cli.go", line: 42, severity: "minor" },
      { file: "internal/cli/cli.go", severity: "blocker" },
      { file: "README.md", line: 7, severity: "blocker" },
    ];
    expect(churnFindings(findings, ranges).map((f) => `${f.file}:${f.line}`)).toEqual(["internal/cli/cli.go:46", "README.md:7"]);
  });
});

describe("pickedTasks", () => {
  test("reads a tasks line from the approval comment", () => {
    expect(pickedTasks("Land it.\ntasks: 3, 1\nthanks", 4)).toEqual([1, 3]);
    expect(pickedTasks("tasks: all", 3)).toEqual([1, 2, 3]);
    expect(pickedTasks("Tasks: none", 3)).toEqual([]);
    expect(pickedTasks("Land it.", 3)).toEqual([]);
  });

  test("refuses a number that isn't in the backlog", () => {
    expect(() => pickedTasks("tasks: 4", 3)).toThrow("isn't one of the backlog's numbers");
  });
});

describe("inferenceNote", () => {
  test("says none, flags an inference, or flags a missing section", () => {
    expect(inferenceNote("## Inference\nnone\n")).toBe("Inference: none.");
    expect(inferenceNote("## Inference\n\n`none`\n\n## Behaviour\nx")).toBe("Inference: none.");
    expect(inferenceNote("## Inference\n**None.**\n")).toBe("Inference: none.");
    expect(inferenceNote("## Inference\n- none\n")).toBe("Inference: none.");
    expect(inferenceNote('## Inference\nnone. "Already present" is decided by O_EXCL, not gh\'s text.\n')).toBe(
      'Inference: none ("Already present" is decided by O_EXCL, not gh\'s text.).',
    );
    expect(inferenceNote("## Inference\n`none` — nothing is guessed.\n")).toBe("Inference: none (nothing is guessed.).");
    expect(inferenceNote("## Inference\nNone of the steps are named, so we cut by timestamps.\n")).toContain("THIS DESIGN INFERS");
    // A design whose later sections use bold headings, not ##.
    expect(inferenceNote("## Inference\n\nnone\n\n**Behaviour**\n\nSkips unreadable folders.\n\n**Code**\n\nx")).toBe("Inference: none.");
    expect(inferenceNote("## Inference\n\n- Steps are cut by time.\n\n- And by markers.\n\n**Code**\n\nx")).toContain("And by markers.");
    expect(inferenceNote("## Inference\n- Steps are cut by timestamps.\n")).toContain("THIS DESIGN INFERS");
    expect(inferenceNote("## Acceptance cases\nAC-1: x\n")).toContain("has no \"## Inference\" section");
  });
});

describe("spec in squashes", () => {
  test("specParagraphs takes only the Spec paragraphs", () => {
    expect(specParagraphs("test(a): x\n\nBody.\n\nSpec: acceptance cases\nAC-1: y\n\nSpec: non-goals\n- z\n\nCo-Authored-By: S <s@e>")).toEqual([
      "Spec: acceptance cases\nAC-1: y",
      "Spec: non-goals\n- z",
    ]);
  });

  test("squashMessage puts the spec before the steps and the trailers", () => {
    expect(squashMessage("feat: a\n\nWhy.\n\nCo-Authored-By: S <s@e>\n", ["t", "f"], ["Spec: acceptance cases\nAC-1: y"])).toBe(
      "feat: a\n\nWhy.\n\nSpec: acceptance cases\nAC-1: y\n\nBuilt in steps:\n- t\n- f\n\nCo-Authored-By: S <s@e>\n",
    );
  });
});

describe("isRecordedData", () => {
  test("names testdata and logs as recorded data", () => {
    expect(isRecordedData("testdata/logs/run.log")).toBe(true);
    expect(isRecordedData("internal/x/testdata/fuzz/F/abc")).toBe(true);
    expect(isRecordedData("build.log")).toBe(true);
    expect(isRecordedData("internal/cli/cli_test.go")).toBe(false);
  });

  test("trimDiff names a small testdata file instead of showing it", () => {
    const part = "diff --git a/testdata/x.json b/testdata/x.json\n--- a/testdata/x.json\n+++ b/testdata/x.json\n@@ -0,0 +1 @@\n+{}\n";
    expect(trimDiff(part)).toContain("- testdata/x.json (");
  });
});

describe("extractSpec", () => {
  const design = [
    "# Design: x",
    "",
    "## 1. What changes",
    "Words.",
    "",
    "## Acceptance cases",
    "1. AC-1: `ciwatch HEAD` with one failed run exits 1.",
    "2. AC-2: `--json` has a `runs` array.",
    "",
    "## Non-goals",
    "- Watching.",
    "",
    "## Questions",
    "1. None.",
  ].join("\n");

  test("takes the acceptance cases, the non-goals and the approval comment", () => {
    expect(extractSpec(design, "Approved; use exit 4 for X.\n")).toBe(
      [
        "Spec: acceptance cases\n1. AC-1: `ciwatch HEAD` with one failed run exits 1.\n2. AC-2: `--json` has a `runs` array.",
        "Spec: non-goals\n- Watching.",
        "Spec: approved with\nApproved; use exit 4 for X.",
      ].join("\n\n"),
    );
  });

  test("keeps each section one paragraph, so a squash carries all of it", () => {
    const spaced = "## Acceptance cases\n\nAC-1: a\n\nAC-2: b\n\n## Non-goals\n\n- c\n\n- d\n";
    const spec = extractSpec(spaced, "Approved.\n\nWith a note.");
    expect(spec).toBe("Spec: acceptance cases\nAC-1: a\nAC-2: b\n\nSpec: non-goals\n- c\n- d\n\nSpec: approved with\nApproved.\nWith a note.");
    expect(specParagraphs(`test: x\n\n${spec}\n\nCo-Authored-By: S <s@e>`).join("\n\n")).toBe(spec);
  });

  test("reads a section that ends the design", () => {
    expect(extractSpec("## Acceptance cases\nAC-1: y", "")).toBe("Spec: acceptance cases\nAC-1: y\n\nSpec: non-goals\n(none stated)");
  });

  test("ends a section at a bold heading or a rule, not only at ##", () => {
    const design = "## Acceptance cases\nAC-1: a\n\n## Non-goals\n- b\n\n**Behaviour**\nNot a non-goal.\n\n---\n\n## Inference\nnone\n";
    expect(extractSpec(design, "")).toBe("Spec: acceptance cases\nAC-1: a\n\nSpec: non-goals\n- b\n\nSpec: inference\nnone");
  });

  test("refuses a design without acceptance cases", () => {
    expect(() => extractSpec("## Non-goals\n- x", "")).toThrow("no \"## Acceptance cases\"");
  });
});

describe("squashMessage", () => {
  test("lists the steps before the trailers", () => {
    const merge = "feat(a): add A\n\nWhat and why.\n\nCo-Authored-By: Someone <s@example.com>\n";
    expect(squashMessage(merge, ["test(a): fail", "feat(a): add A"])).toBe(
      "feat(a): add A\n\nWhat and why.\n\nBuilt in steps:\n- test(a): fail\n- feat(a): add A\n\nCo-Authored-By: Someone <s@example.com>\n",
    );
  });

  test("appends the steps when there are no trailers", () => {
    expect(squashMessage("feat(a): add A\n\nLeft from review:\n- a.go: x\n", ["t"])).toBe(
      "feat(a): add A\n\nLeft from review:\n- a.go: x\n\nBuilt in steps:\n- t\n",
    );
  });

  test("leaves a message alone when there are no steps", () => {
    expect(squashMessage("fix: x\n", [])).toBe("fix: x\n");
  });
});

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
    expect(got).toContain("grep it or read the lines you need");
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

  test("records the approved spec in the tests commit, before its trailers", async () => {
    const { artifacts } = await repo();
    await writeFile("a_test.go", "package a\n");
    await writeFile(join(artifacts, "design.md"), "## Acceptance cases\nAC-1: a fails\n\n## Non-goals\n- b\n");
    await writeFile(join(artifacts, "approval.txt"), "Approved.\n");
    await writeFile(join(artifacts, "commit-tests.txt"), "test(a): a fails\n\nCo-Authored-By: Someone <s@example.com>\n");
    await checkpoint(artifacts, "tests");
    expect((await $`git log -1 ${"--format=%B"}`.quiet().text()).trim()).toBe(
      "test(a): a fails\n\nSpec: acceptance cases\nAC-1: a fails\n\nSpec: non-goals\n- b\n\nSpec: approved with\nApproved.\n\nCo-Authored-By: Someone <s@example.com>",
    );
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

describe("squash", () => {
  const start = process.cwd();
  afterEach(() => process.chdir(start));

  // Lands a task as the factory does: a branch of commits, then a --no-ff merge.
  async function land(name: string, files: string[]): Promise<void> {
    await $`git checkout --quiet -b ${name}`.quiet();
    for (const f of files) {
      await writeFile(f, `${f}\n`);
      await $`git add --all && git commit --quiet -m ${`step ${f}`}`.quiet();
    }
    await $`git checkout --quiet main && git merge --quiet --no-ff -m ${`feat: ${name}\n\nCo-Authored-By: Someone <s@example.com>`} ${name}`.quiet();
  }

  test("turns each task's merge into one commit and keeps the rest", async () => {
    await repo();
    await $`git update-ref refs/remotes/origin/main HEAD`.quiet();
    await writeFile("direct.txt", "x\n");
    await $`git add --all && git commit --quiet -m ${"chore: direct"}`.quiet();
    await land("one", ["a.txt", "b.txt"]);
    await land("two", ["c.txt"]);
    const tree = (await $`git rev-parse HEAD^{tree}`.quiet().text()).trim();

    await squash("origin/main");

    expect((await $`git rev-parse HEAD^{tree}`.quiet().text()).trim()).toBe(tree);
    expect(await log()).toEqual(["feat: two", "feat: one", "chore: direct", "init"]);
    expect((await $`git rev-list --merges origin/main..HEAD`.quiet().text()).trim()).toBe("");
    const one = (await $`git log -1 ${"--format=%B"} HEAD~1`.quiet().text()).trim();
    expect(one).toBe("feat: one\n\nBuilt in steps:\n- step a.txt\n- step b.txt\n\nCo-Authored-By: Someone <s@example.com>");
    expect((await $`git for-each-ref ${"--format=%(refname)"} refs/factory/pre-squash/`.quiet().text()).trim()).toMatch(/^refs\/factory\/pre-squash\/main-/);
    expect((await $`git log -1 ${"--format=%s"} one`.quiet().text()).trim()).toBe("step b.txt");
  });

  test("carries the tests commit's spec into the squashed commit", async () => {
    await repo();
    await $`git update-ref refs/remotes/origin/main HEAD`.quiet();
    await $`git checkout --quiet -b specced`.quiet();
    await writeFile("a_test.go", "package a\n");
    await $`git add --all && git commit --quiet -m ${"test(a): fail"} -m ${"Spec: acceptance cases\nAC-1: a"} -m ${"Spec: non-goals\n- b"}`.quiet();
    await writeFile("a.go", "package a\n\nfunc A() {}\n");
    await $`git add --all && git commit --quiet -m ${"feat(a): add A"}`.quiet();
    await $`git checkout --quiet main && git merge --quiet --no-ff -m ${"feat(a): add A\n\nCo-Authored-By: S <s@e>"} specced`.quiet();
    await squash("origin/main");
    expect((await $`git log -1 ${"--format=%B"}`.quiet().text()).trim()).toBe(
      "feat(a): add A\n\nSpec: acceptance cases\nAC-1: a\n\nSpec: non-goals\n- b\n\nBuilt in steps:\n- test(a): fail\n- feat(a): add A\n\nCo-Authored-By: S <s@e>",
    );
  });

  test("a run that started before a squash lands only its own commits", async () => {
    await repo();
    await $`git update-ref refs/remotes/origin/main HEAD`.quiet();
    await land("first", ["a.txt"]);
    // A run starts from the unsquashed main, then main is squashed under it.
    const base = (await $`git rev-parse HEAD`.quiet().text()).trim();
    await $`git checkout --quiet -b run`.quiet();
    await writeFile("r.txt", "r\n");
    await $`git add --all && git commit --quiet -m ${"feat: run"}`.quiet();
    await $`git checkout --quiet main`.quiet();
    await squash("origin/main");
    await $`git checkout --quiet run`.quiet();
    const newBase = await rebaseOntoSquashed(base, "main");
    expect(newBase).toBe((await $`git rev-parse main`.quiet().text()).trim());
    expect((await $`git log ${"--format=%s"} main..run`.quiet().text()).trim()).toBe("feat: run");
    expect((await $`git rev-list --merges main..run`.quiet().text()).trim()).toBe("");
    // A base still on main is left alone.
    expect(await rebaseOntoSquashed(newBase, "main")).toBe(newBase);
  });

  test("refuses a checkout with uncommitted changes", async () => {
    await repo();
    await $`git update-ref refs/remotes/origin/main HEAD`.quiet();
    await writeFile("a.go", "package a\n// changed\n");
    await expect(squash("origin/main")).rejects.toThrow("commit or stash");
  });

  test("does nothing when there's nothing past the upstream", async () => {
    await repo();
    await $`git update-ref refs/remotes/origin/main HEAD`.quiet();
    const head = (await $`git rev-parse HEAD`.quiet().text()).trim();
    await squash("origin/main");
    expect((await $`git rev-parse HEAD`.quiet().text()).trim()).toBe(head);
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
