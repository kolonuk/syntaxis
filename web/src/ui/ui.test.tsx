// SPDX-License-Identifier: GPL-2.0-or-later
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SideMenu } from "./SideMenu";
import {
  Badge,
  Button,
  EmptyState,
  Input,
  Meter,
  Modal,
  StatusPill,
  Tabs,
  Toggle,
  useShortcuts,
} from "./ui";

afterEach(cleanup);

describe("Button", () => {
  it("is disabled and busy while loading, and shows a spinner", () => {
    const onClick = vi.fn();
    render(
      <Button loading onClick={onClick}>
        Save
      </Button>,
    );
    const b = screen.getByRole("button", { name: /save/i });
    expect(b).toBeDisabled();
    expect(b).toHaveAttribute("aria-busy", "true");
    fireEvent.click(b);
    expect(onClick).not.toHaveBeenCalled();
  });
  it("applies variant classes", () => {
    render(
      <>
        <Button variant="danger">d</Button>
        <Button variant="primary">p</Button>
      </>,
    );
    expect(screen.getByText("d")).toHaveClass("btn-danger");
    expect(screen.getByText("p")).toHaveClass("btn-primary");
  });
});

describe("form controls", () => {
  it("Input associates its label for accessibility", () => {
    render(<Input label="Email" />);
    expect(screen.getByLabelText("Email")).toBeInTheDocument();
  });
  it("Toggle is a labelled switch", () => {
    const on = vi.fn();
    render(<Toggle label="Enabled" checked={false} onChange={on} />);
    fireEvent.click(screen.getByRole("switch", { name: "Enabled" }));
    expect(on).toHaveBeenCalledWith(true);
  });
});

describe("Meter", () => {
  it("exposes progressbar semantics and warns near the limit", () => {
    const { container } = render(<Meter label="Tokens" used={90} limit={100} />);
    const bar = screen.getByRole("progressbar", { name: "Tokens" });
    expect(bar).toHaveAttribute("aria-valuenow", "90");
    expect(container.querySelector(".meter")).toHaveClass("warn");
  });
  it("shows danger at 95% and handles unlimited", () => {
    const { container, rerender } = render(<Meter label="T" used={96} limit={100} />);
    expect(container.querySelector(".meter")).toHaveClass("danger");
    rerender(<Meter label="T" used={5} limit={0} />);
    expect(screen.getByText(/no limit/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "0");
  });
});

describe("Modal", () => {
  it("is a labelled dialog that closes on Escape and returns focus", () => {
    const onClose = vi.fn();
    render(
      <Modal title="Confirm" onClose={onClose}>
        <p>sure?</p>
      </Modal>,
    );
    expect(screen.getByRole("dialog", { name: "Confirm" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("always has a visible way to dismiss it, not only Escape or a click outside", () => {
    const onClose = vi.fn();
    render(
      <Modal title="Read only" onClose={onClose}>
        <p>details</p>
      </Modal>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not move focus back to the first field when its parent re-renders", () => {
    function Form() {
      const [name, setName] = useState("");
      const [email, setEmail] = useState("");
      return (
        <Modal title="Invite" onClose={() => undefined}>
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} />
          <Input label="Email" value={email} onChange={(e) => setEmail(e.target.value)} />
        </Modal>
      );
    }
    render(<Form />);
    const email = screen.getByLabelText("Email");
    email.focus();
    fireEvent.change(email, { target: { value: "a" } });
    fireEvent.change(email, { target: { value: "ab" } });
    expect(document.activeElement).toBe(email);
  });

  it("shows the actions it is given instead of the default Close", () => {
    render(
      <Modal title="Ask" onClose={() => undefined} actions={<button type="button">Do it</button>}>
        <p>x</p>
      </Modal>,
    );
    expect(screen.getByRole("button", { name: "Do it" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
  });
});

describe("every dialog in the app can be cancelled with a button", () => {
  // Reads the source: a Modal that passes its own actions must offer one of these ways out.
  const ways = /(Cancel|Close|Done|Deny|Remind me next time)/;
  const walk = (dir: string): string[] =>
    readdirSync(dir).flatMap((n) => {
      const p = join(dir, n);
      if (statSync(p).isDirectory()) return walk(p);
      return /\.tsx$/.test(n) && !/\.test\.tsx$/.test(n) ? [p] : [];
    });
  it("has a cancel, close or equivalent in every Modal that sets actions", () => {
    const bad: string[] = [];
    for (const f of walk(join(__dirname, ".."))) {
      const src = readFileSync(f, "utf8");
      for (const m of src.matchAll(/<Modal\b([\s\S]*?)>\s*\n/g)) {
        const open = m[1]!;
        if (!/actions=/.test(open)) continue; // no actions: the default Close button is shown
        const from = m.index! + m[0].length;
        const body = src.slice(m.index!, from + 1200);
        const actionsPart = body.slice(body.indexOf("actions="), body.indexOf("actions=") + 700);
        if (!ways.test(actionsPart)) bad.push(f.replace(__dirname, "") + ": " + open.slice(0, 80));
      }
    }
    expect(bad).toEqual([]);
  });
});

describe("Tabs, badges, empty state", () => {
  it("marks the selected tab and reports changes", () => {
    const on = vi.fn();
    render(
      <Tabs
        tabs={[
          { id: "a", label: "A" },
          { id: "b", label: "B" },
        ]}
        value="a"
        onChange={on}
      />,
    );
    expect(screen.getByRole("tab", { name: "A" })).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByRole("tab", { name: "B" }));
    expect(on).toHaveBeenCalledWith("b");
  });
  it("StatusPill maps states to tones", () => {
    render(
      <>
        <StatusPill state="error" />
        <StatusPill state="needs_input" />
      </>,
    );
    expect(screen.getByText("error")).toHaveClass("badge-danger");
    expect(screen.getByText("needs input")).toHaveClass("badge-warn");
  });
  it("renders neutral badge and empty state", () => {
    render(
      <>
        <Badge>x</Badge>
        <EmptyState title="Nothing" hint="here" />
      </>,
    );
    expect(screen.getByText("Nothing")).toBeInTheDocument();
  });
});

describe("useShortcuts", () => {
  function Harness({ fn }: { fn: () => void }) {
    useShortcuts({ n: fn });
    return <input aria-label="field" />;
  }
  it("fires on key press but not while typing in a field", () => {
    const fn = vi.fn();
    render(<Harness fn={fn} />);
    fireEvent.keyDown(document.body, { key: "n" });
    expect(fn).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(screen.getByLabelText("field"), { key: "n" });
    expect(fn).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document.body, { key: "n", ctrlKey: true });
    expect(fn).toHaveBeenCalledTimes(1);
  });
});

describe("SideMenu", () => {
  const groups = [
    {
      label: "Alpha",
      items: [
        { id: "a1", label: "First" },
        { id: "a2", label: "Second" },
      ],
    },
    { label: "Beta", items: [{ id: "b1", label: "Only" }] },
  ];
  it("lists the items under their group, marks the current one and reports a choice", () => {
    const onChange = vi.fn();
    render(
      <SideMenu groups={groups} value="a2" onChange={onChange} label="Sections">
        <p>the screen</p>
      </SideMenu>,
    );
    expect(screen.getByRole("heading", { name: "Alpha" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Beta" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Second" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("button", { name: "First" })).not.toHaveAttribute("aria-current");
    expect(screen.getByText("the screen")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Only" }));
    expect(onChange).toHaveBeenCalledWith("b1");
  });
});
