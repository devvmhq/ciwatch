// A Claude Code PreToolUse hook for Bash: blocks commands that write files from
// the shell (heredocs, redirects into files, tee, sed -i, perl -i), so agents
// create and edit files with their editor tools. A project turns it on in
// .claude/settings.json (see hooks/settings.json and the README); Archon's
// runs load the project's settings, so it covers every factory step too.
//
// Claude Code passes the tool call as JSON on stdin; exit code 2 blocks the
// call and shows stderr to the agent.
import { blockedMessage, shellWrite } from "../.shared/shellwrites.ts";

const input = JSON.parse(await Bun.stdin.text()) as { tool_name?: string; tool_input?: { command?: string } };
if (input.tool_name === "Bash") {
  const reason = shellWrite(input.tool_input?.command ?? "");
  if (reason !== null) {
    console.error(blockedMessage(reason));
    process.exit(2);
  }
}
