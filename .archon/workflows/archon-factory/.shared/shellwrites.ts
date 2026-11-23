// Which shell commands write files: the check behind hooks/no-shell-writes.ts,
// which makes agents create and edit files with their editor tools instead.

// Quoted strings, blanked out, so a `>` inside a grep pattern or a message
// isn't taken for a redirect.
function unquote(command: string): string {
  return command.replace(/'[^']*'/g, "''").replace(/"(?:[^"\\]|\\.)*"/g, '""');
}

// A read-only gh command recording a fixture straight into testdata/, the one
// shell write allowed: routing a large recording through the editor tools
// would put all of it in the agent's context.
function isFixtureRecording(command: string, targets: string[]): boolean {
  return /^\s*gh\s+(run\s+(view|list)|api)\b/.test(command) && targets.length > 0 && targets.every((t) => /(^|\/)testdata\//.test(t));
}

// Why the command writes a file from the shell, or null when it doesn't.
export function shellWrite(command: string): string | null {
  if (/<<-?\s*['"]?[A-Za-z_][A-Za-z0-9_]*['"]?/.test(command.replace(/<<</g, ""))) {
    return "a heredoc";
  }
  const bare = unquote(command);
  if (/\bsed\s+(-[a-zA-Z]*i|--in-place)/.test(bare)) return "sed -i";
  if (/\bperl\s+-[a-zA-Z]*i/.test(bare)) return "perl -i";
  const tee = /\btee\s+(?:-a\s+)?([^\s|;&)]+)/.exec(bare);
  if (tee && tee[1] !== "/dev/null") return `tee into ${tee[1]}`;
  const targets: string[] = [];
  for (const m of bare.matchAll(/(?<![<>&])(?:\d*|&)(?:>>?|>\|)(?!&)(?!\()\s*([^\s;|&)<>]+)/g)) {
    if (m[1] !== "/dev/null") targets.push(m[1]);
  }
  if (targets.length > 0 && !isFixtureRecording(bare, targets)) return `a redirect into ${targets.join(", ")}`;
  return null;
}

// The hook's message to the agent when a command is blocked.
export function blockedMessage(reason: string): string {
  return `Blocked: this command writes a file from the shell (${reason}). Create and edit files with the editor tools (Write, Edit) instead, throwaway files included. Only a read-only gh command recording a fixture straight into testdata/ may redirect into a file.`;
}
