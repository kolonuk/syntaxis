// SPDX-License-Identifier: GPL-2.0-or-later
import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, _setFetch, api, errText, setCsrf } from "./api";
import { parseSSE } from "./sse";
import { clearUserData, enqueue, flush, loadQueue } from "./queue";
import { applyTheme, loadPref, nextPref, resolveTheme } from "./theme";
import { ago, bytes, compact, pct } from "./format";

function mockFetch(
  handler: (url: string, init: RequestInit) => { status?: number; body?: unknown },
) {
  const calls: { url: string; init: RequestInit }[] = [];
  _setFetch((async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    const r = handler(url, init);
    const text =
      r.body === undefined ? "" : typeof r.body === "string" ? r.body : JSON.stringify(r.body);
    return new Response(text, { status: r.status ?? 200 });
  }) as typeof fetch);
  return calls;
}

describe("api", () => {
  beforeEach(() => setCsrf(""));
  it("sends CSRF on unsafe methods only, same-origin credentials", async () => {
    const calls = mockFetch(() => ({ body: { ok: true } }));
    setCsrf("tok");
    await api("GET", "/me");
    await api("POST", "/x", { a: 1 });
    expect((calls[0]!.init.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
    expect((calls[1]!.init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok");
    expect(calls[1]!.init.credentials).toBe("same-origin");
    expect(calls[1]!.url).toBe("/api/x");
    expect(calls[1]!.init.body).toBe('{"a":1}');
  });
  it("turns error responses into ApiError with the server message", async () => {
    mockFetch(() => ({ status: 403, body: { error: "invalid CSRF token" } }));
    await expect(api("POST", "/x", {})).rejects.toMatchObject({
      status: 403,
      message: "invalid CSRF token",
    });
  });
  it("handles empty and non-JSON error bodies", async () => {
    mockFetch(() => ({ status: 500, body: "boom" }));
    await expect(api("GET", "/x")).rejects.toBeInstanceOf(ApiError);
    mockFetch(() => ({ status: 200 }));
    expect(await api("GET", "/x")).toBeNull();
  });
  it("maps machine codes to friendly text", () => {
    expect(errText(new ApiError(403, "password_expired"))).toMatch(/expired/);
    expect(errText(new ApiError(403, "2fa_required"))).toMatch(/Two-factor/);
    expect(errText(new Error("x"))).toBe("x");
    expect(errText(42)).toBe("Something went wrong");
  });
});

describe("parseSSE", () => {
  it("parses complete events and keeps the partial tail", () => {
    const { events, rest } = parseSSE(
      ': connected\n\nevent: tool_call\ndata: {"a":1}\n\nevent: status\ndata: {"b"',
    );
    expect(events).toEqual([{ event: "tool_call", data: '{"a":1}' }]);
    expect(rest).toBe('event: status\ndata: {"b"');
  });
  it("joins multi-line data and surfaces end/idle events", () => {
    const { events } = parseSSE(
      "event: end\ndata: {}\n\nevent: idle\ndata: {}\n\ndata: a\ndata: b\n\n",
    );
    expect(events.map((e) => e.event)).toEqual(["end", "idle", "message"]);
    expect(events[2]!.data).toBe("a\nb");
  });
  it("ignores comments/pings", () => {
    expect(parseSSE(": ping\n\n").events).toEqual([]);
  });
});

describe("offline queue", () => {
  beforeEach(() => localStorage.clear());
  it("persists per user and preserves order", () => {
    enqueue("a@x", { sessionId: "s1", text: "one" });
    enqueue("a@x", { sessionId: "s1", text: "two" });
    enqueue("b@x", { sessionId: "s2", text: "other" });
    expect(loadQueue("a@x").map((q) => q.text)).toEqual(["one", "two"]);
    expect(loadQueue("b@x")).toHaveLength(1);
  });
  it("flush sends in order and stops at the first failure, keeping the rest", async () => {
    enqueue("u", { sessionId: "s", text: "1" });
    enqueue("u", { sessionId: "s", text: "2" });
    enqueue("u", { sessionId: "s", text: "3" });
    const sent: string[] = [];
    const n = await flush("u", async (q) => {
      if (q.text === "2") throw new Error("offline");
      sent.push(q.text);
    });
    expect(n).toBe(1);
    expect(sent).toEqual(["1"]);
    expect(loadQueue("u").map((q) => q.text)).toEqual(["2", "3"]);
    expect(await flush("u", async (q) => void sent.push(q.text))).toBe(2);
    expect(loadQueue("u")).toEqual([]);
    expect(localStorage.length).toBe(0);
  });
  it("clears every user's outbox on logout", () => {
    enqueue("a", { sessionId: "s", text: "x" });
    enqueue("b", { sessionId: "s", text: "y" });
    localStorage.setItem("keep", "1");
    clearUserData();
    expect(localStorage.getItem("keep")).toBe("1");
    expect(loadQueue("a")).toEqual([]);
    expect(loadQueue("b")).toEqual([]);
  });
  it("survives corrupted storage", () => {
    localStorage.setItem("syx.outbox.u", "{not json");
    expect(loadQueue("u")).toEqual([]);
  });
});

describe("theme", () => {
  beforeEach(() => localStorage.clear());
  it("resolves system preference and explicit choices", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });
  it("cycles and persists explicit choices only", () => {
    expect(nextPref("system")).toBe("light");
    expect(nextPref("light")).toBe("dark");
    expect(nextPref("dark")).toBe("system");
    applyTheme("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(loadPref()).toBe("dark");
    applyTheme("system");
    expect(loadPref()).toBe("system");
  });
});

describe("format", () => {
  it("pct clamps and tolerates unlimited", () => {
    expect(pct(50, 100)).toBe(50);
    expect(pct(500, 100)).toBe(100);
    expect(pct(5, 0)).toBe(0);
  });
  it("compact numbers", () => {
    expect(compact(999)).toBe("999");
    expect(compact(1500)).toBe("1.5k");
    expect(compact(2_000_000)).toBe("2M");
  });
  it("ago and bytes", () => {
    const now = Date.parse("2026-01-01T12:00:00Z");
    expect(ago("2026-01-01T11:59:50Z", now)).toBe("just now");
    expect(ago("2026-01-01T11:30:00Z", now)).toBe("30 min ago");
    expect(ago("2025-12-30T12:00:00Z", now)).toBe("2 d ago");
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1536)).toBe("1.5 KB");
  });
});
