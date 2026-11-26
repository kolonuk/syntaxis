// SPDX-License-Identifier: GPL-2.0-or-later
import { useState } from "react";
import { del, errText, post, put } from "../lib/api";
import {
  Badge,
  Banner,
  Button,
  Card,
  Input,
  Select,
  Skeleton,
  Tabs,
  Toggle,
  useToast,
} from "../ui/ui";
import { DOCS, useLoad } from "./shared";

export interface ConfigItem {
  key: string;
  group: string;
  label: string;
  help?: string;
  placeholder?: string;
  kind: "text" | "number" | "toggle" | "choice" | "secret" | "list";
  options?: string[];
  env?: string;
  default?: string;
  live: boolean;
  locked: boolean;
  value: string;
  source: "docker" | "database" | "default";
  env_value?: string;
  env_set: boolean;
  has_value: boolean;
}
interface ConfigGroup {
  id: string;
  label: string;
  help: string;
  items: ConfigItem[];
}
interface ConfigData {
  groups: ConfigGroup[];
  restart_pending: string[];
}

/** The badge that says where a value in effect comes from. */
export function SourceBadge({ it }: { it: ConfigItem }) {
  if (it.source === "database") return <Badge tone="info">saved here</Badge>;
  if (it.source === "docker")
    return <Badge tone="warn">from Docker{it.env ? ` (${it.env})` : ""}</Badge>;
  return <Badge>default</Badge>;
}

