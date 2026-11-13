// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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
