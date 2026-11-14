// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, get } from "../lib/api";
import { Button } from "../ui/ui";

type State = "idle" | "connecting" | "open" | "closed";

const KEYS: { label: string; seq: string; aria: string }[] = [
  { label: "Esc", seq: "\x1b", aria: "Escape" },
  { label: "Tab", seq: "\t", aria: "Tab" },
  { label: "^C", seq: "\x03", aria: "Control C" },
  { label: "^D", seq: "\x04", aria: "Control D" },
  { label: "↑", seq: "\x1b[A", aria: "Up" },
  { label: "↓", seq: "\x1b[B", aria: "Down" },
];

/**
 * An interactive shell inside this session's sandbox. xterm.js is loaded only
 * when the tab is first opened. The same limits and network policy apply as for
 * the agent's own commands.
 */
export default function TerminalView({
  sessionId,
  visible,
  blocked,
}: {
  sessionId: string;
  visible: boolean;
  /** The agent is working in the sandbox right now. */
  blocked: boolean;
}) {
  const host = useRef<HTMLDivElement>(null);
  const ws = useRef<WebSocket | null>(null);
  const fit = useRef<(() => void) | null>(null);
  const term = useRef<import("@xterm/xterm").Terminal | null>(null);
  const [state, setState] = useState<State>("idle");
  const [msg, setMsg] = useState("");

  const send = useCallback((s: string) => {
    if (ws.current?.readyState === WebSocket.OPEN) ws.current.send(new TextEncoder().encode(s));
  }, []);

  const connect = useCallback(async () => {
    setMsg("");
    setState("connecting");
    // A plain request runs the same checks as the WebSocket and explains a refusal;
    // a WebSocket failure itself carries no reason in the browser.
    try {
      await get(`/sessions/${sessionId}/terminal`);
    } catch (x) {
      if (!(x instanceof ApiError && x.status === 426)) {
        setMsg(x instanceof Error ? x.message : "Could not open the terminal.");
        setState("closed");
        return;
      }
    }
    const [{ Terminal }, { FitAddon }] = await Promise.all([
      import("@xterm/xterm"),
      import("@xterm/addon-fit"),
      import("@xterm/xterm/css/xterm.css"),
    ]);
    if (!host.current) return;
    term.current?.dispose();
    const t = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 14,
      scrollback: 5000,
      theme: { background: "#0f1115", foreground: "#e6e8ec" },
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(host.current);
    f.fit();
    term.current = t;
    fit.current = () => {
      try {
        f.fit();
      } catch {
        /* hidden */
      }
    };
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const sock = new WebSocket(
      `${proto}://${location.host}/api/sessions/${sessionId}/terminal?rows=${t.rows}&cols=${t.cols}`,
    );
    sock.binaryType = "arraybuffer";
    ws.current = sock;
    sock.onopen = () => {
      setState("open");
      t.focus();
    };
    sock.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) t.write(new Uint8Array(e.data));
    };
    sock.onclose = (e) => {
      setState("closed");
      setMsg(e.reason || "The terminal was closed.");
    };
    t.onData((d) => send(d));
    t.onResize(({ rows, cols }) => {
      if (sock.readyState === WebSocket.OPEN)
        sock.send(JSON.stringify({ type: "resize", rows, cols }));
    });
  }, [sessionId, send]);

  // connect the first time the tab is shown; never while the agent is working
  useEffect(() => {
    if (visible && state === "idle" && !blocked) void connect();
  }, [visible, state, blocked, connect]);

  // refit on size changes and when the tab becomes visible
  useEffect(() => {
    if (!visible || !host.current) return;
    const ro = new ResizeObserver(() => fit.current?.());
    ro.observe(host.current);
    fit.current?.();
    return () => ro.disconnect();
  }, [visible, state]);

  useEffect(
    () => () => {
      ws.current?.close();
      term.current?.dispose();
    },
    [],
  );

  // the agent took over: the server closes the terminal, so drop back to idle
  useEffect(() => {
    if (blocked && state === "open") ws.current?.close();
  }, [blocked, state]);

  return (
    <div className="terminal-pane" hidden={!visible}>
      {blocked && state !== "open" && (
        <p className="muted terminal-note">
          The agent is working in this sandbox. The terminal is available when it finishes, or press
          Stop.
        </p>
      )}
      {state === "closed" && !blocked && (
        <div className="terminal-note row">
          <span className="muted grow">{msg}</span>
          <Button
            onClick={() => {
              term.current?.dispose();
              term.current = null;
              setState("idle");
            }}
          >
            Reconnect
          </Button>
        </div>
      )}
      {state === "connecting" && <p className="muted terminal-note">Opening the shell…</p>}
      <div className="terminal-host" ref={host} aria-label="Terminal" />
      {state === "open" && (
        <div className="terminal-keys" role="toolbar" aria-label="Terminal keys">
          {KEYS.map((k) => (
            <button
              key={k.label}
              type="button"
              className="btn btn-ghost"
              aria-label={k.aria}
              onClick={() => {
                send(k.seq);
                term.current?.focus();
              }}
            >
              {k.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