function Control({
  it,
  value,
  onChange,
}: {
  it: ConfigItem;
  value: string;
  onChange: (v: string) => void;
}) {
  const hint = [it.help, it.live ? undefined : "Takes effect after a restart."]
    .filter(Boolean)
    .join(" ");
  switch (it.kind) {
    case "toggle":
      return (
        <Toggle label={it.label} checked={value === "true"} onChange={(v) => onChange(String(v))} />
      );
    case "choice":
      return (
        <Select
          label={it.label}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          options={(it.options ?? []).map((o) => ({ value: o, label: o }))}
        />
      );
    case "secret":
      return (
        <Input
          label={it.label}
          type="password"
          autoComplete="new-password"
          value={value}
          placeholder={it.has_value ? "Set. Type a new one to replace it." : "Not set"}
          hint={hint}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    default:
      return (
        <Input
          label={it.label}
          type={it.kind === "number" ? "number" : "text"}
          min={it.kind === "number" ? 0 : undefined}
          value={value}
          placeholder={it.placeholder ?? it.default}
          hint={hint}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

function LockedRow({ it }: { it: ConfigItem }) {
  const shown =
    it.kind === "secret" || it.env === "SYNTAXIS_MASTER_KEY"
      ? it.has_value
        ? "set"
        : "not set"
      : it.value || "not set";
  return (
    <div className="row config-row">
      <div className="grow">
        <strong>{it.label}</strong>
        {it.help && <p className="muted">{it.help}</p>}
      </div>
      <code aria-label={`${it.label} value`}>{shown}</code>
      <Badge>{it.env}</Badge>
    </div>
  );
}

function ConfigGroupCard({ g, onSaved }: { g: ConfigGroup; onSaved: (restart: string[]) => void }) {
  const toast = useToast();
  const editable = g.items.filter((i) => !i.locked);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const current = (it: ConfigItem) =>
    it.kind === "secret" ? (edits[it.key] ?? "") : (edits[it.key] ?? it.value);
  const dirty = Object.keys(edits).filter((k) => {
    const it = editable.find((i) => i.key === k);
    return it && (it.kind === "secret" ? edits[k] !== "" : edits[k] !== it.value);
  });

  async function save() {
    const body: Record<string, string> = {};
    for (const k of dirty) body[k] = edits[k]!;
    try {
      const r = await put<{ restart_pending: string[] }>("/config", body);
      toast("Saved.", "success");
      setEdits({});
      onSaved(r.restart_pending);
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  async function restoreDockerValue(it: ConfigItem) {
    try {
      const r = await del<{ restart_pending: string[] }>(`/config/${it.key}`);
      toast(it.env_set ? "Using the value from Docker again." : "Back to the default.", "success");
      setEdits((e) => {
        const n = { ...e };
        delete n[it.key];
        return n;
      });
      onSaved(r.restart_pending);
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  async function testMail() {
    try {
      const r = await post<{ to: string; sent: boolean }>("/mail/test");
      toast(
        r.sent
          ? `Test email sent to ${r.to}.`
          : "No mail server is set, so the email was only written to the log.",
        r.sent ? "success" : "info",
      );
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  return (
    <Card title={g.label}>
      <p className="muted">{g.help}</p>
      {g.id === "deployment" ? (
        <div className="stack">
          {g.items.map((it) => (
            <LockedRow key={it.key} it={it} />
          ))}
        </div>
      ) : (
        <div className="stack" style={{ maxWidth: 560 }}>
          {editable.map((it) => (
            <div key={it.key} className="config-item">
              <Control
                it={it}
                value={current(it)}
                onChange={(v) => setEdits({ ...edits, [it.key]: v })}
              />
              <div className="row">
                <SourceBadge it={it} />
                {!it.live && <Badge>needs a restart</Badge>}
                {it.source === "database" && (
                  <Button onClick={() => void restoreDockerValue(it)}>
                    {it.env_set
                      ? it.kind === "secret"
                        ? "Use the one from Docker again"
                        : `Use the Docker value again (${it.env_value})`
                      : "Remove what I saved"}
                  </Button>
                )}
              </div>
            </div>
          ))}
          <div className="row">
            <Button variant="primary" onClick={() => void save()} disabled={dirty.length === 0}>
              Save {g.label.toLowerCase()}
            </Button>
            {g.id === "email" && <Button onClick={() => void testMail()}>Send test email</Button>}
          </div>
        </div>
      )}
    </Card>
  );
}

/**
 * Every option Syntaxis has, in one place, with where each value comes from. The Docker environment
 * gives the first value; a value saved here replaces it, and can be removed to use Docker's again.
 */
export function ConfigTab() {
  const { data, err, reload } = useLoad<ConfigData>("/config");
  const [restart, setRestart] = useState<string[] | null>(null);
  const [gid, setGid] = useState("");
  if (err) return <Banner tone="danger">{err}</Banner>;
  if (!data) return <Skeleton />;
  const pending = restart ?? data.restart_pending;
  const active = data.groups.some((g) => g.id === gid) ? gid : (data.groups[0]?.id ?? "");
  return (
    <div className="stack">
      <p className="muted">
        Everything Syntaxis can be configured with. The Docker environment (the compose file or{" "}
        <code>.env</code>) gives the first value. A value you save here replaces it, and{" "}
        <em>stays</em> when Docker&apos;s changes, until you choose &quot;use the Docker value
        again&quot;. Each option says which one is in effect. Policies for quotas, uploads and web
        search are on the Policies tab.{" "}
        <a href={`${DOCS}/configuration.md`} target="_blank" rel="noreferrer">
          Reference
        </a>
      </p>
      {pending.length > 0 && (
        <Banner tone="warn">
          These changes take effect after Syntaxis restarts: <strong>{pending.join(", ")}</strong>.
          Restart it from where it is deployed, for example{" "}
          <code>docker compose restart syntaxis</code>.
        </Banner>
      )}
      <Tabs
        label="Configuration groups"
        value={active}
        onChange={setGid}
        tabs={data.groups.map((g) => ({ id: g.id, label: g.label }))}
      />
      {data.groups.map((g) => (
        // Every group stays mounted, so what you typed in one is still there when you come back.
        <div key={g.id} hidden={g.id !== active}>
          <ConfigGroupCard
            g={g}
            onSaved={(r) => {
              setRestart(r);
              reload();
            }}
          />
        </div>
      ))}
    </div>
  );
}
