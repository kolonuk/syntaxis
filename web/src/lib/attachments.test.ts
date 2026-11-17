// SPDX-License-Identifier: GPL-2.0-or-later
import { describe, expect, it } from "vitest";
import { attachmentName, fileSize, parseAttachments } from "./attachments";
import { latestTodos, progress, todosFromArgs } from "./todos";
import { matchCommands, type SlashCommand } from "./commands";

describe("parseAttachments", () => {
  it("splits the text from the list at the end", () => {
    const r = parseAttachments(
      "fix the layout\n\n<attachments>\n/workspace/.attachments/shot.png | image | 1234\n/workspace/.attachments/a report.pdf | pdf | 99\n</attachments>",
    );
    expect(r.text).toBe("fix the layout");
    expect(r.items).toEqual([
      { path: "/workspace/.attachments/shot.png", kind: "image", size: 1234 },
      { path: "/workspace/.attachments/a report.pdf", kind: "pdf", size: 99 },
    ]);
  });
  it("leaves ordinary messages alone", () => {
    expect(parseAttachments("hello")).toEqual({ text: "hello", items: [] });
    const mid = "<attachments>\n/workspace/.attachments/a.png | image | 5\n</attachments>\nmore";
    expect(parseAttachments(mid).items).toEqual([]);
  });
  it("accepts only files in the attachment folder", () => {
    const r = parseAttachments(
      "x\n\n<attachments>\n/etc/passwd | text | 1\n/workspace/.attachments/../secret | text | 1\n/workspace/.attachments/ok.txt | text | 3\n/workspace/.attachments/x | weird | 3\n</attachments>",
    );
    expect(r.items.map((i) => i.path)).toEqual(["/workspace/.attachments/ok.txt"]);
  });
  it("names and sizes", () => {
    expect(attachmentName("/workspace/.attachments/a b.png")).toBe("a b.png");
    expect(fileSize(500)).toBe("500 B");
    expect(fileSize(2048)).toBe("2.0 KB");
    expect(fileSize(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});

describe("task list", () => {
  const call = (todos: unknown) => ({
    role: "assistant",
    tool_calls: JSON.stringify([
      { id: "t", name: "todo_write", arguments: JSON.stringify({ todos }) },
    ]),
  });
  it("is the latest todo_write in the conversation", () => {
    const msgs = [
      call([{ content: "one", status: "pending" }]),
      {
        role: "assistant",
        tool_calls: JSON.stringify([{ id: "x", name: "run_shell", arguments: "{}" }]),
      },
      call([
        { content: "one", status: "completed" },
        { content: "two", status: "in_progress" },
        { content: "three", status: "weird" },
      ]),
    ];
    const t = latestTodos(msgs);
    expect(t.map((x) => x.status)).toEqual(["completed", "in_progress", "pending"]);
    expect(progress(t)).toEqual({ done: 1, total: 3 });
  });
  it("is empty when there is none, and survives bad history", () => {
    expect(latestTodos([])).toEqual([]);
    expect(latestTodos([{ role: "assistant", tool_calls: "not json" }])).toEqual([]);
    expect(todosFromArgs("{")).toBeNull();
    expect(todosFromArgs({ todos: [{ content: " ", status: "pending" }] })).toEqual([]);
  });
});

describe("matchCommands", () => {
  const all: SlashCommand[] = [
    { name: "review", description: "", source: "builtin" },
    { name: "test", description: "", source: "builtin" },
    { name: "retest", description: "", source: "user" },
  ];
  it("offers commands while a name is being typed", () => {
    expect(matchCommands("/", all).map((c) => c.name)).toEqual(["review", "test", "retest"]);
    expect(matchCommands("/re", all).map((c) => c.name)).toEqual(["review", "retest"]);
    expect(matchCommands("/est", all).map((c) => c.name)).toEqual(["test", "retest"]);
  });
  it("offers nothing for other text", () => {
    expect(matchCommands("hello", all)).toEqual([]);
    expect(matchCommands("/review the code", all)).toEqual([]);
    expect(matchCommands("/etc/passwd", all)).toEqual([]);
    expect(matchCommands("", all)).toEqual([]);
  });
});
