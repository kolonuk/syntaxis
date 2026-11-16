// SPDX-License-Identifier: GPL-2.0-or-later

export interface FileDiff {
  name: string;
  added: number;
  removed: number;
  lines: string[];
  /** the whole file diff as git printed it, header included */
  raw: string;
}

/** Splits the output of the server's diff script into the branch, the status lines and one entry per file. */
export function parseDiff(text: string): { branch: string; status: string[]; files: FileDiff[] } {
  const out = { branch: "", status: [] as string[], files: [] as FileDiff[] };
  let part = "";
  let cur: FileDiff | null = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("### ")) {
      part = line.slice(4).split(" ")[0] ?? "";
      if (part === "branch") out.branch = line.slice("### branch ".length).trim();
      continue;
    }
    if (part === "status") {
      if (line.trim()) out.status.push(line);
    } else if (part === "diff") {
      if (line.startsWith("diff --git ")) {
        const m = / b\/(.+)$/.exec(line);
        cur = { name: m?.[1] ?? line, added: 0, removed: 0, lines: [], raw: line + "\n" };
        out.files.push(cur);
      } else if (cur) {
        if (line.startsWith("+") && !line.startsWith("+++")) cur.added++;
        else if (line.startsWith("-") && !line.startsWith("---")) cur.removed++;
        cur.lines.push(line);
        cur.raw += line + "\n";
      }
    }
  }
  return out;
}

/** The message that sends review comments back to the agent. */
export function commentsMessage(comments: Record<string, string>): string {
  const items = Object.entries(comments)
    .filter(([, c]) => c.trim())
    .map(([f, c]) => `- ${f}: ${c.trim()}`);
  if (items.length === 0) return "";
  return (
    "Review comments on your changes. Please address each one, then tell me what you changed:\n" +
    items.join("\n")
  );
}
