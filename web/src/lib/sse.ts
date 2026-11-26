// SPDX-License-Identifier: GPL-2.0-or-later

export interface StreamEvent {
  kind: string;
  data: Record<string, unknown>;
}

interface RawEvent {
  event: string;
  data: string;
  id?: string;
}

/** Parses a text/event-stream chunk buffer into events; returns the unconsumed rest. */
export function parseSSE(buffer: string): { events: RawEvent[]; rest: string } {
  const events: RawEvent[] = [];
  const parts = buffer.split("\n\n");
  const rest = parts.pop() ?? "";
  for (const block of parts) {
    let event = "message";
    let id: string | undefined;
    const data: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith(":")) continue;
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("id:")) id = line.slice(3).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
    }
    if (data.length || event !== "message") {
      const ev: RawEvent = { event, data: data.join("\n") };
      if (id) ev.id = id;
      events.push(ev);
    }
  }
  return { events, rest };
}

/** How a read of the stream ended: the run finished, there was no run, or the connection dropped. */
export type StreamEnd = "end" | "idle" | "dropped";

export interface StreamOptions {
  /** The id of the last event already handled; the server sends only what came after it. */
  lastId?: string;
  /** Called with each event's id, so a reconnect can resume after it. */
  onId?: (id: string) => void;
  fetchFn?: typeof fetch;
}

/** Reads the event stream of a session once; calls onEvent for each event. */
export async function streamEvents(
  sessionId: string,
  onEvent: (e: StreamEvent) => void,
  signal: AbortSignal,
  opts: StreamOptions = {},
): Promise<StreamEnd> {
  const doFetch = opts.fetchFn ?? fetch;
  const headers: Record<string, string> = {};
  if (opts.lastId) headers["Last-Event-ID"] = opts.lastId;
  const res = await doFetch(`/api/sessions/${sessionId}/events`, {
    credentials: "same-origin",
    signal,
    headers,
  });
  if (!res.ok || !res.body) throw new Error(`stream failed (${res.status})`);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return "dropped";
    buf += dec.decode(value, { stream: true });
    const { events, rest } = parseSSE(buf);
    buf = rest;
    for (const ev of events) {
      if (ev.event === "end") return "end";
      if (ev.event === "idle") return "idle";
      if (ev.id) opts.onId?.(ev.id);
      try {
        onEvent({ kind: ev.event, data: JSON.parse(ev.data).data ?? {} });
      } catch {
        /* ignore malformed event */
      }
    }
  }
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });

/**
 * Follows a session's events until the run ends, and reconnects when the connection drops (a phone
 * going to sleep, a proxy closing a long request), resuming after the last event seen so nothing is
 * lost or shown twice. Resolves with how it ended, or "dropped" when aborted.
 */
export async function followEvents(
  sessionId: string,
  onEvent: (e: StreamEvent) => void,
  signal: AbortSignal,
  opts: { fetchFn?: typeof fetch; retryMs?: number; onReconnect?: () => void } = {},
): Promise<StreamEnd> {
  let lastId = "";
  let failures = 0;
  const base = opts.retryMs ?? 1000;
  while (!signal.aborted) {
    try {
      const r = await streamEvents(sessionId, onEvent, signal, {
        lastId,
        onId: (id) => {
          lastId = id;
          failures = 0;
        },
        fetchFn: opts.fetchFn,
      });
      if (r !== "dropped") return r;
    } catch {
      /* fall through to the retry */
    }
    if (signal.aborted) break;
    failures++;
    await sleep(Math.min(base * 2 ** Math.min(failures, 4), 15000), signal);
    if (!signal.aborted) opts.onReconnect?.();
  }
  return "dropped";
}
