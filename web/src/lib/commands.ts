// SPDX-License-Identifier: GPL-2.0-or-later

export interface SlashCommand {
  name: string;
  description: string;
  source: "builtin" | "user" | "repository";
}

/**
 * The commands to offer while the message starts with "/" and no space has been typed yet. A message
 * that does not look like a command (a path, a sentence) offers nothing.
 */
export function matchCommands(draft: string, all: SlashCommand[]): SlashCommand[] {
  const m = /^\/([a-z0-9-]*)$/i.exec(draft);
  if (!m) return [];
  const q = m[1]!.toLowerCase();
  const starts = all.filter((c) => c.name.startsWith(q));
  const rest = all.filter((c) => !c.name.startsWith(q) && c.name.includes(q));
  return [...starts, ...rest].slice(0, 8);
}
