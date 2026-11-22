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
  TextArea,
  ToastProvider,
  Toggle,
  VersionLabel,
  useToast,
  type Tone,
} from "../ui/ui";
import { AuthFrame, Login, SetPassword, TwoFactorSetup } from "../user/Auth";
import { useMe } from "../user/hooks";
import { SessionMessagesDialog, UserSettingsDialog } from "./UserSettings";

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
interface PolicyData {
  daily_tokens?: number;
  monthly_tokens?: number;
  max_sessions?: number;
  cpu_minutes?: number;
  egress_mode?: string;
  allowed_models?: string[];
  no_terminal?: boolean;
  no_cloud_tools?: boolean;
}
interface NamedPolicy {
  id: number;
  name: string;
  kind: string;
  policy: PolicyData;
  members?: number;
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
  level?: string;
  group_id?: number;
  quota_cpu_minutes?: number;
}
interface Prov {
  id: number;
  name: string;
  kind: string;
  base_url: string;
  concurrency: number;
  enabled: boolean;
  has_key: boolean;
  stream_probe: { ok?: boolean; detail?: string };
  helper_model: string;
  helper_assert: boolean;
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
      {(data.findings ?? []).length === 0 && <Banner>All checks passed.</Banner>}
      {(data.findings ?? []).map((f) => (
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
  const [settingsOf, setSettingsOf] = useState<User | null>(null);
  const [link, setLink] = useState<{ url: string; who: string; warning?: string } | null>(null);
  async function invite(e: FormEvent) {
    e.preventDefault();
    try {
      const r = await post<{ invite_url: string; warning?: string }>("/users", { email, name });
      setLink({ url: r.invite_url, who: name, warning: r.warning });
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
                  <Button onClick={() => setSettingsOf(u)}>Settings</Button>
                  {u.invited && (
                    <Button
                      onClick={async () => {
                        try {
                          const r = await post<{ invite_url: string; warning?: string }>(
                            `/users/${u.id}/resend`,
                          );
                          setLink({ url: r.invite_url, who: u.name, warning: r.warning });
                        } catch (x) {
                          toast(errText(x), "error");
                        }
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
      {link && (
        <Modal
          title={`Invite link for ${link.who}`}
          onClose={() => setLink(null)}
          actions={
            <>
              <Button
                onClick={() => {
                  void navigator.clipboard?.writeText(link.url);
                  toast("Link copied.", "success");
                }}
              >
                Copy link
              </Button>
              <Button variant="primary" onClick={() => setLink(null)}>
                Done
              </Button>
            </>
          }
        >
          {link.warning ? (
            <Banner tone="warn">{link.warning}. Send them this link yourself.</Banner>
          ) : (
            <p>The same link was emailed to them. You can also pass it on yourself.</p>
          )}
          <Input
            label="Single-use link (valid for 3 days; anyone who has it can set the password)"
            readOnly
            value={link.url}
            onFocus={(e) => e.target.select()}
          />
        </Modal>
      )}
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
              Invite
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
      {settingsOf && <UserSettingsDialog user={settingsOf} onClose={() => setSettingsOf(null)} />}
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
  const [level, setLevel] = useState(u.level ?? "user");
  const [group, setGroup] = useState(String(u.group_id ?? 0));
  const [cpu, setCpu] = useState(String(u.quota_cpu_minutes ?? 0));
  const groups = useLoad<NamedPolicy[]>("/groups");
  async function save() {
    try {
      await patch(`/users/${u.id}`, {
        quota_daily_tokens: Number(daily),
        quota_monthly_tokens: Number(monthly),
        egress_mode: egress,
        disabled,
        terminal_disabled: terminalOff,
        level,
        group_id: Number(group),
        quota_cpu_minutes: Number(cpu),
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
      <Select
        label="Level"
        value={level}
        onChange={(e) => setLevel(e.target.value)}
        options={[
          { value: "user", label: "User" },
          { value: "poweruser", label: "Power user" },
          { value: "admin", label: "Admin (needs an admin account with the same email)" },
        ]}
      />
      <Select
        label="Group"
        value={group}
        onChange={(e) => setGroup(e.target.value)}
        options={[
          { value: "0", label: "No group" },
          ...(groups.data ?? []).map((g) => ({ value: String(g.id), label: g.name })),
        ]}
      />
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
      <Input
        label="Daily compute minutes"
        hint="0 = from the group, level or default; -1 = unlimited"
        value={cpu}
        onChange={(e) => setCpu(e.target.value)}
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
  tool_probe: { ok?: boolean; detail?: string };
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
          <Select
            label="Helper model for cheap jobs (titles, summaries)"
            value={p.helper_model}
            onChange={async (e) => {
              try {
                const r = await put<{ notice?: string }>(`/providers/${p.id}/helper`, {
                  model: e.target.value,
                  assert_resident: p.helper_assert,
                });
                toast(r.notice ?? "Saved.", r.notice ? "info" : "success");
                provs.reload();
              } catch (x) {
                toast(errText(x), "error");
              }
            }}
            options={[
              { value: "", label: "None (the session's own model does everything)" },
              ...(models.data ?? [])
                .filter((m) => m.provider_id === p.id)
                .map((m) => ({ value: m.name, label: m.name })),
            ]}
          />
          <p className="muted">
            Used only while the server reports it is already loaded; Syntaxis never loads or swaps a
            model. If it is not loaded, or fails, the session&apos;s own model does the job.
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
                  <th>Live probe</th>
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
                        <td title={m.tool_probe.detail ?? "Not probed yet"}>
                          {m.tool_probe.ok === undefined ? (
                            <span className="muted">not probed</span>
                          ) : m.tool_probe.ok ? (
                            "calls tools"
                          ) : (
                            "does not call tools"
                          )}
                          {p.stream_probe.ok === false && (
                            <>
                              {" · "}
                              <strong title={p.stream_probe.detail}>stream is buffered</strong>
                            </>
                          )}
                        </td>
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
                                await post(`/models/${m.id}/probe`);
                                toast(
                                  "Probe started. It may load the model, so it runs in the background.",
                                  "success",
                                );
                                setTimeout(() => {
                                  models.reload();
                                  provs.reload();
                                }, 30000);
                              } catch (x) {
                                toast(errText(x), "error");
                              }
                            }}
                          >
                            Probe
                          </Button>{" "}
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

interface EnvTpl {
  name: string;
  image: string;
  matches: string[];
  description: string;
  present: boolean;
}

interface CatalogRow {
  name: string;
  adds: string;
  size_gb: number;
  minutes: string;
  state: "not built" | "queued" | "building" | "removing" | "built" | "failed";
  managed?: boolean;
  last_used?: string;
  message?: string;
}
interface Catalog {
  environments: CatalogRow[];
  agent_running: boolean;
  idle_days: number;
  manual: string;
}

function EnvironmentCatalog({ onChange }: { onChange: () => void }) {
  const toast = useToast();
  const [cat, setCat] = useState<Catalog | null>(null);
  const [ask, setAsk] = useState<CatalogRow | null>(null);
  const [days, setDays] = useState("");
  const load = useCallback(async () => {
    try {
      const c = await get<Catalog>("/environment-catalog");
      setCat(c);
      setDays((d) => d || String(c.idle_days));
    } catch (x) {
      toast(errText(x), "error");
    }
  }, [toast]);
  useEffect(() => {
    void load();
  }, [load]);
  const working = cat?.environments.some((e) =>
    ["queued", "building", "removing"].includes(e.state),
  );
  useEffect(() => {
    if (!working) return;
    const t = setInterval(() => {
      void load();
      onChange();
    }, 10000);
    return () => clearInterval(t);
  }, [working, load, onChange]);
  async function build() {
    if (!ask) return;
    try {
      await post(`/environment-catalog/${ask.name}/build`);
      toast("Build requested. It runs on the Docker host and takes a while.", "success");
    } catch (x) {
      toast(errText(x), "error");
    }
    setAsk(null);
    void load();
  }
  if (!cat) return <Skeleton />;
  return (
    <Card title="Example environments">
      <p className="muted">
        These are not built until you choose them. Building runs on the Docker host and takes a few
        minutes or more (see the time and disk space beside each). Once built, an environment is
        approved automatically, and it is removed again if nobody uses it for a while.
      </p>
      {!cat.agent_running && (
        <Banner tone="warn">
          The update service on the Docker host is not running, so images cannot be built from here.
          Start it with <code>sudo systemctl start syntaxis-update-agent</code>, or build one by
          hand with <code>{cat.manual}</code>.
        </Banner>
      )}
      <div className="table-wrap" tabIndex={0} role="region" aria-label="Example environments">
        <table>
          <thead>
            <tr>
              <th>Environment</th>
              <th>Adds</th>
              <th>Disk</th>
              <th>Build time</th>
              <th>Status</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {cat.environments.map((e) => (
              <tr key={e.name}>
                <td>
                  <strong>{e.name}</strong>
                </td>
                <td>{e.adds}</td>
                <td>about {e.size_gb} GB</td>
                <td>{e.minutes} min</td>
                <td title={e.message}>
                  {e.state === "built" ? (
                    <>
                      built
                      {e.last_used && (
                        <span className="muted">
                          {" "}
                          · used {new Date(e.last_used).toLocaleDateString()}
                        </span>
                      )}
                    </>
                  ) : (
                    e.state
                  )}
                </td>
                <td className="actions-cell">
                  {(e.state === "not built" || e.state === "failed") && (
                    <Button disabled={!cat.agent_running} onClick={() => setAsk(e)}>
                      Build
                    </Button>
                  )}
                  {e.state === "built" && e.managed && (
                    <Button
                      variant="danger"
                      disabled={!cat.agent_running}
                      onClick={async () => {
                        try {
                          await post(`/environment-catalog/${e.name}/remove`);
                          void load();
                        } catch (x) {
                          toast(errText(x), "error");
                        }
                      }}
                    >
                      Remove
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="row">
        <Input
          label="Remove built environments unused for this many days (0 keeps them)"
          type="number"
          min={0}
          value={days}
          onChange={(e) => setDays(e.target.value)}
        />
        <Button
          onClick={async () => {
            try {
              await put("/environment-settings", { idle_days: Number(days) });
              toast("Saved.", "success");
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          Save
        </Button>
      </div>
      {ask && (
        <Modal
          title={`Build ${ask.name}?`}
          onClose={() => setAsk(null)}
          actions={
            <>
              <Button onClick={() => setAsk(null)}>Cancel</Button>
              <Button variant="primary" onClick={() => void build()}>
                Build it
              </Button>
            </>
          }
        >
          <p>
            This takes about {ask.minutes} minutes and about {ask.size_gb} GB of disk on the Docker
            host. It downloads packages from the internet, and other builds and updates wait until
            it finishes. Syntaxis keeps working meanwhile.
          </p>
        </Modal>
      )}
    </Card>
  );
}

function EnvironmentsTab() {
  const toast = useToast();
  const list = useLoad<EnvTpl[]>("/image-templates");
  const [f, setF] = useState({ name: "", image: "", matches: "", description: "" });
  async function save() {
    try {
      const r = await put<{ present: boolean }>(`/image-templates/${f.name}`, {
        image: f.image,
        matches: f.matches.split("\n").map((x) => x.trim()),
        description: f.description,
      });
      toast(
        r.present
          ? "Saved."
          : "Saved, but that image is not on the Docker host yet. Build or pull it there.",
        r.present ? "success" : "info",
      );
      setF({ name: "", image: "", matches: "", description: "" });
      list.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <>
      <p className="muted">
        Environments are sandbox images you have prepared or built from the examples below. Users
        can pick one for a session, and a repository&apos;s devcontainer.json is matched to one by
        the images listed here. A repository can never choose an image of its own.
      </p>
      <EnvironmentCatalog onChange={list.reload} />
      {(list.data ?? []).map((t) => (
        <Card key={t.name} title={t.name}>
          <p>
            <code>{t.image}</code> {!t.present && <strong>· not on the Docker host</strong>}
          </p>
          {t.description && <p className="muted">{t.description}</p>}
          {t.matches.length > 0 && <p className="muted">Matches: {t.matches.join(", ")}</p>}
          <Button
            variant="danger"
            onClick={async () => {
              await del(`/image-templates/${t.name}`);
              list.reload();
            }}
          >
            Remove
          </Button>
        </Card>
      ))}
      <Card title="Add or replace an environment">
        <Input label="Name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
        <Input
          label="Image on the Docker host"
          value={f.image}
          onChange={(e) => setF({ ...f, image: e.target.value })}
        />
        <Input
          label="Description (shown to users)"
          value={f.description}
          onChange={(e) => setF({ ...f, description: e.target.value })}
        />
        <TextArea
          label="devcontainer.json images that map to it (one per line, tags ignored)"
          value={f.matches}
          onChange={(e) => setF({ ...f, matches: e.target.value })}
        />
        <Button variant="primary" onClick={save} disabled={!f.name || !f.image}>
          Save
        </Button>
      </Card>
    </>
  );
}

interface UpdateInfo {
  current: { version: string; commit: string; date: string };
  mode: "release" | "edge";
  releases: { tag: string; name: string; published: string; url: string; prerelease: boolean }[];
  latest?: { tag: string; published: string; url: string };
  release_available?: boolean;
  edge?: { sha: string; date: string; subject: string; newer: boolean };
  check_error?: string;
  agent: { running: boolean; manual: string };
  status: { state?: string; target?: string; message?: string; at?: string };
}

function UpdatesTab() {
  const toast = useToast();
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [lost, setLost] = useState(false);
  const [pick, setPick] = useState("");
  const [confirm, setConfirm] = useState<{ target: string; label: string } | null>(null);
  const load = useCallback(async (refresh = false) => {
    try {
      setInfo(await get<UpdateInfo>("/update" + (refresh ? "?refresh=1" : "")));
      setLost(false);
    } catch {
      setLost(true); // the server restarts while it updates
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const busy = info?.status.state === "requested" || info?.status.state === "running";
  useEffect(() => {
    if (!busy && !lost) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [busy, lost, load]);
  async function start() {
    if (!confirm) return;
    try {
      await post("/update", { target: confirm.target });
      toast("Update requested.", "success");
      setConfirm(null);
      void load();
    } catch (x) {
      toast(errText(x), "error");
      setConfirm(null);
    }
  }
  if (!info) return lost ? <Banner tone="warn">Reconnecting…</Banner> : <Skeleton />;
  const cur = info.current;
  return (
    <div className="stack">
      {(busy || lost) && (
        <Banner tone="info">
          {busy ? `Updating to ${info.status.target}. ` : ""}Syntaxis restarts during an update;
          this page reconnects by itself and sessions that are running will be stopped.
        </Banner>
      )}
      {info.status.state === "failed" && (
        <Banner tone="danger">
          The last update to {info.status.target} failed and was rolled back. {info.status.message}
        </Banner>
      )}
      {info.status.state === "done" && (
        <Banner>
          Updated to {info.status.target}. {info.status.message}
        </Banner>
      )}
      {!info.agent.running && (
        <Banner tone="warn">
          The update service on the Docker host is not running, so updates cannot be started from
          here. Start it with <code>sudo systemctl start syntaxis-update-agent</code>, or update by
          hand with <code>{info.agent.manual}</code>.
        </Banner>
      )}
      <Card title="Running now">
        <p>
          <strong>{cur.version}</strong> {cur.commit && <code>{cur.commit.slice(0, 8)}</code>}{" "}
          <Badge>{info.mode === "release" ? "published release" : "development branch"}</Badge>
        </p>
        {cur.date && <p className="muted">Built {new Date(cur.date).toLocaleString()}</p>}
      </Card>
      <Card title="Releases">
        {info.check_error && <Banner tone="warn">{info.check_error}</Banner>}
        {info.releases.length === 0 && !info.check_error && (
          <p className="muted">No release has been published yet.</p>
        )}
        {info.latest && (
          <p>
            Latest: <strong>{info.latest.tag}</strong>{" "}
            <span className="muted">
              {new Date(info.latest.published).toLocaleDateString()} ·{" "}
              <a href={info.latest.url} target="_blank" rel="noreferrer">
                release notes
              </a>
            </span>{" "}
            {info.release_available ? <Badge tone="warn">new</Badge> : <Badge>up to date</Badge>}
          </p>
        )}
        {info.release_available && info.latest && (
          <Button
            variant="primary"
            disabled={busy || !info.agent.running}
            onClick={() =>
              setConfirm({ target: info.latest!.tag, label: `Update to ${info.latest!.tag}` })
            }
          >
            Update to {info.latest.tag}
          </Button>
        )}
        {info.releases.length > 0 && (
          <div className="row">
            <Select
              label="Switch to another release"
              value={pick}
              onChange={(e) => setPick(e.target.value)}
              options={[
                { value: "", label: "Choose a version" },
                ...info.releases.map((r) => ({
                  value: r.tag,
                  label:
                    r.tag +
                    (r.prerelease ? " (pre-release)" : "") +
                    (r.tag === cur.version ? " — running" : ""),
                })),
              ]}
            />
            <Button
              disabled={!pick || busy || !info.agent.running || pick === cur.version}
              onClick={() => setConfirm({ target: pick, label: `Switch to ${pick}` })}
            >
              Switch
            </Button>
          </div>
        )}
      </Card>
      <Card title="Development branch (edge)">
        <p className="muted">
          Builds the newest code on the main branch on this host. It has the latest changes and
          fixes, and also the newest mistakes; it is not what releases are tested as.
        </p>
        {info.edge && (
          <p>
            Newest commit: <code>{info.edge.sha.slice(0, 8)}</code> {info.edge.subject}{" "}
            {info.edge.newer ? (
              <Badge tone="warn">different from what is running</Badge>
            ) : (
              <Badge>running</Badge>
            )}
          </p>
        )}
        <Button
          disabled={busy || !info.agent.running}
          onClick={() => setConfirm({ target: "edge", label: "Follow the development branch" })}
        >
          {info.mode === "edge"
            ? "Update to the newest commit"
            : "Switch to the development branch"}
        </Button>
      </Card>
      <Button onClick={() => void load(true)}>Check again</Button>
      {confirm && (
        <Modal
          title={confirm.label}
          onClose={() => setConfirm(null)}
          actions={
            <>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button variant="primary" onClick={() => void start()}>
                Update now
              </Button>
            </>
          }
        >
          <p>
            Syntaxis backs up its database, installs the new version and restarts. Running sessions
            are stopped, and the work in their workspaces is kept. If the new version does not start
            properly, the old one and its data are put back automatically.
          </p>
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
  const sugg = useLoad<{
    user_credentials: string[];
    suggestions: { title: string; search: string; env_secrets: Record<string, string> }[];
  }>("/mcp/suggestions");
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
      setMap({});
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
      <Card title="Suggested servers">
        <p className="muted">
          First-party servers that run as a local program, set up so each user connects with their
          own credentials (Settings, Connections). GitLab and Cloudflare publish only hosted
          servers, which Syntaxis does not run.
        </p>
        {(sugg.data?.suggestions ?? []).map((s) => (
          <div key={s.search} className="row">
            <strong className="grow">{s.title}</strong>
            <Button
              onClick={() => {
                setQ(s.search);
                setMap(s.env_secrets);
                void (async () => {
                  const r = await get<{ servers: McpServerEntry[] }>(
                    `/mcp/search?q=${encodeURIComponent(s.search)}`,
                  );
                  setResults(r.servers.filter((x) => x.name === s.search));
                })();
              }}
            >
              Find and review
            </Button>
          </div>
        ))}
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
          onClose={() => {
            setReview(null);
            setMap({});
          }}
          actions={
            <>
              <Button
                onClick={() => {
                  setReview(null);
                  setMap({});
                }}
              >
                Cancel
              </Button>
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
                ...(sugg.data?.user_credentials ?? []).map((n) => ({
                  value: "user:" + n,
                  label: `Each user's own ${n}`,
                })),
                ...(secrets.data ?? []).map((n) => ({ value: n, label: `Shared: ${n}` })),
              ]}
            />
          ))}
        </Modal>
      )}
    </div>
  );
}

function Table({
  rows,
  cols,
  action,
}: {
  rows: Record<string, unknown>[];
  cols: string[];
  action?: (row: Record<string, unknown>) => React.ReactNode;
}) {
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label="Table">
      <table>
        <thead>
          <tr>
            {cols.map((c) => (
              <th key={c}>{c}</th>
            ))}
            {action && <th aria-label="Actions" />}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {cols.map((c) => (
                <td key={c}>{String(r[c] ?? "")}</td>
              ))}
              {action && <td>{action(r)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SessionsTab() {
  const { data } = useLoad<Record<string, unknown>[]>("/sessions");
  const [open, setOpen] = useState("");
  if (!data) return <Skeleton />;
  return (
    <>
      <p className="muted">
        Admins can read every session's messages; each read is recorded in the audit log.
      </p>
      <Table
        rows={data}
        cols={["id", "user_id", "title", "model", "state", "tokens_in", "tokens_out", "updated_at"]}
        action={(r) => <Button onClick={() => setOpen(String(r.id))}>Read</Button>}
      />
      {open && <SessionMessagesDialog id={open} onClose={() => setOpen("")} />}
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
      <Input
        label="Web search server (SearXNG address)"
        hint="For example https://search.example.com. Leave empty to turn web search off. The server needs the json format enabled (search.formats in its settings). A server on the local network also needs SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE."
        value={s.search_url ?? ""}
        onChange={(e) => setS({ ...s, search_url: e.target.value })}
      />
      <Input
        label="Models that accept pictures"
        hint="Model names, separated by commas. Ollama reports this itself; list models here for servers that do not (OpenAI-compatible ones). Other models read pictures as text with OCR."
        value={s.vision_models ?? ""}
        onChange={(e) => setS({ ...s, vision_models: e.target.value })}
      />
      <Toggle
        label="Allow file uploads (Files tab and message attachments)"
        checked={s.uploads_enabled === "true"}
        onChange={(v) => setS({ ...s, uploads_enabled: String(v) })}
      />
      <Input
        label="Largest upload (MB, 1 to 100)"
        value={s.upload_max_mb ?? ""}
        onChange={(e) => setS({ ...s, upload_max_mb: e.target.value })}
      />
      <Input
        label="Kinds of file users may upload"
        hint="Any of: images, pdf, documents (docx, xlsx, pptx, odt, ods, odp), text (txt, md, csv, tsv, json, log). Every upload is also checked by content: programs, scripts, archives, macros, embedded files and encoded data are always refused."
        value={s.upload_kinds ?? ""}
        onChange={(e) => setS({ ...s, upload_kinds: e.target.value })}
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

function PolicyFields({
  value,
  onChange,
}: {
  value: PolicyData;
  onChange: (p: PolicyData) => void;
}) {
  const num = (k: keyof PolicyData) => String(value[k] ?? 0);
  const set = (k: keyof PolicyData, v: string) => onChange({ ...value, [k]: Number(v) || 0 });
  return (
    <div className="stack">
      <p className="muted">
        Leave a number at 0 to leave it unset here. -1 means unlimited. A setting on the user
        overrides these; on/off switches stay off if any of level, group or user turns them off.
      </p>
      <Input
        label="Daily token quota"
        value={num("daily_tokens")}
        onChange={(e) => set("daily_tokens", e.target.value)}
      />
      <Input
        label="Monthly token quota"
        value={num("monthly_tokens")}
        onChange={(e) => set("monthly_tokens", e.target.value)}
      />
      <Input
        label="Concurrent sessions"
        value={num("max_sessions")}
        onChange={(e) => set("max_sessions", e.target.value)}
      />
      <Input
        label="Daily compute minutes"
        value={num("cpu_minutes")}
        onChange={(e) => set("cpu_minutes", e.target.value)}
      />
      <Select
        label="Egress policy"
        value={value.egress_mode ?? ""}
        onChange={(e) => onChange({ ...value, egress_mode: e.target.value })}
        options={[
          { value: "", label: "Not set here" },
          { value: "open", label: "Open" },
          { value: "allowlist", label: "Allowlist" },
          { value: "none", label: "None" },
        ]}
      />
      <Input
        label="Allowed models"
        hint="Comma-separated provider/model ids. Empty = not set here."
        value={(value.allowed_models ?? []).join(", ")}
        onChange={(e) =>
          onChange({
            ...value,
            allowed_models: e.target.value
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
          })
        }
      />
      <Toggle
        label="No interactive terminal"
        checked={!!value.no_terminal}
        onChange={(v) => onChange({ ...value, no_terminal: v })}
      />
      <Toggle
        label="No cloud tooling"
        checked={!!value.no_cloud_tools}
        onChange={(v) => onChange({ ...value, no_cloud_tools: v })}
      />
    </div>
  );
}

const LEVEL_LABEL: Record<string, string> = {
  user: "User",
  poweruser: "Power user",
  admin: "Admin",
};

function GroupsTab() {
  const toast = useToast();
  const levels = useLoad<NamedPolicy[]>("/levels");
  const groups = useLoad<NamedPolicy[]>("/groups");
  const [edit, setEdit] = useState<{
    kind: "level" | "group";
    id: number;
    name: string;
    policy: PolicyData;
  } | null>(null);
  async function save() {
    if (!edit) return;
    try {
      if (edit.kind === "level") await put(`/levels/${edit.name}`, edit.policy);
      else if (edit.id) await put(`/groups/${edit.id}`, { name: edit.name, policy: edit.policy });
      else await post("/groups", { name: edit.name, policy: edit.policy });
      toast("Saved.", "success");
      setEdit(null);
      levels.reload();
      groups.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <div className="stack">
      <Card title="Levels">
        <p className="muted">
          What each kind of user gets by default. Power users usually get higher limits.
        </p>
        <ul>
          {(levels.data ?? []).map((l) => (
            <li key={l.name} className="row">
              <strong className="grow">{LEVEL_LABEL[l.name] ?? l.name}</strong>
              <Button
                onClick={() => setEdit({ kind: "level", id: 0, name: l.name, policy: l.policy })}
              >
                Edit
              </Button>
            </li>
          ))}
        </ul>
      </Card>
      <Card title="Groups">
        <p className="muted">
          Settings shared by a set of people. Put a user in a group from Users, then Edit.
        </p>
        {groups.data && groups.data.length === 0 && <p className="muted">No groups yet.</p>}
        <ul>
          {(groups.data ?? []).map((g) => (
            <li key={g.id} className="row">
              <strong className="grow">
                {g.name} <span className="muted">({g.members ?? 0} members)</span>
              </strong>
              <Button
                onClick={() => setEdit({ kind: "group", id: g.id, name: g.name, policy: g.policy })}
              >
                Edit
              </Button>
              <Button
                variant="danger"
                onClick={async () => {
                  await del(`/groups/${g.id}`);
                  groups.reload();
                }}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
        <Button
          variant="primary"
          onClick={() => setEdit({ kind: "group", id: 0, name: "", policy: {} })}
        >
          New group
        </Button>
      </Card>
      {edit && (
        <Modal
          title={
            edit.kind === "level"
              ? `Level: ${LEVEL_LABEL[edit.name] ?? edit.name}`
              : edit.id
                ? "Edit group"
                : "New group"
          }
          onClose={() => setEdit(null)}
          actions={
            <>
              <Button onClick={() => setEdit(null)}>Cancel</Button>
              <Button variant="primary" onClick={save}>
                Save
              </Button>
            </>
          }
        >
          {edit.kind === "group" && (
            <Input
              label="Group name"
              value={edit.name}
              onChange={(e) => setEdit({ ...edit, name: e.target.value })}
            />
          )}
          <PolicyFields value={edit.policy} onChange={(p) => setEdit({ ...edit, policy: p })} />
        </Modal>
      )}
    </div>
  );
}

function Console({
  name,
  signOut,
  chatUrl,
}: {
  name: string;
  signOut: () => void;
  chatUrl?: string;
}) {
  const [tab, setTab] = useState("health");
  const { data: h } = useLoad<Health>("/health");
  const { data: upd } = useLoad<UpdateInfo>("/update");
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
        <VersionLabel />
        {upd?.release_available && upd.latest && (
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => setTab("updates")}
            title="A new release is available"
          >
            Update available: {upd.latest.tag}
          </button>
        )}
        {chatUrl && (
          <a className="btn btn-ghost" href={chatUrl} title="Back to the chat app">
            Chat app
          </a>
        )}
        <span className="muted account-name">{name}</span>
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
            { id: "groups", label: "Levels & groups" },
            { id: "providers", label: "Providers & models" },
            { id: "envs", label: "Environments" },
            { id: "updates", label: upd?.release_available ? "Updates ●" : "Updates" },
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
        {tab === "groups" && <GroupsTab />}
        {tab === "providers" && <ProvidersTab />}
        {tab === "envs" && <EnvironmentsTab />}
        {tab === "updates" && <UpdatesTab />}
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

/** The installer's single-use link: #setup=<token> (a fragment, so it never reaches a server log). */
function readSetupToken(): string {
  const m = /^#setup=([A-Za-z0-9_-]+)$/.exec(window.location.hash);
  return m?.[1] ?? "";
}

export default function AdminApp() {
  const { me, reload, clear } = useMe();
  const [setupToken, setSetupToken] = useState(readSetupToken);
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
  else if (me === null && setupToken)
    body = (
      <SetPassword
        kind="setup"
        token={setupToken}
        go={() => {
          // drop the used token from the address bar and history
          window.history.replaceState(null, "", window.location.pathname);
          setSetupToken("");
        }}
      />
    );
  else if (me === null)
    body = <Login prefix="Admin" go={() => undefined} onDone={() => void reload()} />;
  else if (!me.totp_enabled)
    body = (
      <AuthFrame title="Set up two-factor authentication">
        <TwoFactorSetup mandatory onDone={() => void reload()} />
      </AuthFrame>
    );
  else body = <Console name={me.name} signOut={signOut} chatUrl={me.chat_url} />;
  return <ToastProvider>{body}</ToastProvider>;
}
