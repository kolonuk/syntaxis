// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { errText, get, post } from "../lib/api";
import { Banner, Button, useToast } from "../ui/ui";

interface Hook {
  event: string;
  tools?: string[];
  command: string;
  timeout_seconds?: number;
}

interface RepoHooksInfo {
  present?: boolean;
  hash?: string;
  hooks?: Hook[];
  error?: string;
  trusted?: boolean;
}

/**
 * A repository can ship hooks (.syntaxis/hooks.json): commands that run around the agent's tool
 * calls. They do not run until the user has read them and trusted this exact file.
 */
export function RepoHooks({ sessionId, running }: { sessionId: string; running: boolean }) {
  const toast = useToast();
  const [info, setInfo] = useState<RepoHooksInfo>({});
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    get<RepoHooksInfo>(`/sessions/${sessionId}/repo-hooks`)
      .then(setInfo)
      .catch(() => setInfo({}));
  }, [sessionId]);
  useEffect(() => {
    if (!running) load();
  }, [load, running]);

  if (!info.present || info.trusted || !info.hooks || info.hooks.length === 0) return null;

  async function trust() {
    setBusy(true);
    try {
      await post(`/sessions/${sessionId}/repo-hooks/trust`, { hash: info.hash });
      toast("Hooks trusted for this repository. They run from the next message.", "success");
      load();
    } catch (x) {
      toast(errText(x), "error");
      load();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Banner tone="warn">
      <p>
        This repository wants to run {info.hooks.length} hook
        {info.hooks.length === 1 ? "" : "s"} around the agent&apos;s tool calls (
        <code>.syntaxis/hooks.json</code>). They do not run until you trust them.
      </p>
      <ul>
        {info.hooks.map((h, i) => (
          <li key={i}>
            <code>{h.command}</code> <span className="muted">{h.event}</span>
            {h.tools && h.tools.length > 0 ? (
              <span className="muted"> on {h.tools.join(", ")}</span>
            ) : null}
          </li>
        ))}
      </ul>
      <Button onClick={() => void trust()} loading={busy} disabled={running}>
        Trust these hooks
      </Button>
    </Banner>
  );
}
