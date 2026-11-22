// The factory pack's steps that aren't AI: dex, lanes, the fail-first check, the
// diff, the gate, each stage's commit and the landing merge. Each workflow's scripts/factory.ts calls step()
// with the values its node binds; nothing here reads the environment itself.
//
// Runs happen in an Archon worktree, but dex's storage (.dex/, gitignored) lives
// only in the project's main checkout, so every dex and lane call goes there.

import { $ } from "bun";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, rename, rm, rmdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
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

// What a prompt's diff leaves out. Recorded fixtures (a gh run view recording
// is a 120 KB line of JSON) would otherwise be read again by every reviewer,
// fixer and message writer, and they dominated a run's tokens. Code, tests
// included, has short lines and stays in unless its diff is huge.
export const promptDiffLimits = { longestLine: 2000, fileBytes: 64000 };

// A diff for a prompt: each file's part as it is, except a part that looks like
// data (a line longer than longestLine) or is over fileBytes, which becomes a
// line naming the file, so the agent can read it if it matters.
export function trimDiff(diff: string, limits = promptDiffLimits): string {
  const kept: string[] = [];
  const omitted: string[] = [];
  for (const part of diff.split(/^(?=diff --git )/m).filter((p) => p.trim() !== "")) {
    const longest = Math.max(...part.split("\n").map((l) => l.length));
    if (longest <= limits.longestLine && part.length <= limits.fileBytes) {
      kept.push(part.trimEnd());
      continue;
    }
    const path = /^diff --git a\/(.*?) b\//.exec(part)?.[1] ?? "(unknown file)";
    omitted.push(`- ${path} (${part.length} bytes of diff)`);
  }
  if (omitted.length > 0) {
    kept.push(
      `Not shown here, because each file's diff is data (a line over ${limits.longestLine} characters) or over ${limits.fileBytes} bytes; read the file if it matters:\n${omitted.join("\n")}`,
    );
  }
  return kept.join("\n");
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

// The message for a task's squashed commit: the merge's message, with the
// subjects of the stacked commits that built it listed before its trailers.
export function squashMessage(merge: string, steps: string[]): string {
  const paragraphs = merge.trim().split(/\n{2,}/);
  const last = paragraphs[paragraphs.length - 1] ?? "";
  const trailers = paragraphs.length > 1 && last.split("\n").every((l) => /^[A-Za-z][A-Za-z0-9-]*: \S/.test(l));
  const out = trailers ? paragraphs.slice(0, -1) : [...paragraphs];
  if (steps.length > 0) out.push(`Built in steps:\n${steps.map((s) => `- ${s}`).join("\n")}`);
  if (trailers) out.push(last);
  return `${out.join("\n\n")}\n`;
}

async function git(args: string[]): Promise<string> {
  return (await $`git ${args}`.quiet().text()).trim();
}

// Squashes each task's stack before a push. Every merge commit on the current
// branch's first-parent history since upstream becomes one ordinary commit with
// the merge's tree and squashMessage; other commits are rebuilt as they are. The
// final tree is unchanged, the old head is kept under refs/factory/pre-squash/,
// and each dex task linked to a merge is relinked to its squashed commit. The
// task branches keep their stacks. Pushes nothing.
export async function squash(upstream: string): Promise<void> {
  const branch = await git(["symbolic-ref", "--short", "HEAD"]);
  if ((await $`git status --porcelain --untracked-files=no`.quiet().text()).trim() !== "") {
    throw new Error("commit or stash your changes first");
  }
  const base = await git(["rev-parse", "--verify", upstream]);
  const head = await git(["rev-parse", "HEAD"]);
  if ((await $`git merge-base --is-ancestor ${base} ${head}`.quiet().nothrow()).exitCode !== 0) {
    throw new Error(`${upstream} isn't an ancestor of ${branch}; pull first`);
  }
  const commits = (await git(["rev-list", "--first-parent", "--reverse", `${base}..${head}`])).split("\n").filter(Boolean);
  if (commits.length === 0) {
    console.log("nothing to squash");
    return;
  }
  const tmp = await mkdtemp(join(tmpdir(), "factory-squash-"));
  const squashed: [string, string][] = [];
  let prev = base;
  try {
    for (const c of commits) {
      const parents = (await git(["rev-list", "--parents", "-n1", c])).split(" ").slice(1);
      let message = await $`git log -1 ${"--format=%B"} ${c}`.quiet().text();
      if (parents.length > 1) {
        const steps = (await git(["log", "--reverse", "--format=%s", `${parents[0]}..${parents[1]}`])).split("\n").filter(Boolean);
        message = squashMessage(message, steps);
      }
      const file = join(tmp, "message");
      await writeFile(file, message);
      const [name, email, date] = (await $`git log -1 ${"--format=%an%x00%ae%x00%aI"} ${c}`.quiet().text()).trim().split("\0");
      const env = { ...process.env, GIT_AUTHOR_NAME: name, GIT_AUTHOR_EMAIL: email, GIT_AUTHOR_DATE: date };
      const made = (await $`git commit-tree ${`${c}^{tree}`} -p ${prev} -F ${file}`.env(env).quiet().text()).trim();
      if (parents.length > 1) squashed.push([c, made]);
      prev = made;
    }
  } finally {
    await rm(tmp, { recursive: true, force: true });
  }
  if ((await git(["rev-parse", `${prev}^{tree}`])) !== (await git(["rev-parse", `${head}^{tree}`]))) {
    throw new Error("the squashed history's files differ from HEAD's; nothing was changed");
  }
  const backup = `refs/factory/pre-squash/${branch}-${Date.now()}`;
  await git(["update-ref", backup, head]);
  await git(["update-ref", `refs/heads/${branch}`, prev, head]);
  console.log(`${branch}: ${commits.length} commits rebuilt, ${squashed.length} tasks squashed; the old head is ${backup}`);
  for (const [from, to] of squashed) console.log(`  ${from.slice(0, 7)} -> ${to.slice(0, 7)} ${await git(["log", "-1", "--format=%s", to])}`);
  await relinkDex(squashed);
}

// Points each dex task linked to a squashed merge at its new commit. A project
// without dex, or a task dex can't update, gets a warning, not a failure.
async function relinkDex(squashed: [string, string][]): Promise<void> {
  if (squashed.length === 0 || !existsSync(join(await root(), ".dex"))) return;
  try {
    const tasks: { id: string; metadata?: { commit?: { sha?: string } } }[] = JSON.parse(await dex(["list", "--all", "--json"]));
    for (const [from, to] of squashed) {
      for (const t of tasks.filter((t) => t.metadata?.commit?.sha === from)) {
        await dex(["edit", t.id, "--commit", to]);
        console.log(`  dex ${t.id} -> ${to.slice(0, 7)}`);
      }
    }
  } catch (err) {
    console.error(`squash: couldn't relink dex tasks: ${(err as Error).message}`);
  }
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
      console.log(trimDiff(await diff(a.artifacts)));
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
