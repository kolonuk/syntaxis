// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

/** Read-only activity terminal: shows commands the agent runs and their output. */
export function ActivityTerminal({ lines }: { lines: string[] }) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm | null>(null);
  const written = useRef(0);

  useEffect(() => {
    if (!host.current) return;
    const t = new XTerm({
      convertEol: true,
      disableStdin: true,
      fontSize: 12,
      cursorBlink: false,
      scrollback: 5000,
      theme: { background: "#0b0d12" },
    });
    const fit = new FitAddon();
    t.loadAddon(fit);
    t.open(host.current);
    try {
      fit.fit();
    } catch {
      /* not visible yet */
    }
    term.current = t;
    written.current = 0;
    const onResize = () => {
      try {
        fit.fit();
      } catch {
        /* ignore */
      }
    };
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      t.dispose();
    };
  }, []);

  useEffect(() => {
    const t = term.current;
    if (!t) return;
    if (lines.length < written.current) {
      t.reset();
      written.current = 0;
    }
    for (let i = written.current; i < lines.length; i++) t.writeln(lines[i] ?? "");
    written.current = lines.length;
  }, [lines]);

  return <div className="terminal" ref={host} role="log" aria-label="Agent terminal output" />;
}
