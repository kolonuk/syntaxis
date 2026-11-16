// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { get, put, errText } from "../lib/api";
import { Badge, Banner, Card, EmptyState, Skeleton, Toggle, useToast } from "../ui/ui";

interface Server {
  name: string;
  package: string;
  enabled: boolean;
  missing: string[];
  ready: boolean;
}

/** Extra tools (MCP servers) your admin has approved. Each runs only for you, with your own credentials. */
export function McpTools() {
  const toast = useToast();
  const [list, setList] = useState<Server[] | null>(null);
  const load = useCallback(() => {
    void get<Server[]>("/mcp").then(setList);
  }, []);
  useEffect(load, [load]);
  if (!list) return <Skeleton lines={2} />;
  if (list.length === 0)
    return (
      <EmptyState
        title="No extra tools yet"
        hint="Your admin can approve MCP servers, such as the official GitHub server, for you to switch on here."
      />
    );
  return (
    <div className="stack">
      <Banner>
        These tools are approved by your admin and run in their own sandbox, only when you switch
        them on, with your own connected credentials. State-changing tools ask before they run.
      </Banner>
      {list.map((s) => (
        <Card key={s.name} title={s.name}>
          <div className="row">
            <span className="muted grow">{s.package}</span>
            {!s.ready && <Badge tone="warn">needs {s.missing.join(", ")}</Badge>}
            <Toggle
              label="On"
              checked={s.enabled}
              onChange={async (v) => {
                try {
                  await put("/mcp", { name: s.name, enabled: v });
                  load();
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            />
          </div>
          {!s.ready && (
            <p className="muted">
              Connect the matching service in Settings, Connections first; until then it will not
              start.
            </p>
          )}
        </Card>
      ))}
    </div>
  );
}
