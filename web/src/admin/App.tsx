// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { del, get, patch, post, put, errText } from "../lib/api";
import { applyTheme, loadPref } from "../lib/theme";
import {
  Badge,
  Banner,
  Button,
  Card,
  Input,
  Modal,
  Select,
  Skeleton,
  Tabs,
  ToastProvider,
  Toggle,
  useToast,
  type Tone,
} from "../ui/ui";
import { AuthFrame, Login, TwoFactorSetup } from "../user/Auth";
import { useMe } from "../user/hooks";

interface Finding {
  check_id: string;
  severity: string;
  subject: string;
  message: string;
  fix: string;
  snoozed?: boolean;
}
interface Health {
  findings: Finding[];
  summary: { errors: number; warnings: number; infos: number; worst: string };
}
interface User {
  id: number;
  email: string;
  name: string;
  disabled: boolean;
  invited: boolean;
  totp_enabled: boolean;
  quota_daily_tokens: number;
  quota_monthly_tokens: number;
  egress_mode: string;
  allowed_models: string[] | null;
}
interface Prov {
  id: number;
  name: string;
  kind: string;
  base_url: string;
  concurrency: number;
  enabled: boolean;
  has_key: boolean;
}
interface Mod {
  id: number;
  provider_id: number;
  name: string;
  enabled: boolean;
  tools: number;
}

const tone = (s: string): Tone => (s === "error" ? "danger" : s === "warning" ? "warn" : "info");

