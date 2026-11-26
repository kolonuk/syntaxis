// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { del, get, patch, post, put, errText } from "../lib/api";
import { applyTheme, loadPref } from "../lib/theme";
import { GroupedTabs, type TabGroup } from "../ui/GroupedTabs";
import {
  Badge,
  Banner,
  Button,
  Card,
  Input,
  Modal,
  Select,
  Skeleton,
  TextArea,
  ToastProvider,
  Toggle,
  VersionLabel,
  useToast,
} from "../ui/ui";
import { AuthFrame, Login, SetPassword, TwoFactorSetup } from "../user/Auth";
import { useMe } from "../user/hooks";
import { AdminsTab } from "./AdminsTab";
import { AuditTab } from "./AuditTab";
import { ConfigTab } from "./ConfigTab";
import { McpTab } from "./McpTab";
import { BenchmarksTab, ProvidersTab } from "./ProvidersTab";
import { Table, tone, useLoad } from "./shared";
import { UpdatesTab, type UpdateInfo } from "./UpdatesTab";
import { SessionMessagesDialog, UserSettingsDialog } from "./UserSettings";

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
/** A finding the admin can act on in another tab: where, and what to call the button. */
const FIX_TABS: Record<string, { tab: string; label: string }> = {
  "security.admin-2fa": { tab: "admins", label: "Open Admins" },
  "security.smtp": { tab: "config", label: "Open Configuration" },
  "security.default-quotas": { tab: "settings", label: "Open Policies" },
  "security.egress-open": { tab: "settings", label: "Open Policies" },
};

