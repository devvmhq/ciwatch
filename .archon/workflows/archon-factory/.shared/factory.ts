// The factory pack's steps that aren't AI: dex, lanes, the fail-first check, the
// diff, the gate and the commit. Each workflow's scripts/factory.ts calls step()
// with the values its node binds; nothing here reads the environment itself.
//
// Runs happen in an Archon worktree, but dex's storage (.dex/, gitignored) lives
// only in the project's main checkout, so every dex and lane call goes there.

import { $ } from "bun";
import { existsSync } from "node:fs";
import { mkdir, readFile, rename, rmdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

$.throws(true);

export type Args = {
  task: string;
  lane: string;
  run: string;
  artifacts: string;
};

// The project's main checkout: the parent of the git directory that every
// worktree shares.
export async function root(): Promise<string> {
  const common = (await $`git rev-parse --path-format=absolute --git-common-dir`.quiet().text()).trim();
  return dirname(common);
}

async function dex(args: string[]): Promise<string> {
  const store = join(await root(), ".dex");
  return (await $`dex --storage-path ${store} ${args}`.quiet().text()).trim();
}

// Refuses anything that doesn't look like a dex task ID before it reaches a
// command or a file, then checks that the task exists.
export async function checkTask(task: string): Promise<void> {
  if (!/^[a-z0-9]+$/.test(task)) throw new Error(`not a dex task ID: '${task}'`);
  await dex(["show", task, "--json"]);
}

// Records the task's lane in .dex/lanes.json, a sidecar that dex never rewrites
// (dex drops metadata keys it doesn't know). A mkdir lock serialises writers.
export async function setLane(dexDir: string, task: string, lane: string, run: string, now = new Date()): Promise<void> {
  const file = join(dexDir, "lanes.json");
  const lock = join(dexDir, "lanes.lock");
  await mkdir(dexDir, { recursive: true });
  await acquire(lock);
  try {
    const lanes = existsSync(file) ? JSON.parse(await readFile(file, "utf8")) : {};
    lanes[task] = { lane, run, updated: now.toISOString().replace(/\.\d{3}Z$/, "Z") };
    const tmp = `${file}.${process.pid}.tmp`;
    await writeFile(tmp, JSON.stringify(lanes, null, 2) + "\n");
    await rename(tmp, file);
  } finally {
    await rmdir(lock);
  }
}

async function acquire(lock: string): Promise<void> {
  for (let i = 0; i < 50; i++) {
    try {
      await mkdir(lock);
      return;
    } catch {
      await Bun.sleep(100);
    }
  }
  throw new Error(`could not lock ${lock}`);
}

// Whether a changed path is a test or a test fixture, for the fail-first check.
export function isTestPath(path: string): boolean {
  return /(_test\.[a-z]+|\.(test|spec)\.[a-z]+)$|(^|\/)(testdata|tests?|__tests__)\/|(^|\/)test_[^/]*\.py$/.test(path);
}

// Gets a fresh worktree ready: trusts the project's mise config (the worktree
// path is new to mise) and records the commit the run started from.
async function prepare(artifacts: string): Promise<void> {
  if (existsSync("mise.toml") || existsSync(".mise.toml")) await $`mise trust --quiet`.quiet();
  const head = (await $`git rev-parse HEAD`.quiet().text()).trim();
  await writeFile(join(artifacts, "base"), head + "\n");
}

// The run's changes so far, new files included, against the starting commit.
async function diff(artifacts: string, options: string[] = []): Promise<string> {
  const base = (await readFile(join(artifacts, "base"), "utf8")).trim();
  await $`git add --intent-to-add --all`.quiet();
  return (await $`git diff ${options} ${base}`.quiet().text()).trimEnd();
}

// Fail-first: new or changed test files exist, and the suite fails.
async function red(artifacts: string): Promise<void> {
  const changed = (await diff(artifacts, ["--name-only"])).split("\n").filter(Boolean);
  if (!changed.some(isTestPath)) throw new Error("no test files were added or changed");
  const result = await $`mise run test`.quiet().nothrow();
  const log = (result.stdout.toString() + result.stderr.toString()).trimEnd().split("\n");
  if (result.exitCode === 0) {
    throw new Error(`the tests pass before any change to the code, so they don't test it:\n${log.slice(-20).join("\n")}`);
  }
  console.log(`red as expected:\n${log.slice(-30).join("\n")}`);
}

// The project gate: its mise.toml defines fmt, lint and test.
async function gate(): Promise<void> {
  for (const task of ["fmt", "lint", "test"]) await $`mise run ${task}`;
}

// Commits the worktree as one commit with the message the commit-message node
// wrote, fast-forwards the main checkout's current branch to it, and completes
// the dex task. Never pushes. The task ID stays out of the commit; dex links the
// task to the commit instead.
async function commit(a: Args): Promise<void> {
  const message = join(a.artifacts, "commit-message.txt");
  if (!existsSync(message) || (await readFile(message, "utf8")).trim() === "") {
    throw new Error(`no commit message at ${message}`);
  }
  await $`git add --all`.quiet();
  if ((await $`git diff --cached --quiet`.quiet().nothrow()).exitCode === 0) throw new Error("nothing to commit");
  await $`git commit --quiet --file ${message}`.quiet();
  const sha = (await $`git rev-parse HEAD`.quiet().text()).trim();
  const main = await root();
  const branch = (await $`git -C ${main} branch --show-current`.quiet().text()).trim();
  const merged = await $`git -C ${main} merge --ff-only --quiet ${sha}`.quiet().nothrow();
  if (merged.exitCode !== 0) {
    const here = (await $`git branch --show-current`.quiet().text()).trim();
    throw new Error(`committed ${sha} on ${here}, but could not fast-forward ${branch} in ${main}; merge it by hand:\n${merged.stderr}`);
  }
  await dex(["complete", a.task, "--result", `Committed ${sha} (Archon run ${a.run})`, "--commit", sha]);
  await setLane(join(main, ".dex"), a.task, "done", a.run);
  console.log(sha);
}

// Runs one step. Every step but start records the lane first when one is bound.
export async function step(name: string, a: Args): Promise<void> {
  if (name === "start") {
    await checkTask(a.task);
    await prepare(a.artifacts);
    await dex(["start", a.task, "--force"]);
  }
  if (a.lane !== "") await setLane(join(await root(), ".dex"), a.task, a.lane, a.run);
  switch (name) {
    case "start":
      console.log(await dex(["show", a.task, "--json"]));
      return;
    case "lane":
      return;
    case "red":
      return red(a.artifacts);
    case "diff":
      console.log(await diff(a.artifacts));
      return;
    case "summary":
      console.log(`Worktree: ${process.cwd()}\n${await diff(a.artifacts, ["--stat"])}`);
      return;
    case "gate":
      return gate();
    case "commit":
      return commit(a);
    default:
      throw new Error(`unknown step '${name}'`);
  }
}

// The entry point for each workflow's scripts/factory.ts.
export async function main(name: string, a: Args): Promise<void> {
  try {
    await step(name, a);
  } catch (err) {
    const e = err as { message?: string; stderr?: { toString(): string } };
    console.error(`factory ${name}: ${e.message ?? String(err)}${e.stderr ? `\n${e.stderr.toString().trimEnd()}` : ""}`);
    process.exit(1);
  }
}
