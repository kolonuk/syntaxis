// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState, type FormEvent } from "react";
import "./user.css";
import { del, get, post, put, errText } from "../lib/api";
import { compact } from "../lib/format";
import { Connections } from "./Connections";
import { McpTools } from "./McpTools";
import { CommandsEditor, HooksEditor, Notes } from "./AgentSettings";
import { SharedLinks } from "./Shared";
import { ApiTokens } from "./ApiTokens";
import { SideMenu } from "../ui/SideMenu";
import {
  Badge,
  Banner,
  Button,
  Card,
  Input,
  Meter,
  Modal,
  Skeleton,
  useToast,
  VersionLabel,
} from "../ui/ui";
import { ChangePassword, TwoFactorSetup } from "./Auth";
import type { Me } from "./hooks";

interface Usage {
  daily: { Used: number; Limit: number };
  monthly: { Used: number; Limit: number };
  sandbox_seconds: { Used: number; Limit: number };
  cpu_seconds?: { Used: number; Limit: number };
}

export function UsagePanel() {
  const [u, setU] = useState<Usage | null>(null);
  useEffect(() => {
    void get<Usage>("/usage").then(setU);
  }, []);
  if (!u) return <Skeleton />;
  return (
    <Card title="Usage">
      <div className="stack">
        <Meter label="Tokens today" used={u.daily.Used} limit={u.daily.Limit} />
        <Meter label="Tokens this month" used={u.monthly.Used} limit={u.monthly.Limit} />
        <Meter
          label="Sandbox minutes today"
          used={Math.round(u.sandbox_seconds.Used / 60)}
          limit={Math.round(u.sandbox_seconds.Limit / 60)}
        />
        {u.cpu_seconds && (
          <Meter
            label="Compute minutes today (CPU time actually used)"
            used={Math.round(u.cpu_seconds.Used / 60)}
            limit={Math.round(u.cpu_seconds.Limit / 60)}
          />
        )}
        <p className="muted">
          {compact(u.daily.Used)} tokens used today. Quotas reset at midnight UTC.
        </p>
      </div>
    </Card>
  );
}

function Secrets() {
  const toast = useToast();
  const [names, setNames] = useState<string[] | null>(null);
  // the secret being added (name "") or changed, in the pop-up
  const [edit, setEdit] = useState<{ name: string; isNew: boolean } | null>(null);
  const [value, setValue] = useState("");
  const [removing, setRemoving] = useState<string | null>(null);
  const load = () => get<string[]>("/secrets").then(setNames);
  useEffect(() => {
    void load();
  }, []);
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!edit) return;
    try {
      await put(`/secrets/${edit.name}`, { value });
      setEdit(null);
      setValue("");
      toast("Secret saved.", "success");
      void load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <Card title="Secrets and integrations">
      <p className="muted">
        Tokens you add here (GitHub, Cloudflare, cloud credentials) are encrypted, never shown
        again, never sent to the model, and supplied to your sandbox commands. Changes apply
        immediately; an open terminal needs reconnecting. Use least-privilege, short-lived
        credentials.
      </p>
      {names === null ? (
        <Skeleton lines={2} />
      ) : names.length === 0 ? (
        <p className="muted">No secrets yet.</p>
      ) : (
        <ul className="list-rows">
          {names.map((n) => (
            <li key={n}>
              <code className="grow">{n}</code>
              <Button variant="ghost" onClick={() => setEdit({ name: n, isNew: false })}>
                Replace value
              </Button>
              <Button variant="danger" onClick={() => setRemoving(n)}>
                Delete
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Button variant="primary" onClick={() => setEdit({ name: "", isNew: true })}>
        Add a secret
      </Button>
      {edit && (
        <Modal
          title={edit.isNew ? "Add a secret" : `Replace ${edit.name}`}
          onClose={() => setEdit(null)}
          actions={
            <>
              <Button onClick={() => setEdit(null)}>Cancel</Button>
              <Button variant="primary" type="submit" form="secret-form">
                Save secret
              </Button>
            </>
          }
        >
          <form id="secret-form" className="stack" onSubmit={(e) => void save(e)}>
            {edit.isNew && (
              <Input
                label="Name"
                hint="UPPER_SNAKE_CASE, e.g. GITHUB_TOKEN"
                value={edit.name}
                onChange={(e) => setEdit({ ...edit, name: e.target.value.toUpperCase() })}
                required
              />
            )}
            <Input
              label="Value"
              type="password"
              autoComplete="off"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              required
            />
          </form>
        </Modal>
      )}
      {removing && (
        <Modal
          title={`Delete ${removing}?`}
          onClose={() => setRemoving(null)}
          actions={
            <>
              <Button onClick={() => setRemoving(null)}>Cancel</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await del(`/secrets/${removing}`);
                    setRemoving(null);
                    void load();
                  } catch (x) {
                    toast(errText(x), "error");
                  }
                }}
              >
                Delete
              </Button>
            </>
          }
        >
          <p>Sandbox commands lose access to this secret straight away.</p>
        </Modal>
      )}
    </Card>
  );
}

export const SETTINGS_GROUPS: { label: string; items: { id: string; label: string }[] }[] = [
  {
    label: "Account",
    items: [
      { id: "profile", label: "Profile" },
      { id: "password", label: "Password" },
      { id: "2fa", label: "Two-factor" },
      { id: "usage", label: "Usage" },
    ],
  },
  {
    label: "Services and keys",
    items: [
      { id: "connections", label: "Connections" },
      { id: "secrets", label: "Secrets" },
      { id: "tokens", label: "API tokens" },
    ],
  },
  {
    label: "Agent",
    items: [
      { id: "tools", label: "Tools" },
      { id: "notes", label: "Notes" },
      { id: "commands", label: "Commands" },
      { id: "hooks", label: "Hooks" },
    ],
  },
  { label: "Sharing", items: [{ id: "shared", label: "Shared links" }] },
];

export function Settings({
  me,
  reload,
  onLogout,
}: {
  me: Me;
  reload: () => void;
  onLogout: () => void;
}) {
  const toast = useToast();
  const [tab, setTab] = useState("profile");
  return (
    <SideMenu groups={SETTINGS_GROUPS} value={tab} onChange={setTab} label="Settings">
      {tab === "profile" && (
        <Card title="Profile">
          <p>
            {me.name} <span className="muted">({me.email})</span>
          </p>
          <Button
            onClick={async () => {
              await post("/logout-all");
              onLogout();
            }}
          >
            Sign out everywhere
          </Button>
          <p>
            <VersionLabel />
          </p>
        </Card>
      )}
      {tab === "password" && (
        <Card title="Change password">
          <ChangePassword
            onDone={() => {
              toast("Password changed. Please sign in again.", "success");
              onLogout();
            }}
          />
        </Card>
      )}
      {tab === "2fa" && (
        <Card title="Two-factor authentication">
          {me.totp_enabled ? (
            <Badge tone="success">Enabled</Badge>
          ) : (
            <TwoFactorSetup onDone={reload} />
          )}
          {!me.totp_enabled && (
            <Banner>
              Accounts with two-factor authentication are never asked to rotate their password.
            </Banner>
          )}
        </Card>
      )}
      {tab === "connections" && <Connections />}
      {tab === "tools" && <McpTools />}
      {tab === "notes" && <Notes />}
      {tab === "commands" && <CommandsEditor />}
      {tab === "hooks" && <HooksEditor />}
      {tab === "shared" && <SharedLinks />}
      {tab === "secrets" && <Secrets />}
      {tab === "tokens" && <ApiTokens />}
      {tab === "usage" && <UsagePanel />}
    </SideMenu>
  );
}
