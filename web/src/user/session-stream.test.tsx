// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import { followEvents, parseSSE, type StreamEvent } from "../lib/sse";
import { Chat, stampText, type Session } from "./Chat";
import { TodoList } from "./Todos";
import { ToastProvider } from "../ui/ui";

afterEach(cleanup);

const enc = new TextEncoder();

/** A response whose body is the given chunks, then closed (a connection that drops) or left open. */
function stream(chunks: string[], keepOpen = false): Response {
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const ch of chunks) c.enqueue(enc.encode(ch));
      if (!keepOpen) c.close();
    },
  });
  return new Response(body, { status: 200 });
}
const ev = (id: string, kind: string, data: object = {}) =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify({ kind, data })}\n\n`;

describe("event stream", () => {
  it("reads the id of each event", () => {
    const { events } = parseSSE('id: 7-3\nevent: phase\ndata: {"a":1}\n\n');
    expect(events).toEqual([{ event: "phase", data: '{"a":1}', id: "7-3" }]);
  });

  it("reconnects after a dropped connection and resumes after the last event seen", async () => {
    const seen: string[] = [];
    const headers: (string | null)[] = [];
    let n = 0;
    const fetchFn = vi.fn(async (_url: string | URL | Request, init?: RequestInit) => {
      headers.push(new Headers(init?.headers).get("Last-Event-ID"));
      n++;
      if (n === 1) return stream([ev("9-1", "progress", { message: "one" }), ev("9-2", "phase")]);
      return stream([ev("9-3", "progress", { message: "three" }), "event: end\ndata: {}\n\n"]);
    }) as unknown as typeof fetch;
    const how = await followEvents(
      "s1",
      (e: StreamEvent) => seen.push(`${e.kind}:${String(e.data.message ?? "")}`),
      new AbortController().signal,
      { fetchFn, retryMs: 1 },
    );
    expect(how).toBe("end");
    expect(headers).toEqual([null, "9-2"]);
    expect(seen).toEqual(["progress:one", "phase:", "progress:three"]);
  });

  it("stops when there is no run", async () => {
    const fetchFn = (async () => stream(["event: idle\ndata: {}\n\n"])) as unknown as typeof fetch;
    expect(
      await followEvents("s1", () => undefined, new AbortController().signal, { fetchFn }),
    ).toBe("idle");
  });

  it("keeps trying when the server is unreachable, and stops when told to", async () => {
    const ac = new AbortController();
    let n = 0;
    const fetchFn = (async () => {
      if (++n === 3) ac.abort();
      throw new Error("offline");
    }) as unknown as typeof fetch;
    await followEvents("s1", () => undefined, ac.signal, { fetchFn, retryMs: 1 });
    expect(n).toBe(3);
  });
});

describe("message times", () => {
  const now = new Date("2026-11-26T15:00:00");
  it("shows only the time for today", () => {
    expect(stampText("2026-11-26T09:05:00", now)).toMatch(/9:05|09:05/);
    expect(stampText("2026-11-26T09:05:00", now)).not.toMatch(/Nov/);
  });
  it("adds the date for an earlier day, and the year for an earlier year", () => {
    expect(stampText("2026-11-20T09:05:00", now)).toMatch(/20/);
    expect(stampText("2025-11-20T09:05:00", now)).toMatch(/2025/);
  });
  it("shows nothing for a bad date", () => {
    expect(stampText("not a date", now)).toBe("");
  });
});

describe("task list", () => {
  it("shows the progress bar outside the list, so it stays visible when the list is closed", () => {
    const { container } = render(
      <TodoList
        todos={[
          { content: "a", status: "completed" },
          { content: "b", status: "completed" },
        ]}
      />,
    );
    const details = container.querySelector("details")!;
    expect(details.open).toBe(false); // everything is done, so it starts closed
    expect(details.querySelector("summary [role=progressbar]")).toBeTruthy();
  });
});

const session: Session = {
  id: "s1",
  title: "Fix it",
  model: "prov/m1",
  state: "idle",
  autonomy: "ask",
};

/** A server for the chat screen: one open event connection, everything else empty. */
function fakeServer() {
  let push: (chunk: string) => void = () => undefined;
  let eventCalls = 0;
  const events = () => {
    eventCalls++;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        push = (chunk) => c.enqueue(enc.encode(chunk));
      },
    });
    return new Response(body, { status: 200 });
  };
  const impl = (async (url: string | URL | Request) => {
    const u = String(url);
    if (u.endsWith("/events")) return events();
    const body = /messages|commands|mcp|hooks|checkpoints|todos$/.test(u) ? "[]" : "{}";
    return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
  }) as unknown as typeof fetch;
  return { impl, send: (c: string) => push(c), calls: () => eventCalls };
}

function chat(onChanged: () => void) {
  return (
    <ToastProvider>
      <Chat
        session={session}
        userKey="u@example.com"
        onChanged={onChanged}
        onDeleted={() => undefined}
        panelOpen
        setPanelOpen={() => undefined}
      />
    </ToastProvider>
  );
}

describe("the chat screen follows a run", () => {
  it("is not rebuilt when its parent re-renders, and shows sandbox progress while it waits", async () => {
    const srv = fakeServer();
    _setFetch(srv.impl);
    const realFetch = globalThis.fetch;
    globalThis.fetch = srv.impl;
    try {
      const { rerender } = render(chat(() => undefined));
      await waitFor(() => expect(srv.calls()).toBe(1));
      await act(async () => {
        srv.send(ev("1-1", "progress", { message: "Creating a new sandbox" }));
        srv.send(ev("1-2", "phase", { phase: "preparing", detail: "Creating a new sandbox" }));
        srv.send(ev("1-3", "context", { used: 400, window: 8000, percent: 5 }));
      });
      // the parent hands over a new onChanged each render, as the real one does
      for (let i = 0; i < 5; i++) rerender(chat(() => undefined));
      await act(async () => undefined);
      expect(srv.calls()).toBe(1); // still the same connection
      expect(screen.getAllByText(/Getting your sandbox ready/).length).toBeGreaterThan(0);
      // the output panel shows the step, not an empty box
      await act(async () => {
        screen.getAllByRole("tab", { name: "Output" })[0]?.click();
      });
      expect(document.querySelector("pre.output")?.textContent).toContain("Creating a new sandbox");
    } finally {
      globalThis.fetch = realFetch;
    }
  });

  it("shows the run as going on when events arrive for a session the list called idle", async () => {
    const srv = fakeServer();
    _setFetch(srv.impl);
    const realFetch = globalThis.fetch;
    globalThis.fetch = srv.impl;
    try {
      render(chat(() => undefined));
      await waitFor(() => expect(srv.calls()).toBe(1));
      expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
      await act(async () => {
        srv.send(ev("1-1", "phase", { phase: "thinking" }));
      });
      await waitFor(() => expect(screen.getAllByRole("button", { name: "Stop" }).length).toBe(2));
    } finally {
      globalThis.fetch = realFetch;
    }
  });
});
