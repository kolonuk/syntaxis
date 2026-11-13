// SPDX-License-Identifier: GPL-2.0-or-later
import { describe, expect, it } from "vitest";
import { buildItems, describeCall } from "./Chat";

describe("buildItems", () => {
  it("pairs each tool call with its result and keeps text in order", () => {
    const items = buildItems([
      { id: 1, role: "user", content: "run the tests" },
      {
        id: 2,
        role: "assistant",
        content: "On it.",
        tool_calls: JSON.stringify([
          { id: "c1", name: "run_shell", arguments: '{"command":"go test ./..."}' },
        ]),
      },
      { id: 3, role: "tool", content: "exit code: 0\nok", tool_call_id: "c1" },
      { id: 4, role: "assistant", content: "All green." },
    ]);
    expect(items.map((i) => i.kind)).toEqual(["user", "assistant", "step", "assistant"]);
    const step = items[2]!;
    expect(step.kind === "step" && step.output).toContain("ok");
  });
  it("shows a step without output while it is still running, and survives bad history", () => {
    const items = buildItems([
      {
        id: 1,
        role: "assistant",
        content: "",
        tool_calls: JSON.stringify([{ id: "x", name: "run_shell", arguments: "{}" }]),
      },
      { id: 2, role: "assistant", content: "", tool_calls: "not json" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]!.kind === "step" && items[0]!.output).toBeUndefined();
  });
  it("does not show tool results as chat messages", () => {
    expect(
      buildItems([{ id: 1, role: "tool", content: "secret-looking output", tool_call_id: "z" }]),
    ).toHaveLength(0);
  });
});

describe("describeCall", () => {
  it("summarises common tools readably", () => {
    expect(describeCall({ id: "1", name: "run_shell", arguments: '{"command":"ls -la"}' })).toBe(
      "ls -la",
    );
    expect(
      describeCall({ id: "1", name: "read_file", arguments: '{"path":"/workspace/a.go"}' }),
    ).toBe("read_file /workspace/a.go");
    expect(
      describeCall({ id: "1", name: "git", arguments: '{"action":"commit","message":"fix"}' }),
    ).toBe("git commit — fix");
    expect(
      describeCall({ id: "1", name: "fetch_url", arguments: '{"url":"https://example.com"}' }),
    ).toBe("fetch https://example.com");
    expect(describeCall({ id: "1", name: "mystery", arguments: "{" })).toBe("mystery");
  });
});