function useLoad<T>(path: string) {
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

function HealthTab() {
  const toast = useToast();
  const { data, err, reload } = useLoad<Health>("/health");
  if (err) return <Banner tone="danger">{err}</Banner>;
  if (!data) return <Skeleton />;
  return (
    <div className="stack">
      {data.findings.length === 0 && <Banner>All checks passed.</Banner>}
      {data.findings.map((f) => (
        <Card key={f.check_id + f.subject}>
          <div className="row">
            <Badge tone={tone(f.severity)}>{f.severity}</Badge>
            <strong className="grow">
              {f.check_id} {f.subject && <span className="muted">· {f.subject}</span>}
            </strong>
            {f.snoozed && <Badge>snoozed</Badge>}
            {f.severity !== "error" && !f.snoozed && (
              <Button
                onClick={async () => {
                  try {
                    await post("/health/snooze", {
                      check_id: f.check_id,
                      subject: f.subject,
                      hours: 24,
                    });
                    reload();
                  } catch (e) {
                    toast(errText(e), "error");
                  }
                }}
              >
                Snooze 24h
              </Button>
            )}
          </div>
          <p>{f.message}</p>
          <p className="muted">
            <strong>Fix:</strong> {f.fix}
          </p>
        </Card>
      ))}
    </div>
  );
}

function UsersTab() {
  const toast = useToast();
  const { data, reload } = useLoad<User[]>("/users");
  const [adding, setAdding] = useState(false);
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [edit, setEdit] = useState<User | null>(null);
  async function invite(e: FormEvent) {
    e.preventDefault();
    try {
      await post("/users", { email, name });
      toast("Invite sent.", "success");
      setAdding(false);
      setEmail("");
      setName("");
      reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (!data) return <Skeleton />;
  return (
    <div className="stack">
      <Button variant="primary" onClick={() => setAdding(true)}>
        Invite user
      </Button>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Status</th>
              <th>2FA</th>
              <th>Daily tokens</th>
              <th>Egress</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data.map((u) => (
              <tr key={u.id}>
                <td>
                  {u.name}
                  <br />
                  <span className="muted">{u.email}</span>
                </td>
                <td>
                  {u.disabled ? (
                    <Badge tone="danger">disabled</Badge>
                  ) : u.invited ? (
                    <Badge tone="warn">invited</Badge>
                  ) : (
                    <Badge tone="success">active</Badge>
                  )}
                </td>
                <td>{u.totp_enabled ? "on" : "off"}</td>
                <td>{u.quota_daily_tokens || "default"}</td>
                <td>{u.egress_mode || "default"}</td>
                <td className="row">
                  <Button onClick={() => setEdit(u)}>Edit</Button>
                  {u.invited && (
                    <Button
                      onClick={async () => {
                        await post(`/users/${u.id}/resend`);
                        toast("Invite re-sent.", "success");
                      }}
                    >
                      Resend
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {adding && (
        <Modal title="Invite user" onClose={() => setAdding(false)}>
          <form onSubmit={invite}>
            <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} required />
            <Input
              label="Email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              hint="They receive a single-use link to set their own password."
            />
            <Button variant="primary" type="submit">
              Send invite
            </Button>
          </form>
        </Modal>
      )}
      {edit && (
        <EditUser
          u={edit}
          onClose={() => {
            setEdit(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function EditUser({ u, onClose }: { u: User; onClose: () => void }) {
  const toast = useToast();
  const [daily, setDaily] = useState(String(u.quota_daily_tokens));
  const [monthly, setMonthly] = useState(String(u.quota_monthly_tokens));
  const [egress, setEgress] = useState(u.egress_mode);
  const [models, setModels] = useState((u.allowed_models ?? []).join(", "));
  const [disabled, setDisabled] = useState(u.disabled);
  async function save() {
    try {
      await patch(`/users/${u.id}`, {
        quota_daily_tokens: Number(daily),
        quota_monthly_tokens: Number(monthly),
        egress_mode: egress,
        disabled,
        allowed_models: models
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      });
      toast("Saved.", "success");
      onClose();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <Modal
      title={`Edit ${u.name}`}
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <Input
        label="Daily token quota"
        hint="0 = default, -1 = unlimited"
        value={daily}
        onChange={(e) => setDaily(e.target.value)}
      />
      <Input
        label="Monthly token quota"
        value={monthly}
        onChange={(e) => setMonthly(e.target.value)}
      />
      <Select
        label="Egress policy"
        value={egress}
        onChange={(e) => setEgress(e.target.value)}
        options={[
          { value: "", label: "Instance default" },
          { value: "open", label: "Open" },
          { value: "allowlist", label: "Allowlist" },
          { value: "none", label: "None" },
        ]}
      />
      <Input
        label="Allowed models"
        hint="Comma-separated provider/model ids. Empty = all enabled models."
        value={models}
        onChange={(e) => setModels(e.target.value)}
      />
      <Toggle
        label="Disabled (revokes sessions and stops sandboxes)"
        checked={disabled}
        onChange={setDisabled}
      />
      <div style={{ marginTop: 12 }} className="row">
        <Button
          onClick={async () => {
            await post(`/users/${u.id}/topup`, { tokens: 100000 });
            toast("Added 100k tokens for today.", "success");
          }}
        >
          Top up 100k
        </Button>
        <Button
          onClick={async () => {
            await post(`/users/${u.id}/reset-usage`);
            toast("Today's usage reset.", "success");
          }}
        >
          Reset today
        </Button>
      </div>
    </Modal>
  );
}

function ProvidersTab() {
  const toast = useToast();
  const provs = useLoad<Prov[]>("/providers");
  const models = useLoad<Mod[]>("/models");
  const [f, setF] = useState({
    name: "",
    kind: "ollama",
    base_url: "",
    api_key: "",
    concurrency: "1",
  });
  async function add(e: FormEvent) {
    e.preventDefault();
    try {
      await post("/providers", { ...f, concurrency: Number(f.concurrency) });
      setF({ ...f, name: "", base_url: "", api_key: "" });
      provs.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (!provs.data || !models.data) return <Skeleton />;
  return (
    <div className="stack">
      <p className="muted">
        Syntaxis only reads from your LLM server: it never pulls, loads or deletes models.
      </p>
      {provs.data.map((p) => (
        <Card key={p.id} title={`${p.name} (${p.kind})`}>
          <p className="muted">
            {p.base_url} · concurrency {p.concurrency} {p.has_key && "· key stored"}
          </p>
          <div className="row">
            <Button
              onClick={async () => {
                try {
                  const r = await post<{ models: number }>(`/providers/${p.id}/refresh`);
                  toast(`Found ${r.models} models.`, "success");
                  models.reload();
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Discover models
            </Button>
            <Button
              variant="danger"
              onClick={async () => {
                await del(`/providers/${p.id}`);
                provs.reload();
                models.reload();
              }}
            >
              Remove
            </Button>
          </div>
          {models
            .data!.filter((m) => m.provider_id === p.id)
            .map((m) => (
              <div key={m.id} className="row" style={{ marginTop: 8 }}>
                <Toggle
                  label={m.name}
                  checked={m.enabled}
                  onChange={async (v) => {
                    await post(`/models/${m.id}/enabled`, { enabled: v });
                    models.reload();
                  }}
                />
                {m.tools === 0 && <Badge tone="warn">no tool support</Badge>}
              </div>
            ))}
        </Card>
      ))}
      <Card title="Add provider">
        <form onSubmit={add}>
          <Input
            label="Name"
            value={f.name}
            onChange={(e) => setF({ ...f, name: e.target.value })}
            required
          />
          <Select
            label="Kind"
            value={f.kind}
            onChange={(e) => setF({ ...f, kind: e.target.value })}
            options={[
              { value: "ollama", label: "Ollama" },
              { value: "llamacpp", label: "llama.cpp server" },
              { value: "litellm", label: "LiteLLM" },
              { value: "openai", label: "OpenAI-compatible" },
            ]}
          />
          <Input
            label="Base URL"
            placeholder="http://llm.internal:11434"
            value={f.base_url}
            onChange={(e) => setF({ ...f, base_url: e.target.value })}
            required
          />
          <Input
            label="API key (optional)"
            type="password"
            value={f.api_key}
            onChange={(e) => setF({ ...f, api_key: e.target.value })}
          />
          <Input
            label="Concurrency"
            hint="Match your server's parallel setting; Syntaxis never exceeds it."
            value={f.concurrency}
            onChange={(e) => setF({ ...f, concurrency: e.target.value })}
          />
          <Button variant="primary" type="submit">
            Add
          </Button>
        </form>
      </Card>
    </div>
  );
}

function Table({ rows, cols }: { rows: Record<string, unknown>[]; cols: string[] }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {cols.map((c) => (
              <th key={c}>{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {cols.map((c) => (
                <td key={c}>{String(r[c] ?? "")}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SessionsTab() {
  const { data } = useLoad<Record<string, unknown>[]>("/sessions");
  if (!data) return <Skeleton />;
  return (
    <>
      <p className="muted">
        Metadata only. Message content is private unless ALLOW_ADMIN_READ_CONTENT is enabled.
      </p>
      <Table
        rows={data}
        cols={["id", "user_id", "model", "state", "tokens_in", "tokens_out", "updated_at"]}
      />
    </>
  );
}

function AuditTab() {
  const { data } = useLoad<{ entries: Record<string, unknown>[]; chain_ok: boolean }>("/audit");
  if (!data) return <Skeleton />;
  return (
    <>
      <Banner tone={data.chain_ok ? "info" : "danger"}>
        {data.chain_ok ? "Audit chain verified." : "AUDIT CHAIN BROKEN: the log was modified."}
      </Banner>
      <Table
        rows={[...data.entries].reverse()}
        cols={["seq", "time", "actor", "action", "target", "detail"]}
      />
    </>
  );
}

function EgressTab() {
  const { data } = useLoad<Record<string, unknown>[]>("/egress");
  if (!data) return <Skeleton />;
  return (
    <Table
      rows={data}
      cols={[
        "time",
        "user_id",
        "session_id",
        "dest_host",
        "dest_ip",
        "port",
        "protocol",
        "allowed",
      ]}
    />
  );
}

function SettingsTab() {
  const toast = useToast();
  const { data, reload } = useLoad<Record<string, string>>("/settings");
  const [s, setS] = useState<Record<string, string>>({});
  useEffect(() => {
    if (data) setS(data);
  }, [data]);
  if (!data) return <Skeleton />;
  return (
    <div className="stack" style={{ maxWidth: 520 }}>
      <Input
        label="Password rotation (days, users without 2FA)"
        hint="0 disables rotation"
        value={s.password_rotation_days ?? ""}
        onChange={(e) => setS({ ...s, password_rotation_days: e.target.value })}
      />
      <Select
        label="Secret scanner"
        value={s.secret_scan_mode ?? "warn"}
        onChange={(e) => setS({ ...s, secret_scan_mode: e.target.value })}
        options={[
          { value: "off", label: "Off" },
          { value: "warn", label: "Warn" },
          { value: "block", label: "Block pushes" },
        ]}
      />
      <Toggle
        label="Users may override a scanner block"
        checked={s.secret_scan_allow_user_override === "true"}
        onChange={(v) => setS({ ...s, secret_scan_allow_user_override: String(v) })}
      />
      <Input
        label="Default daily tokens (0 = none)"
        value={s.default_daily_tokens ?? ""}
        onChange={(e) => setS({ ...s, default_daily_tokens: e.target.value })}
      />
      <Input
        label="Default monthly tokens (0 = none)"
        value={s.default_monthly_tokens ?? ""}
        onChange={(e) => setS({ ...s, default_monthly_tokens: e.target.value })}
      />
      <div className="row">
        <Button
          variant="primary"
          onClick={async () => {
            try {
              await put("/settings", s);
              toast("Saved.", "success");
              reload();
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          Save
        </Button>
        <Button
          onClick={async () => {
            try {
              await post("/mail/test");
              toast("Test email sent to you.", "success");
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          Send test email
        </Button>
      </div>
    </div>
  );
}

function KillTab() {
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  return (
    <Card title="Kill switch">
      <p>
        Stops every sandbox immediately and blocks new ones until released. Workspaces are kept.
      </p>
      <div className="row">
        <Button variant="danger" onClick={() => setConfirm(true)}>
          Stop all sandboxes
        </Button>
        <Button
          onClick={async () => {
            await post("/kill/release");
            toast("Released.", "success");
          }}
        >
          Release
        </Button>
      </div>
      {confirm && (
        <Modal
          title="Stop all sandboxes?"
          onClose={() => setConfirm(false)}
          actions={
            <>
              <Button onClick={() => setConfirm(false)}>Cancel</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  const r = await post<{ stopped: number }>("/kill");
                  toast(`Stopped ${r.stopped}.`, "success");
                  setConfirm(false);
                }}
              >
                Stop all
              </Button>
            </>
          }
        >
          <p>Running agent sessions will be interrupted.</p>
        </Modal>
      )}
    </Card>
  );
}

function Console({ name, signOut }: { name: string; signOut: () => void }) {
  const [tab, setTab] = useState("health");
  const { data: h } = useLoad<Health>("/health");
  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">Syntaxis admin</span>
        {h && h.summary.worst && (
          <Badge
            tone={tone(
              h.summary.worst === "error"
                ? "error"
                : h.summary.worst === "warning"
                  ? "warning"
                  : "info",
            )}
          >
            {h.summary.errors} errors · {h.summary.warnings} warnings
          </Badge>
        )}
        <span className="muted">{name}</span>
        <Button variant="ghost" onClick={signOut}>
          Sign out
        </Button>
      </header>
      <div className="main">
        <Tabs
          value={tab}
          onChange={setTab}
          tabs={[
            { id: "health", label: "Health" },
            { id: "users", label: "Users" },
            { id: "providers", label: "Providers & models" },
            { id: "sessions", label: "Sessions" },
            { id: "audit", label: "Audit" },
            { id: "egress", label: "Egress" },
            { id: "settings", label: "Policies" },
            { id: "kill", label: "Kill switch" },
          ]}
        />
        {tab === "health" && <HealthTab />}
        {tab === "users" && <UsersTab />}
        {tab === "providers" && <ProvidersTab />}
        {tab === "sessions" && <SessionsTab />}
        {tab === "audit" && <AuditTab />}
        {tab === "egress" && <EgressTab />}
        {tab === "settings" && <SettingsTab />}
        {tab === "kill" && <KillTab />}
      </div>
    </div>
  );
}

export default function AdminApp() {
  const { me, reload, clear } = useMe();
  useEffect(() => {
    applyTheme(loadPref());
  }, []);
  const signOut = async () => {
    try {
      await post("/logout");
    } catch {
      /* ignore */
    }
    clear();
  };
  let body;
  if (me === undefined)
    body = (
      <AuthFrame title="Syntaxis admin">
        <Skeleton />
      </AuthFrame>
    );
  else if (me === null)
    body = <Login prefix="Admin" go={() => undefined} onDone={() => void reload()} />;
  else if (!me.totp_enabled)
    body = (
      <AuthFrame title="Set up two-factor authentication">
        <TwoFactorSetup mandatory onDone={() => void reload()} />
      </AuthFrame>
    );
  else body = <Console name={me.name} signOut={signOut} />;
  return <ToastProvider>{body}</ToastProvider>;
}
