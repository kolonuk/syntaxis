// SPDX-License-Identifier: GPL-2.0-or-later
// Small pieces the admin tabs share.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { get, errText } from "../lib/api";
import type { Tone } from "../ui/ui";

/** Where the project's documentation lives. Links to it are public pages, never an instance address. */
export const DOCS = "https://github.com/kolonuk/syntaxis/blob/main/docs";

/** A health severity as a badge tone. */
export const tone = (s: string): Tone =>
  s === "error" ? "danger" : s === "warning" ? "warn" : "info";

/** Loads a path from the admin API now and again whenever reload() is called. */
export function useLoad<T>(path: string) {
  const [data, setData] = useState<T | null>(null);
  const [err, setErr] = useState("");
  const reload = useCallback(() => {
    get<T>(path)
      .then(setData)
      .catch((e) => setErr(errText(e)));
  }, [path]);
  useEffect(reload, [reload]);
  return { data, err, reload };
}

/** A plain table of records, one column per name in cols. */
export function Table({
  rows,
  cols,
  action,
}: {
  rows: Record<string, unknown>[];
  cols: string[];
  action?: (row: Record<string, unknown>) => ReactNode;
}) {
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label="Table">
      <table>
        <thead>
          <tr>
            {cols.map((c) => (
              <th key={c}>{c}</th>
            ))}
            {action && <th aria-label="Actions" />}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {cols.map((c) => (
                <td key={c}>{String(r[c] ?? "")}</td>
              ))}
              {action && <td>{action(r)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** "3 minutes ago" style text for a past time. */
export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}
