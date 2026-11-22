// The factory pack's steps that aren't AI: dex, lanes, the fail-first check, the
// diff, the gate, each stage's commit and the landing merge. Each workflow's scripts/factory.ts calls step()
// with the values its node binds; nothing here reads the environment itself.
//
// Runs happen in an Archon worktree, but dex's storage (.dex/, gitignored) lives
// only in the project's main checkout, so every dex and lane call goes there.

import { $ } from "bun";
import { existsSync } from "node:fs";
import { mkdir, readFile, rename, rm, rmdir, writeFile } from "node:fs/promises";
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

async function nonEmpty(file: string): Promise<boolean> {
  return existsSync(file) && (await readFile(file, "utf8")).trim() !== "";
}

async function staged(): Promise<boolean> {
  await $`git add --all`.quiet();
  return (await $`git diff --cached --quiet`.quiet().nothrow()).exitCode !== 0;
}

// The stages that commit on the run's branch, each with the message its AI step
// writes to commit-<stage>.txt in the artifacts folder. gate is the exception:
// what mise run fmt changed gets a fixed message.
export const stages = ["tests", "implement", "self-review", "review", "reproduce", "fix", "gate"] as const;

// Commits what a stage changed on the run's branch, so the branch keeps every
// step: the failing tests, the implementation, the self-review, each review
// round and the formatter. A stage that changed nothing commits nothing, except
// a review round, which always commits (empty if the fixer changed nothing) so
// the reviewer's findings and the answers to them stay in the history.
export async function checkpoint(artifacts: string, stage: string): Promise<string> {
  if (!(stages as readonly string[]).includes(stage)) throw new Error(`unknown stage '${stage}'`);
  const changed = await staged();
  const file = join(artifacts, `commit-${stage}.txt`);
  if (stage === "gate") {
    if (!changed) return "gate: nothing to commit";
    // The previous commit's trailers (Co-Authored-By and the like), since the
    // project's rules for commits apply to this one too.
    const trailers = (await $`git log -1 ${"--format=%(trailers:only)"}`.quiet().text()).trim();
    await writeFile(file, `style: apply mise run fmt${trailers ? `\n\n${trailers}` : ""}\n`);
  } else if (!(await nonEmpty(file))) {
    if (!changed) return `${stage}: nothing to commit`;
    throw new Error(`the ${stage} step changed files but wrote no commit message to ${file}`);
  } else if (!changed && stage !== "review") {
    await rm(file);
    return `${stage}: nothing to commit`;
  }
  await $`git commit --quiet --allow-empty --file ${file}`.quiet();
  await rm(file); // so a later round can't reuse it
  return (await $`git log -1 ${"--format=%h %s"}`.quiet().text()).trim();
}

// Lands the run's branch on the main checkout's current branch with a merge
// commit (git merge --no-ff), whose message the commit-message node wrote, and
// completes the dex task. Every commit on the branch is kept, and the main
// branch's first-parent history passes the gate at every commit. Never pushes.
// The task ID stays out of the commits; dex links the task to the merge instead.
async function land(a: Args): Promise<void> {
  const message = join(a.artifacts, "commit-message.txt");
  if (!(await nonEmpty(message))) throw new Error(`no landing message at ${message}`);
  if (await staged()) throw new Error("the worktree has changes no checkpoint committed");
  const head = (await $`git rev-parse HEAD`.quiet().text()).trim();
  const base = (await readFile(join(a.artifacts, "base"), "utf8")).trim();
  if (head === base) throw new Error("the branch has no commits to land");
  const main = await root();
  const branch = (await $`git -C ${main} branch --show-current`.quiet().text()).trim();
  const merged = await $`git -C ${main} merge --no-ff --quiet --file ${message} ${head}`.quiet().nothrow();
  if (merged.exitCode !== 0) {
    await $`git -C ${main} merge --abort`.quiet().nothrow();
    const here = (await $`git branch --show-current`.quiet().text()).trim();
    throw new Error(`could not merge ${here} into ${branch} in ${main}; it's left as it was, so merge it by hand:\n${merged.stdout}${merged.stderr}`);
  }
  const sha = (await $`git -C ${main} rev-parse HEAD`.quiet().text()).trim();
  await dex(["complete", a.task, "--result", `Landed ${sha} (Archon run ${a.run})`, "--commit", sha]);
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
    case "land":
      return land(a);
    default:
      if (name.startsWith("checkpoint-")) {
        console.log(await checkpoint(a.artifacts, name.slice("checkpoint-".length)));
        return;
      }
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
