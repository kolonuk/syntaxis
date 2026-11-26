// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { del, get, post, put, errText } from "../lib/api";
import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  Input,
  Modal,
  Skeleton,
  Toggle,
  useToast,
} from "../ui/ui";

interface Server {
  name: string;
  package: string;
  enabled: boolean;
  missing: string[];
  ready: boolean;
  /** What the server needs from this user: the credential's name and whether they have supplied it. */
  credentials?: { name: string; env: string; set: boolean }[];
}
interface Found {
  name: string;
  title?: string;
  description: string;
  repository?: string;
  approved: boolean;
  requested: boolean;
}
interface Request {
  id: string;
  name: string;
  title?: string;
  reason?: string;
}

/** Look for an MCP server in the public catalogue and ask the admin to approve it. */
export function FindServers() {
  const toast = useToast();
  const [q, setQ] = useState("");
  const [found, setFound] = useState<Found[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [mine, setMine] = useState<Request[]>([]);
  const [asking, setAsking] = useState<Found | null>(null);
  const [reason, setReason] = useState("");
  const loadMine = useCallback(() => {
    get<Request[]>("/mcp/requests")
      .then(setMine)
      .catch(() => setMine([]));
  }, []);
  useEffect(loadMine, [loadMine]);

  async function search(e?: FormEvent) {
    e?.preventDefault();
    setSearching(true);
    try {
      const r = await get<{ servers: Found[] }>(`/mcp/search?q=${encodeURIComponent(q)}`);
      setFound(r.servers);
    } catch (x) {
      setFound(null);
      toast(errText(x), "error");
    } finally {
      setSearching(false);
    }
  }
  async function request() {
    if (!asking) return;
    try {
      await post("/mcp/requests", { name: asking.name, title: asking.title ?? "", reason });
      toast("Requested. Your admin will see it.", "success");
      setAsking(null);
      setReason("");
      loadMine();
      void search();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  return (
    <Card title="Find more tools">
      <p className="muted">
        Search the public MCP catalogue for a server you would like. Your admin reviews it before
        anyone can switch it on, so asking costs nothing and changes nothing until it is approved.
        Once it is approved you give it your own credentials.
      </p>
      <form className="row" onSubmit={(e) => void search(e)}>
        <Input
          label="Search"
          hint="For example github, filesystem, postgres"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <Button variant="primary" type="submit" loading={searching}>
          Search
        </Button>
      </form>
      {found && found.length === 0 && <p className="muted">Nothing found for that search.</p>}
      {found && found.length > 0 && (
        <ul className="list-rows">
          {found.map((f) => (
            <li key={f.name}>
              <div className="grow">
                <strong>{f.title || f.name}</strong>
                <div className="muted">{f.description}</div>
                <small className="muted">{f.name}</small>
              </div>
              {f.approved ? (
                <Badge tone="success">Approved: switch it on above</Badge>
              ) : f.requested ? (
                <Badge>Requested</Badge>
              ) : (
                <Button
                  onClick={() => {
                    setAsking(f);
                    setReason("");
                  }}
                >
                  Request
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {mine.length > 0 && (
        <>
          <h3>Your requests</h3>
          <ul className="list-rows">
            {mine.map((m) => (
              <li key={m.id}>
                <div className="grow">
                  <strong>{m.title || m.name}</strong>
                  {m.reason && <div className="muted">{m.reason}</div>}
                </div>
                <Button
                  variant="ghost"
                  onClick={async () => {
                    try {
                      await del(`/mcp/requests/${m.id}`);
                      loadMine();
                      if (found) void search();
                    } catch (x) {
                      toast(errText(x), "error");
                    }
                  }}
                >
                  Withdraw
                </Button>
              </li>
            ))}
          </ul>
        </>
      )}
      {asking && (
        <Modal
          title={`Request ${asking.title || asking.name}`}
          onClose={() => setAsking(null)}
          actions={
            <>
              <Button onClick={() => setAsking(null)}>Cancel</Button>
              <Button variant="primary" onClick={() => void request()}>
                Send request
              </Button>
            </>
          }
        >
          <p>{asking.description}</p>
          <Input
            label="Why do you want it? (optional)"
            value={reason}
            maxLength={500}
            onChange={(e) => setReason(e.target.value)}
          />
        </Modal>
      )}
    </Card>
  );
}

/** One credential a server needs: say it is set, or take its value (write-only, kept in your own vault). */
function CredentialRow({
  name,
  set,
  onSaved,
}: {
  name: string;
  set: boolean;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!value) return;
    setSaving(true);
    try {
      await put(`/secrets/${encodeURIComponent(name)}`, { value });
      setValue("");
      toast(`${name} is saved in your vault.`, "success");
      onSaved();
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setSaving(false);
    }
  }
  return (
    <form className="row" onSubmit={(e) => void save(e)}>
      <div className="grow">
        <code>{name}</code>{" "}
        {set ? <Badge tone="success">yours is set</Badge> : <Badge tone="warn">needed</Badge>}
      </div>
      <Input
        label={set ? `Replace your ${name}` : `Your ${name}`}
        type="password"
        autoComplete="off"
        value={value}
        placeholder="the key or token itself"
        onChange={(e) => setValue(e.target.value)}
      />
      <Button type="submit" loading={saving} disabled={!value}>
        {set ? "Replace" : "Save"}
      </Button>
    </form>
  );
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
  return (
    <div className="stack">
      {list.length === 0 ? (
        <EmptyState
          title="No extra tools approved yet"
          hint="Search below for a server and ask your admin to approve it."
        />
      ) : (
        <>
          <Banner>
            These tools are approved by your admin and run in their own sandbox, only when you
            switch them on, with your own credentials. State-changing tools ask before they run.
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
              {(s.credentials ?? []).length > 0 && (
                <>
                  <p className="muted">
                    This server uses your own credentials, kept in your vault and never shown again.
                    You can also connect the matching service under Connections. It will not turn on
                    or start until all of them are set.
                  </p>
                  {(s.credentials ?? []).map((c) => (
                    <CredentialRow key={c.name} name={c.name} set={c.set} onSaved={load} />
                  ))}
                </>
              )}
            </Card>
          ))}
        </>
      )}
      <FindServers />
    </div>
  );
}
