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
  terminal_disabled?: boolean;
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
      <div className="table-wrap" tabIndex={0} role="region" aria-label="Table">
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Status</th>
              <th>2FA</th>
              <th>Daily tokens</th>
              <th>Egress</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
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
                <td className="actions-cell">
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
  const [terminalOff, setTerminalOff] = useState(!!u.terminal_disabled);
  async function save() {
    try {
      await patch(`/users/${u.id}`, {
        quota_daily_tokens: Number(daily),
        quota_monthly_tokens: Number(monthly),
        egress_mode: egress,
        disabled,
        terminal_disabled: terminalOff,
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
        label="Block the interactive terminal for this user"
        checked={terminalOff}
        onChange={setTerminalOff}
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

interface ModFull {
  id: number;
  provider_id: number;
  name: string;
  enabled: boolean;
  tools: number;
  recommended: boolean;
  context_override: number;
}
interface Bench {
  id: number;
  model_id: number;
  time: string;
  report: {
    pass_rate: number;
    tokens_per_sec: number;
    ttft_ms: number;
    tool_call_failure_rate: number;
    tasks: { id: string; passed: boolean; detail: string; seconds: number }[];
  };
}

function ProvidersTab() {
  const toast = useToast();
  const provs = useLoad<Prov[]>("/providers");
  const models = useLoad<ModFull[]>("/models");
  const bench = useLoad<Bench[]>("/benchmarks");
  const [adding, setAdding] = useState(false);
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
      setAdding(false);
      provs.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (!provs.data || !models.data) return <Skeleton />;
  const latest = (id: number) => bench.data?.find((b) => b.model_id === id);
  return (
    <div className="stack">
      <div className="row">
        <p className="muted grow">
          Syntaxis only reads from your LLM server: it never pulls, loads or deletes models.
        </p>
        <Button variant="primary" onClick={() => setAdding(true)}>
          Add provider
        </Button>
      </div>
      {provs.data.length === 0 && (
        <Banner>
          No provider yet. Add your Ollama, llama.cpp, LiteLLM or OpenAI-compatible server.
        </Banner>
      )}
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
          <div className="table-wrap" tabIndex={0} role="region" aria-label={`Models of ${p.name}`}>
            <table>
              <thead>
                <tr>
                  <th>Model</th>
                  <th>Enabled</th>
                  <th>Recommended</th>
                  <th>Context override</th>
                  <th>Last benchmark</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {models
                  .data!.filter((m) => m.provider_id === p.id)
                  .map((m) => {
                    const b = latest(m.id);
                    return (
                      <tr key={m.id}>
                        <td>
                          {m.name} {m.tools === 0 && <Badge tone="warn">no tools</Badge>}
                        </td>
                        <td>
                          <Toggle
                            label={`Enable ${m.name}`}
                            checked={m.enabled}
                            onChange={async (v) => {
                              await post(`/models/${m.id}/enabled`, { enabled: v });
                              models.reload();
                            }}
                          />
                        </td>
                        <td>
                          <Toggle
                            label={`Recommend ${m.name} for agents`}
                            checked={m.recommended}
                            onChange={async (v) => {
                              await post(`/models/${m.id}/recommended`, { recommended: v });
                              models.reload();
                            }}
                          />
                        </td>
                        <td>{m.context_override || <span className="muted">detected</span>}</td>
                        <td>
                          {b ? (
                            `${Math.round(b.report.pass_rate * 100)}% · ${b.report.tokens_per_sec.toFixed(1)} tok/s`
                          ) : (
                            <span className="muted">never</span>
                          )}
                        </td>
                        <td>
                          <Button
                            onClick={async () => {
                              try {
                                await post(`/models/${m.id}/benchmark`);
                                toast(
                                  "Benchmark started. It runs in the background at the lowest priority.",
                                  "success",
                                );
                                setTimeout(bench.reload, 15000);
                              } catch (x) {
                                toast(errText(x), "error");
                              }
                            }}
                          >
                            Benchmark
                          </Button>
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        </Card>
      ))}
      {adding && (
        <Modal title="Add provider" onClose={() => setAdding(false)}>
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
        </Modal>
      )}
    </div>
  );
}

function BenchmarksTab() {
  const { data } = useLoad<Bench[]>("/benchmarks");
  const models = useLoad<ModFull[]>("/models");
  if (!data || !models.data) return <Skeleton />;
  if (data.length === 0)
    return (
      <Banner>
        No benchmark has been run. Start one from Providers &amp; models. Results are visible to
        admins only.
      </Banner>
    );
  const name = (id: number) => models.data!.find((m) => m.id === id)?.name ?? `model ${id}`;
  return (
    <div className="stack">
      {data.map((b) => (
        <Card
          key={b.id}
          title={`${name(b.model_id)} — ${Math.round(b.report.pass_rate * 100)}% passed`}
        >
          <p className="muted">
            {new Date(b.time).toLocaleString()} · {b.report.tokens_per_sec.toFixed(1)} tok/s · first
            token {Math.round(b.report.ttft_ms)} ms · tool failures{" "}
            {Math.round(b.report.tool_call_failure_rate * 100)}%
          </p>
          <ul>
            {b.report.tasks.map((t) => (
              <li key={t.id}>
                <Badge tone={t.passed ? "success" : "danger"}>{t.passed ? "pass" : "fail"}</Badge>{" "}
                <strong>{t.id}</strong>{" "}
                <span className="muted">
                  {t.seconds.toFixed(0)}s — {t.detail}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      ))}
    </div>
  );
}

interface McpPkg {
  index: number;
  package: {
    registryType: string;
    identifier: string;
    version: string;
    env: { name: string; required: boolean; secret: boolean; description: string }[];
  };
  review: {
    capabilities: string[];
    warnings: string[];
    needs_credentials: string[];
    pinnable: boolean;
  };
}
interface McpServerEntry {
  name: string;
  title: string;
  description: string;
  repository: string;
  status: string;
  remote_only: boolean;
  packages: McpPkg[];
}
interface McpApproved {
  name: string;
  package: string;
  type: string;
  env_secrets: Record<string, string>;
  approved_by: string;
}

function McpTab() {
  const toast = useToast();
  const approved = useLoad<McpApproved[]>("/mcp/approved");
  const secrets = useLoad<string[]>("/secrets");
  const [q, setQ] = useState("");
  const [results, setResults] = useState<McpServerEntry[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [review, setReview] = useState<{ server: McpServerEntry; pkg: McpPkg } | null>(null);
  const [map, setMap] = useState<Record<string, string>>({});
  const [newName, setNewName] = useState("");
  const [newValue, setNewValue] = useState("");
  async function search(e?: FormEvent) {
    e?.preventDefault();
    setBusy(true);
    try {
      setResults(
        (await get<{ servers: McpServerEntry[] }>(`/mcp/search?q=${encodeURIComponent(q)}`))
          .servers,
      );
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  async function approve() {
    if (!review) return;
    try {
      await post("/mcp/approve", {
        name: review.server.name,
        package: review.pkg.index,
        env_secrets: map,
      });
      toast("Approved.", "success");
      setReview(null);
      approved.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <div className="stack">
      <Banner>
        MCP servers are third-party code. Each approved server runs in its own gVisor container with
        only the credentials you map to it; its output is treated as untrusted and state-changing
        tools ask the user first.
      </Banner>
      <Card title="Approved servers">
        {!approved.data ? (
          <Skeleton lines={2} />
        ) : approved.data.length === 0 ? (
          <p className="muted">None yet.</p>
        ) : (
          <ul>
            {approved.data.map((a) => (
              <li key={a.name} className="row">
                <strong>{a.name}</strong>
                <span className="muted grow">{a.package}</span>
                <Button
                  variant="danger"
                  onClick={async () => {
                    await del(`/mcp/approved?name=${encodeURIComponent(a.name)}`);
                    approved.reload();
                  }}
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card title="Credentials for servers (instance secrets)">
        <p className="muted">
          Names only are shown. Map these to a server's variables when approving it.
        </p>
        <ul>
          {(secrets.data ?? []).map((n) => (
            <li key={n} className="row">
              <code className="grow">{n}</code>
              <Button
                variant="danger"
                onClick={async () => {
                  await del(`/secrets/${n}`);
                  secrets.reload();
                }}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            try {
              await put(`/secrets/${newName}`, { value: newValue });
              setNewName("");
              setNewValue("");
              secrets.reload();
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          <Input
            label="Name"
            hint="UPPER_SNAKE_CASE"
            value={newName}
            onChange={(e) => setNewName(e.target.value.toUpperCase())}
            required
          />
          <Input
            label="Value"
            type="password"
            autoComplete="off"
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            required
          />
          <Button type="submit">Save credential</Button>
        </form>
      </Card>
      <Card title="Browse the MCP registry">
        <form className="row" onSubmit={search}>
          <div className="grow">
            <Input
              label="Search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="e.g. filesystem, github, docs"
            />
          </div>
          <Button variant="primary" type="submit" loading={busy}>
            Search
          </Button>
        </form>
        {results && results.length === 0 && <p className="muted">No servers found.</p>}
        {results?.map((r) => (
          <div key={r.name} className="step" style={{ padding: 12 }}>
            <strong>{r.title || r.name}</strong> <span className="muted">{r.name}</span>{" "}
            {r.status && r.status !== "active" && <Badge tone="warn">{r.status}</Badge>}
            <p>{r.description}</p>
            {r.remote_only ? (
              <Badge tone="warn">remote only: cannot run in a sandbox</Badge>
            ) : (
              r.packages.map((p) => (
                <Button
                  key={p.index}
                  onClick={() => {
                    setReview({ server: r, pkg: p });
                    setMap({});
                  }}
                >
                  Review {p.package.registryType}: {p.package.identifier}@{p.package.version}
                </Button>
              ))
            )}
          </div>
        ))}
      </Card>
      {review && (
        <Modal
          title={`Approve ${review.server.name}?`}
          onClose={() => setReview(null)}
          actions={
            <>
              <Button onClick={() => setReview(null)}>Cancel</Button>
              <Button variant="primary" onClick={approve} disabled={!review.pkg.review.pinnable}>
                Approve
              </Button>
            </>
          }
        >
          <p className="muted">
            {review.pkg.package.registryType} package {review.pkg.package.identifier}@
            {review.pkg.package.version}. {review.server.repository}
          </p>
          <ul>
            {review.pkg.review.capabilities.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
          {review.pkg.review.warnings.map((w) => (
            <Banner key={w} tone="warn">
              {w}
            </Banner>
          ))}
          {review.pkg.package.env.map((ev) => (
            <Select
              key={ev.name}
              label={`${ev.name}${ev.required ? " (required)" : ""}`}
              value={map[ev.name] ?? ""}
              onChange={(e) => setMap({ ...map, [ev.name]: e.target.value })}
              options={[
                { value: "", label: ev.required ? "Choose a credential…" : "Not set" },
                ...(secrets.data ?? []).map((n) => ({ value: n, label: n })),
              ]}
            />
          ))}
        </Modal>
      )}
    </div>
  );
}

function Table({ rows, cols }: { rows: Record<string, unknown>[]; cols: string[] }) {
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label="Table">
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
        <h1 className="brand">Syntaxis admin</h1>
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
      <main className="main">
        <Tabs
          value={tab}
          onChange={setTab}
          tabs={[
            { id: "health", label: "Health" },
            { id: "users", label: "Users" },
            { id: "providers", label: "Providers & models" },
            { id: "bench", label: "Benchmarks" },
            { id: "mcp", label: "MCP servers" },
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
        {tab === "bench" && <BenchmarksTab />}
        {tab === "mcp" && <McpTab />}
        {tab === "sessions" && <SessionsTab />}
        {tab === "audit" && <AuditTab />}
        {tab === "egress" && <EgressTab />}
        {tab === "settings" && <SettingsTab />}
        {tab === "kill" && <KillTab />}
      </main>
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
