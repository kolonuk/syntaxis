// SPDX-License-Identifier: GPL-2.0-or-later

export interface StreamEvent {
  kind: string;
  data: Record<string, unknown>;
}

/** Parses a text/event-stream chunk buffer into events; returns the unconsumed rest. */
export function parseSSE(buffer: string): {
  events: { event: string; data: string }[];
  rest: string;
} {
  const events: { event: string; data: string }[] = [];
  const parts = buffer.split("\n\n");
  const rest = parts.pop() ?? "";
  for (const block of parts) {
    let event = "message";
    const data: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith(":")) continue;
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
    }
    if (data.length || event !== "message") events.push({ event, data: data.join("\n") });
  }
  return { events, rest };
}

/** Reads the event stream of a session; calls onEvent for each; resolves at stream end. */
export async function streamEvents(
  sessionId: string,
  onEvent: (e: StreamEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const res = await fetch(`/api/sessions/${sessionId}/events`, {
    credentials: "same-origin",
    signal,
  });
  if (!res.ok || !res.body) throw new Error(`stream failed (${res.status})`);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    const { events, rest } = parseSSE(buf);
    buf = rest;
    for (const ev of events) {
      if (ev.event === "end") return;
      if (ev.event === "idle") continue;
      try {
        onEvent({ kind: ev.event, data: JSON.parse(ev.data).data ?? {} });
      } catch {
        /* ignore malformed event */
      }
    }
  }
}
