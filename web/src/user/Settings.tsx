// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState, type FormEvent } from "react";
import { del, get, post, put, errText } from "../lib/api";
import { compact } from "../lib/format";
import { Badge, Banner, Button, Card, Input, Meter, Skeleton, Tabs, useToast } from "../ui/ui";
import { ChangePassword, TwoFactorSetup } from "./Auth";
import type { Me } from "./hooks";

interface Usage {
  daily: { Used: number; Limit: number };
  monthly: { Used: number; Limit: number };
  sandbox_seconds: { Used: number; Limit: number };
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
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const load = () => get<string[]>("/secrets").then(setNames);
  useEffect(() => {
    void load();
  }, []);
  async function add(e: FormEvent) {
    e.preventDefault();
    try {
      await put(`/secrets/${name}`, { value });
      setName("");
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
        again, never sent to the model, and injected into your sandbox only while it runs. Use
        least-privilege, short-lived credentials.
      </p>
      {names === null ? (
        <Skeleton lines={2} />
      ) : names.length === 0 ? (
        <p className="muted">No secrets yet.</p>
      ) : (
        <ul>
          {names.map((n) => (
            <li key={n} className="row">
              <code className="grow">{n}</code>
              <Button
                variant="danger"
                onClick={async () => {
                  await del(`/secrets/${n}`);
                  void load();
                }}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={add}>
        <Input
          label="Name"
          hint="UPPER_SNAKE_CASE, e.g. GITHUB_TOKEN"
          value={name}
          onChange={(e) => setName(e.target.value.toUpperCase())}
          required
        />
        <Input
          label="Value"
          type="password"
          autoComplete="off"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          required
        />
        <Button variant="primary" type="submit">
          Save secret
        </Button>
      </form>
    </Card>
  );
}

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
    <div className="stack" style={{ maxWidth: 720 }}>
      <Tabs
        tabs={[
          { id: "profile", label: "Profile" },
          { id: "password", label: "Password" },
          { id: "2fa", label: "Two-factor" },
          { id: "secrets", label: "Secrets" },
          { id: "usage", label: "Usage" },
        ]}
        value={tab}
        onChange={setTab}
      />
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
      {tab === "secrets" && <Secrets />}
      {tab === "usage" && <UsagePanel />}
    </div>
  );
}
