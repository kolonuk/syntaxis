// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { del, get, post, put, errText } from "../lib/api";
import { Badge, Banner, Button, Card, Input, Skeleton, useToast } from "../ui/ui";

export interface ConnField {
  name: string;
  label: string;
  hint?: string;
  secret: boolean;
  required: boolean;
  default?: string;
}
export interface ConnKind {
  id: string;
  title: string;
  guide: string;
  git: boolean;
  fields: ConnField[];
}
export interface Conn {
  kind: ConnKind;
  connected: boolean;
  account: string;
  detail: string;
  checked_at: string;
  values: Record<string, string>;
  stored: Record<string, boolean>;
}

export function useConnections() {
  const [data, setData] = useState<Conn[] | null>(null);
  const reload = useCallback(() => {
    void get<Conn[]>("/connections")
      .then(setData)
      .catch(() => setData([]));
  }, []);
  useEffect(reload, [reload]);
  return { data, reload };
}

function ConnectionCard({ c, reload }: { c: Conn; reload: () => void }) {
  const toast = useToast();
  const [open, setOpen] = useState(!c.connected);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const k = c.kind;
  const shown = (f: ConnField) =>
    vals[f.name] ?? (f.secret ? "" : (c.values[f.name] ?? f.default ?? ""));

  async function save() {
    setBusy(true);
    try {
      const fields: Record<string, string> = {};
      for (const f of k.fields) fields[f.name] = shown(f);
      const r = await put<{ account: string }>(`/connections/${k.id}`, { fields });
      toast(`Connected as ${r.account}.`, "success");
      setVals({});
      setOpen(false);
      reload();
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  async function test() {
    try {
      const r = await post<{ account: string }>(`/connections/${k.id}/test`);
      toast(`Works: ${r.account}.`, "success");
      reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <Card title={k.title}>
      <div className="row">
        {c.connected ? (
          <Badge tone="success">Connected{c.account ? `: ${c.account}` : ""}</Badge>
        ) : (
          <Badge>Not connected</Badge>
        )}
        {c.detail && <span className="muted">{c.detail}</span>}
        <span className="grow" />
        {c.connected && <Button onClick={test}>Test</Button>}
        <Button onClick={() => setOpen(!open)} aria-expanded={open}>
          {c.connected ? "Change" : "Connect"}
        </Button>
        {c.connected && (
          <Button
            variant="danger"
            onClick={async () => {
              await del(`/connections/${k.id}`);
              toast(`${k.title} removed.`, "success");
              reload();
            }}
          >
            Remove
          </Button>
        )}
      </div>
      {open && (
        <form
          className="stack"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <p className="muted">{k.guide}</p>
          {k.fields.map((f) => (
            <Input
              key={f.name}
              label={f.label + (f.required ? "" : " (optional)")}
              hint={f.hint}
              type={f.secret ? "password" : "text"}
              autoComplete="off"
              placeholder={f.secret && c.stored[f.name] ? "Saved. Leave empty to keep it" : ""}
              value={shown(f)}
              onChange={(e) => setVals({ ...vals, [f.name]: e.target.value })}
            />
          ))}
          <Button variant="primary" type="submit" disabled={busy}>
            {busy ? "Checking…" : "Check and save"}
          </Button>
        </form>
      )}
    </Card>
  );
}

/** The user's own connections to other services. Credentials are kept in their own vault. */
export function Connections() {
  const { data, reload } = useConnections();
  if (!data) return <Skeleton lines={3} />;
  return (
    <div className="stack">
      <Banner>
        These are your own accounts. Nobody else, including admins, can see or use them. They are
        checked when you save them, kept encrypted, and used only in your own sessions, where the
        usual tools (git, gh, aws, az, gcloud, wrangler) find them. They are never shown to the
        model. Use least-privilege, short-lived credentials where the service allows.
      </Banner>
      {data.map((c) => (
        <ConnectionCard key={c.kind.id} c={c} reload={reload} />
      ))}
    </div>
  );
}
