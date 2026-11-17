// SPDX-License-Identifier: GPL-2.0-or-later
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { crumbs } from "./Files";
import { TodoList } from "./Todos";
import { describeCall } from "./Chat";

describe("file browser breadcrumb", () => {
  it("leads back to each folder", () => {
    expect(crumbs("/workspace")).toEqual([{ label: "workspace", path: "/workspace" }]);
    expect(crumbs("/workspace/src/app")).toEqual([
      { label: "workspace", path: "/workspace" },
      { label: "src", path: "/workspace/src" },
      { label: "app", path: "/workspace/src/app" },
    ]);
  });
});

describe("task list", () => {
  it("shows progress and says each item's state to a screen reader", () => {
    render(
      <TodoList
        todos={[
          { content: "read the code", status: "completed" },
          { content: "fix the bug", status: "in_progress" },
          { content: "run the tests", status: "pending" },
        ]}
      />,
    );
    expect(screen.getByText("Progress 1 of 3")).toBeTruthy();
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("1");
    expect(screen.getByText("done:")).toBeTruthy();
    expect(screen.getByText("in progress:")).toBeTruthy();
    expect(screen.getAllByText(/fix the bug/).length).toBeGreaterThan(0);
  });
  it("shows nothing for an empty list", () => {
    const { container } = render(<TodoList todos={[]} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("describeCall for the new tools", () => {
  it("names web searches and notes", () => {
    expect(describeCall({ id: "1", name: "web_search", arguments: '{"query":"go slices"}' })).toBe(
      "web search: go slices",
    );
    expect(
      describeCall({ id: "1", name: "remember", arguments: '{"note":"use tabs","scope":"user"}' }),
    ).toBe("remember: use tabs");
    expect(
      describeCall({ id: "1", name: "apply_patch", arguments: '{"path":"a.go","edits":[]}' }),
    ).toBe("apply_patch a.go");
  });
});