function HealthTab({ go }: { go: (tab: string) => void }) {
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
            {FIX_TABS[f.check_id] && (
              <Button onClick={() => go(FIX_TABS[f.check_id]!.tab)}>
                {FIX_TABS[f.check_id]!.label}
              </Button>
            )}
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

export function UsersTab() {
  const toast = useToast();
  const { data, reload } = useLoad<User[]>("/users");
  const groups = useLoad<NamedPolicy[]>("/groups");
  const groupName = (id?: number) =>
    id ? (groups.data?.find((g) => g.id === id)?.name ?? `group ${id}`) : "";
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
              <th>Level</th>
              <th>Group</th>
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
                <td>{u.level || "user"}</td>
                <td>{groupName(u.group_id) || <span className="muted">none</span>}</td>
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
        <Modal
          title="Invite user"
          onClose={() => setAdding(false)}
          actions={
            <>
              <Button onClick={() => setAdding(false)}>Cancel</Button>
              <Button variant="primary" type="submit" form="invite-form">
                Invite
              </Button>
            </>
          }
        >
          <form id="invite-form" onSubmit={invite}>
            <Input
              label="Name"
              placeholder="Alex Developer"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
            <Input
              label="Email"
              placeholder="alex@example.com"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              hint="They receive a single-use link to set their own password."
            />
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
        placeholder="500000"
        hint="0 = default, -1 = unlimited"
        value={daily}
        onChange={(e) => setDaily(e.target.value)}
      />
      <Input
        label="Monthly token quota"
        placeholder="10000000"
        value={monthly}
        onChange={(e) => setMonthly(e.target.value)}
      />
      <Input
        label="Daily compute minutes"
        placeholder="240"
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
        placeholder="local/small, local/large"
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
          <p>
            Images are built by the update service on the Docker host, and it is not running, so
            nothing can be built from here. Start it with{" "}
            <code>sudo systemctl start syntaxis-update-agent</code>, or build one by hand on the
            host with <code>{cat.manual}</code>.
          </p>
          <p>
            If Syntaxis is deployed by another tool, that tool has to provide the service (it needs
            the project&apos;s <code>scripts/</code> and <code>images/templates/</code> and access
            to the host&apos;s Docker). Otherwise build the image yourself and add it under
            &quot;Add or replace an environment&quot; below.
          </p>
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
          placeholder="30"
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
        <p className="muted">
          For an image you built or pulled on the Docker host yourself, for example one with your
          team&apos;s tools. Syntaxis does not build it from here.
        </p>
        <Input
          label="Name"
          placeholder="rust-nightly"
          hint="Lowercase letters, digits and dashes. Users see this name."
          value={f.name}
          onChange={(e) => setF({ ...f, name: e.target.value })}
        />
        <Input
          label="Image on the Docker host"
          placeholder="registry.example.com/team/dev-rust:2"
          hint="As docker images lists it, with its tag. An image made from the examples above is named syntaxis-env-<name>:1."
          value={f.image}
          onChange={(e) => setF({ ...f, image: e.target.value })}
        />
        <Input
          label="Description (shown to users)"
          placeholder="Rust nightly with cargo-nextest and clippy"
          hint="One short line, up to 200 characters."
          value={f.description}
          onChange={(e) => setF({ ...f, description: e.target.value })}
        />
        <TextArea
          label="devcontainer.json images that map to it (one per line, tags ignored)"
          placeholder={"mcr.microsoft.com/devcontainers/rust\nghcr.io/example/devcontainers/rust"}
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
      <p className="muted">
        Password rotation, the secret scanner, email, sandbox lifetimes and the other options that
        can also be set in Docker are under Configuration. These are the policies that are only set
        here.
      </p>
      <Input
        label="Default daily tokens (0 = none)"
        placeholder="500000"
        value={s.default_daily_tokens ?? ""}
        onChange={(e) => setS({ ...s, default_daily_tokens: e.target.value })}
      />
      <Input
        label="Default monthly tokens (0 = none)"
        placeholder="10000000"
        value={s.default_monthly_tokens ?? ""}
        onChange={(e) => setS({ ...s, default_monthly_tokens: e.target.value })}
      />
      <Input
        label="Web search server (SearXNG address)"
        placeholder="https://search.example.com"
        hint="For example https://search.example.com. Leave empty to turn web search off. The server needs the json format enabled (search.formats in its settings). A server on the local network also needs SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE."
        value={s.search_url ?? ""}
        onChange={(e) => setS({ ...s, search_url: e.target.value })}
      />
      <Input
        label="Models that accept pictures"
        placeholder="example-vision:7b, example-vision:13b"
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
        placeholder="10"
        value={s.upload_max_mb ?? ""}
        onChange={(e) => setS({ ...s, upload_max_mb: e.target.value })}
      />
      <Input
        label="Kinds of file users may upload"
        placeholder="images,pdf,documents,text"
        hint="Any of: images, pdf, documents (docx, xlsx, pptx, odt, ods, odp), text (txt, md, csv, tsv, json, log). Every upload is also checked by content: programs, scripts, archives, macros, embedded files and encoded data are always refused."
        value={s.upload_kinds ?? ""}
        onChange={(e) => setS({ ...s, upload_kinds: e.target.value })}
      />
      <div className="row">
        <Button
          variant="primary"
          onClick={async () => {
            try {
              // only what this tab shows: sending the rest would turn the Docker values of options on
              // the Configuration tab into saved ones
              const own = [
                "default_daily_tokens",
                "default_monthly_tokens",
                "search_url",
                "vision_models",
                "uploads_enabled",
                "upload_max_mb",
                "upload_kinds",
              ];
              await put(
                "/settings",
                Object.fromEntries(own.filter((k) => k in s).map((k) => [k, s[k]])),
              );
              toast("Saved.", "success");
              reload();
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          Save
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
        placeholder="500000"
        value={num("daily_tokens")}
        onChange={(e) => set("daily_tokens", e.target.value)}
      />
      <Input
        label="Monthly token quota"
        placeholder="10000000"
        value={num("monthly_tokens")}
        onChange={(e) => set("monthly_tokens", e.target.value)}
      />
      <Input
        label="Concurrent sessions"
        placeholder="3"
        value={num("max_sessions")}
        onChange={(e) => set("max_sessions", e.target.value)}
      />
      <Input
        label="Daily compute minutes"
        placeholder="240"
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
        placeholder="local/small, local/large"
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
              placeholder="Platform team"
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

/** The admin menu: related screens are grouped, and each group has its own row of tabs. */
export function navGroups(updateAvailable: boolean): TabGroup[] {
  return [
    {
      id: "health-updates",
      label: updateAvailable ? "Health & updates ●" : "Health & updates",
      tabs: [
        { id: "health", label: "Health" },
        { id: "updates", label: updateAvailable ? "Updates ●" : "Updates" },
      ],
    },
    {
      id: "people",
      label: "Users",
      tabs: [
        { id: "users", label: "Users" },
        { id: "groups", label: "Levels & groups" },
        { id: "admins", label: "Admins" },
      ],
    },
    {
      id: "models",
      label: "Models & tools",
      tabs: [
        { id: "providers", label: "Providers & models" },
        { id: "bench", label: "Benchmarks" },
        { id: "envs", label: "Environments" },
        { id: "mcp", label: "MCP servers" },
      ],
    },
    {
      id: "sessions",
      label: "Sessions & egress",
      tabs: [
        { id: "sessions", label: "Sessions" },
        { id: "egress", label: "Egress" },
        { id: "settings", label: "Policies" },
      ],
    },
    {
      id: "system",
      label: "System",
      tabs: [
        { id: "config", label: "Configuration" },
        { id: "audit", label: "Audit" },
        { id: "kill", label: "Kill switch" },
      ],
    },
  ];
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
        <GroupedTabs
          groups={navGroups(!!upd?.release_available)}
          value={tab}
          onChange={setTab}
          label="Admin sections"
        />
        {tab === "health" && <HealthTab go={setTab} />}
        {tab === "users" && <UsersTab />}
        {tab === "groups" && <GroupsTab />}
        {tab === "admins" && <AdminsTab />}
        {tab === "providers" && <ProvidersTab />}
        {tab === "envs" && <EnvironmentsTab />}
        {tab === "updates" && <UpdatesTab />}
        {tab === "bench" && <BenchmarksTab />}
        {tab === "mcp" && <McpTab />}
        {tab === "sessions" && <SessionsTab />}
        {tab === "audit" && <AuditTab />}
        {tab === "egress" && <EgressTab />}
        {tab === "settings" && <SettingsTab />}
        {tab === "config" && <ConfigTab />}
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
