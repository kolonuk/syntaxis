// SPDX-License-Identifier: GPL-2.0-or-later

export interface Todo {
  content: string;
  status: "pending" | "in_progress" | "completed";
}

interface StoredMessage {
  role: string;
  tool_calls?: string;
}

const STATUSES = new Set(["pending", "in_progress", "completed"]);

/** Reads a task list from the arguments of a todo_write call (a string or an object). */
export function todosFromArgs(args: unknown): Todo[] | null {
  let a: unknown = args;
  if (typeof a === "string") {
    try {
      a = JSON.parse(a);
    } catch {
      return null;
    }
  }
  const list = (a as { todos?: unknown } | null)?.todos;
  if (!Array.isArray(list)) return null;
  const out: Todo[] = [];
  for (const t of list as { content?: unknown; status?: unknown }[]) {
    if (typeof t?.content !== "string" || !t.content.trim()) continue;
    out.push({
      content: t.content,
      status: STATUSES.has(String(t.status)) ? (t.status as Todo["status"]) : "pending",
    });
  }
  return out;
}

/** The latest task list in a conversation, so the progress list is there after a reload. */
export function latestTodos(msgs: StoredMessage[]): Todo[] {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i]!;
    if (m.role !== "assistant" || !m.tool_calls) continue;
    let calls: { name?: string; arguments?: unknown }[];
    try {
      calls = JSON.parse(m.tool_calls) as typeof calls;
    } catch {
      continue;
    }
    for (let j = calls.length - 1; j >= 0; j--) {
      if (calls[j]!.name === "todo_write") {
        const t = todosFromArgs(calls[j]!.arguments);
        if (t) return t;
      }
    }
  }
  return [];
}

export function progress(todos: Todo[]): { done: number; total: number } {
  return { done: todos.filter((t) => t.status === "completed").length, total: todos.length };
}
