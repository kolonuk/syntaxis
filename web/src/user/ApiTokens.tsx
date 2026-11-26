// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { del, errText, get, post } from "../lib/api";
import { copyText } from "../lib/fences";
import { Banner, Button, Card, Input, Modal, Select, Skeleton, useToast } from "../ui/ui";

interface Token {
  id: number;
  name: string;
  prefix: string;
  scope: "read" | "run";
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
  token?: string;
}

const SCOPES = [
  { value: "read", label: "Read: look at sessions, messages, changes and files" },
  { value: "run", label: "Run: read, and also start sessions, send messages, answer approvals" },
];

const EXPIRY = [
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
  { value: "365", label: "1 year" },
  { value: "0", label: "Never" },
];

/** Personal API tokens: made here, shown once, listed by name and the start of the token. */
export function ApiTokens() {
  const toast = useToast();
  const [list, setList] = useState<Token[] | null>(null);
  const [name, setName] = useState("");
  const [scope, setScope] = useState("read");
  const [days, setDays] = useState("90");
  const [fresh, setFresh] = useState<Token | null>(null);
  const [creating, setCreating] = useState(false);
  const load = useCallback(() => {
    get<Token[]>("/tokens")
      .then(setList)
      .catch((x) => toast(errText(x), "error"));
  }, [toast]);
  useEffect(load, [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    try {
      const t = await post<Token>("/tokens", { name, scope, expires_days: Number(days) });
      setFresh(t);
      setName("");
      setCreating(false);
      load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  return (
    <Card title="API tokens">
      <p className="muted">
        A token lets a script or tool use Syntaxis as you, with a limited scope. It is shown once,
        stored only as a hash, and can be revoked here at any time. A token can never see your
        secrets or connections, change settings, hooks, commands or automations, open the terminal,
        edit files or make other tokens. See the API section of the docs for what each scope can
        reach.
      </p>
      {fresh?.token && (
        <Banner tone="info">
          <p>
            Copy this token now. It is not shown again: <code>{fresh.token}</code>
          </p>
          <Button onClick={() => void copyText(fresh.token ?? "")}>Copy</Button>{" "}
          <Button variant="ghost" onClick={() => setFresh(null)}>
            Done
          </Button>
        </Banner>
      )}
      {list === null ? (
        <Skeleton lines={2} />
      ) : list.length === 0 ? (
        <p className="muted">No tokens yet.</p>
      ) : (
        <ul className="list-rows">
          {list.map((t) => (
            <li key={t.id}>
              <span className="grow">
                <strong>{t.name}</strong> <code>{t.prefix}…</code>{" "}
                <span className="muted">
                  {t.scope}
                  {t.revoked
                    ? ", revoked"
                    : t.expires_at
                      ? `, expires ${new Date(t.expires_at).toLocaleDateString()}`
                      : ", never expires"}
                  {t.last_used_at
                    ? `, last used ${new Date(t.last_used_at).toLocaleDateString()}`
                    : ", never used"}
                </span>
              </span>
              {!t.revoked && (
                <Button
                  variant="danger"
                  onClick={async () => {
                    try {
                      await del(`/tokens/${t.id}`);
                      load();
                    } catch (x) {
                      toast(errText(x), "error");
                    }
                  }}
                >
                  Revoke
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <Button variant="primary" onClick={() => setCreating(true)}>
        Create a token
      </Button>
      {creating && (
        <Modal
          title="Create a token"
          onClose={() => setCreating(false)}
          actions={
            <>
              <Button onClick={() => setCreating(false)}>Cancel</Button>
              <Button variant="primary" type="submit" form="token-form">
                Create token
              </Button>
            </>
          }
        >
          <form id="token-form" className="stack" onSubmit={(e) => void create(e)}>
            <Input
              label="Name"
              hint="What it is for, for example CI reader"
              value={name}
              maxLength={60}
              onChange={(e) => setName(e.target.value)}
              required
            />
            <Select
              label="Scope"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              options={SCOPES}
            />
            <Select
              label="Expires"
              value={days}
              onChange={(e) => setDays(e.target.value)}
              options={EXPIRY}
            />
          </form>
        </Modal>
      )}
    </Card>
  );
}
