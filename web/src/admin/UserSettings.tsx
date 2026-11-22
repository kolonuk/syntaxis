// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { del, errText, get, put } from "../lib/api";
import { Button, Modal, Skeleton, TextArea, useToast } from "../ui/ui";

interface Settings {
  user: { id: number; email: string; name: string };
  notes: { id: number; scope: string; scope_key: string; content: string }[];
  commands: unknown[];
  hooks: string;
  trusted_repo_hooks: number;
  connections: { kind: string; account: string; detail: string }[];
  mcp: string[];
  automations: {
    id: string;
    name: string;
    enabled: boolean;
    trigger: string;
    last_status: string;
  }[];
  tokens: { id: number; name: string; prefix: string; scope: string; revoked: boolean }[];
  secret_names: string[];
}

/**
 * Everything one user has set up, for the admin to read and change. Credential values are never
 * here, only the names of the connections and secrets. Each change is recorded in the audit log.
 */
export function UserSettingsDialog({
  user,
  onClose,
}: {
  user: { id: number; name: string };
  onClose: () => void;
}) {
  const toast = useToast();
  const base = `/users/${user.id}`;
  const [s, setS] = useState<Settings | null>(null);
  const [hooks, setHooks] = useState("");
  const [commands, setCommands] = useState("[]");
  const load = useCallback(() => {
    get<Settings>(`${base}/settings`)
      .then((r) => {
        setS(r);
        setHooks(r.hooks);
        setCommands(JSON.stringify(r.commands ?? [], null, 2));
      })
      .catch((x) => toast(errText(x), "error"));
  }, [base, toast]);
  useEffect(load, [load]);

  async function act(fn: () => Promise<unknown>, done: string) {
    try {
      await fn();
      toast(done, "success");
      load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  function parsedCommands(): unknown {
    try {
      return JSON.parse(commands);
    } catch {
      return null;
    }
  }

  return (
    <Modal title={`Settings of ${user.name}`} onClose={onClose}>
      {!s ? (
        <Skeleton lines={4} />
      ) : (
        <div className="stack">
          <p className="muted">
            Changes are recorded in the audit log. Credential values are never shown, only names.
          </p>
          <h3>Notes</h3>
          {s.notes.length === 0 && <p className="muted">None.</p>}
          <ul>
            {s.notes.map((n) => (
              <li key={n.id} className="row">
                <span className="grow">
                  <span className="muted">{n.scope === "repo" ? n.scope_key : "general"}: </span>
                  {n.content}
                </span>
                <Button
                  variant="danger"
                  onClick={() => void act(() => del(`${base}/notes/${n.id}`), "Note deleted.")}
                >
                  Delete
                </Button>
              </li>
            ))}
          </ul>
          <h3>Hooks</h3>
          <p className="muted">Repository hooks the user has trusted: {s.trusted_repo_hooks}.</p>
          <TextArea
            label="Hooks (JSON)"
            rows={5}
            value={hooks}
            onChange={(e) => setHooks(e.target.value)}
          />
          <Button
            onClick={() => void act(() => put(`${base}/hooks`, { hooks }), "Hooks saved.")}
            disabled={hooks === s.hooks}
          >
            Save hooks
          </Button>
          <h3>Slash commands</h3>
          <TextArea
            label="Commands (JSON list of name, description, template)"
            rows={6}
            value={commands}
            onChange={(e) => setCommands(e.target.value)}
          />
          <Button
            disabled={parsedCommands() === null}
            onClick={() =>
              void act(
                () => put(`${base}/commands`, { commands: parsedCommands() }),
                "Commands saved.",
              )
            }
          >
            Save commands
          </Button>
          <h3>Connections</h3>
          {s.connections.length === 0 && <p className="muted">None.</p>}
          <ul>
            {s.connections.map((c) => (
              <li key={c.kind} className="row">
                <span className="grow">
                  {c.kind} <span className="muted">{c.account}</span>
                </span>
                <Button
                  variant="danger"
                  onClick={() =>
                    void act(() => del(`${base}/connections/${c.kind}`), "Connection removed.")
                  }
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
          <h3>Secrets (names only)</h3>
          <p className="muted">{s.secret_names.length ? s.secret_names.join(", ") : "None."}</p>
          <h3>MCP servers switched on</h3>
          {s.mcp.length === 0 && <p className="muted">None.</p>}
          <ul>
            {s.mcp.map((n) => (
              <li key={n} className="row">
                <span className="grow">{n}</span>
                <Button
                  onClick={() =>
                    void act(() => put(`${base}/mcp`, { name: n, enabled: false }), "Switched off.")
                  }
                >
                  Switch off
                </Button>
              </li>
            ))}
          </ul>
          <h3>Automations</h3>
          {s.automations.length === 0 && <p className="muted">None.</p>}
          <ul>
            {s.automations.map((a) => (
              <li key={a.id} className="row">
                <span className="grow">
                  {a.name} <span className="muted">{a.trigger}</span>
                </span>
                <Button
                  variant="danger"
                  onClick={() =>
                    void act(() => del(`${base}/automations/${a.id}`), "Automation deleted.")
                  }
                >
                  Delete
                </Button>
              </li>
            ))}
          </ul>
          <h3>API tokens</h3>
          {s.tokens.length === 0 && <p className="muted">None.</p>}
          <ul>
            {s.tokens.map((t) => (
              <li key={t.id} className="row">
                <span className="grow">
                  {t.name} <code>{t.prefix}…</code> <span className="muted">{t.scope}</span>
                  {t.revoked ? " (revoked)" : ""}
                </span>
                {!t.revoked && (
                  <Button
                    variant="danger"
                    onClick={() => void act(() => del(`${base}/tokens/${t.id}`), "Token revoked.")}
                  >
                    Revoke
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Modal>
  );
}

interface Msg {
  role: string;
  content: string;
  created_at?: string;
}

/** A session's messages, for the admin. */
export function SessionMessagesDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const toast = useToast();
  const [msgs, setMsgs] = useState<Msg[] | null>(null);
  useEffect(() => {
    get<Msg[]>(`/sessions/${id}/messages`)
      .then(setMsgs)
      .catch((x) => toast(errText(x), "error"));
  }, [id, toast]);
  return (
    <Modal title="Session messages" onClose={onClose}>
      {!msgs ? (
        <Skeleton lines={4} />
      ) : msgs.length === 0 ? (
        <p className="muted">No messages.</p>
      ) : (
        <div className="stack">
          <p className="muted">This read is recorded in the audit log.</p>
          {msgs.map((m, i) => (
            <div key={i}>
              <strong>{m.role}</strong>
              <pre className="output">{m.content}</pre>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
