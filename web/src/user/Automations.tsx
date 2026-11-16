// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { del, get, post, put, errText } from "../lib/api";
import { ago } from "../lib/format";
import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  Input,
  Modal,
  Select,
  Skeleton,
  TextArea,
  Toggle,
  useToast,
} from "../ui/ui";
import { useConnections } from "./Connections";
import { RepoPicker, type RepoChoice } from "./RepoPicker";

interface Automation {
  id: string;
  name: string;
  enabled: boolean;
  trigger: "schedule" | "webhook" | "watch";
  config: Record<string, string>;
  prompt: string;
  model: string;
  autonomy: string;
  notify?: Record<string, string>;
  repo?: { kind: string; full_name: string; clone_url: string; default_branch: string };
  last_run_at: string;
  next_run_at: string;
  last_status: string;
  last_error: string;
}
interface WatchField {
  name: string;
  label: string;
  hint?: string;
  required: boolean;
}
interface Kinds {
  watch: { id: string; title: string; fields: WatchField[] }[];
  min_schedule_minutes: number;
  min_watch_minutes: number;
}
interface ModelOpt {
  id: string;
  recommended: boolean;
}

const PRESETS = [
  { value: "0 * * * *", label: "Every hour" },
  { value: "0 9 * * *", label: "Every day at 09:00" },
  { value: "0 3 * * *", label: "Every night at 03:00" },
  { value: "0 9 * * 1", label: "Every Monday at 09:00" },
  { value: "0 9 1 * *", label: "The 1st of every month at 09:00" },
  { value: "", label: "Custom (cron expression)" },
];

const empty = (model: string): Draft => ({
  id: "",
  name: "",
  trigger: "schedule",
  config: { cron: "0 9 * * *", tz: Intl.DateTimeFormat().resolvedOptions().timeZone },
  prompt: "",
  model,
  autonomy: "workspace",
  notify: {},
  repo: null,
  enabled: true,
});
interface Draft {
  id: string;
  name: string;
  trigger: "schedule" | "webhook" | "watch";
  config: Record<string, string>;
  prompt: string;
  model: string;
  autonomy: string;
  notify: Record<string, string>;
  repo: RepoChoice | null;
  enabled: boolean;
}

function describe(a: Automation): string {
  if (a.trigger === "schedule") return `On a schedule: ${a.config.cron} (${a.config.tz || "UTC"})`;
  if (a.trigger === "webhook") return "When its web address is called";
  return `Watching ${a.config.kind?.replace("_", " ")}: ${a.config.repo ?? a.config.image ?? a.config.name ?? a.config.url ?? ""}`;
}

function Editor({
  initial,
  kinds,
  models,
  onClose,
  onSaved,
}: {
  initial: Draft;
  kinds: Kinds;
  models: ModelOpt[];
  onClose: () => void;
  onSaved: (hook?: string) => void;
}) {
  const toast = useToast();
  const conns = useConnections();
  const [d, setD] = useState(initial);
  const [busy, setBusy] = useState(false);
  const cfg = (k: string, v: string) => setD({ ...d, config: { ...d.config, [k]: v } });
  const preset = PRESETS.some((p) => p.value === d.config.cron && p.value) ? d.config.cron : "";
  const kind = kinds.watch.find((k) => k.id === d.config.kind);
  const hasIFTTT = (conns.data ?? []).some((c) => c.kind.id === "ifttt" && c.connected);

  async function save() {
    setBusy(true);
    try {
      const body = {
        name: d.name,
        enabled: d.enabled,
        trigger: d.trigger,
        config: d.config,
        prompt: d.prompt,
        model: d.model,
        autonomy: d.autonomy,
        notify: d.notify,
        repo: d.repo,
      };
      const r = d.id
        ? await put<{ webhook_url?: string }>(`/automations/${d.id}`, body)
        : await post<{ webhook_url?: string }>("/automations", body);
      onSaved(r.webhook_url);
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={d.id ? "Edit automation" : "New automation"}
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} disabled={busy || !d.name || !d.prompt}>
            {busy ? "Checking…" : "Save"}
          </Button>
        </>
      }
    >
      <Input label="Name" value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} />
      <Select
        label="When should it run?"
        value={d.trigger}
        onChange={(e) => {
          const t = e.target.value as Draft["trigger"];
          setD({
            ...d,
            trigger: t,
            config:
              t === "schedule"
                ? { cron: "0 9 * * *", tz: Intl.DateTimeFormat().resolvedOptions().timeZone }
                : t === "watch"
                  ? { kind: kinds.watch[0]?.id ?? "", interval_min: "30" }
                  : {},
          });
        }}
        options={[
          { value: "schedule", label: "On a schedule" },
          { value: "watch", label: "When something changes (a release, a tag, a feed, a page)" },
          { value: "webhook", label: "When a web address is called (IFTTT, GitHub, a script)" },
        ]}
      />
      {d.trigger === "schedule" && (
        <>
          <Select
            label="Schedule"
            value={preset}
            onChange={(e) => e.target.value && cfg("cron", e.target.value)}
            options={PRESETS}
          />
          <Input
            label="Cron expression"
            hint={`minute hour day-of-month month day-of-week; at most every ${kinds.min_schedule_minutes} minutes`}
            value={d.config.cron ?? ""}
            onChange={(e) => cfg("cron", e.target.value)}
          />
          <Input
            label="Time zone"
            hint="For example Europe/London"
            value={d.config.tz ?? ""}
            onChange={(e) => cfg("tz", e.target.value)}
          />
        </>
      )}
      {d.trigger === "watch" && (
        <>
          <Select
            label="What to watch"
            value={d.config.kind ?? ""}
            onChange={(e) =>
              setD({
                ...d,
                config: { kind: e.target.value, interval_min: d.config.interval_min ?? "30" },
              })
            }
            options={kinds.watch.map((k) => ({ value: k.id, label: k.title }))}
          />
          {kind?.fields.map((f) => (
            <Input
              key={f.name}
              label={f.label + (f.required ? "" : " (optional)")}
              hint={f.hint}
              value={d.config[f.name] ?? ""}
              onChange={(e) => cfg(f.name, e.target.value)}
            />
          ))}
          <Input
            label="Check every (minutes)"
            hint={`at least ${kinds.min_watch_minutes}`}
            value={d.config.interval_min ?? "30"}
            onChange={(e) => cfg("interval_min", e.target.value)}
          />
          <p className="muted">
            Creating it remembers how things are now, so it only runs for what is new from here on.
          </p>
        </>
      )}
      {d.trigger === "webhook" && (
        <p className="muted">
          You get a secret web address when you save. Anything that can send a web request can use
          it; the body of the request is passed to the run as information, not as instructions.
        </p>
      )}
      <TextArea
        label="What should it do?"
        rows={4}
        value={d.prompt}
        onChange={(e) => setD({ ...d, prompt: e.target.value })}
        placeholder="For example: A new release is out. Update the dependency, run the tests and open a pull request."
      />
      <Select
        label="Model"
        value={d.model}
        onChange={(e) => setD({ ...d, model: e.target.value })}
        options={models.map((m) => ({ value: m.id, label: m.id }))}
      />
      <Select
        label="How much should it ask you?"
        value={d.autonomy}
        onChange={(e) => setD({ ...d, autonomy: e.target.value })}
        options={[
          { value: "ask", label: "Ask before running anything (waits for you)" },
          { value: "workspace", label: "Automatic in the workspace (risky actions wait for you)" },
          { value: "full", label: "Fully autonomous" },
        ]}
      />
      <RepoPicker
        onChange={(r) => setD({ ...d, repo: r && !r.create ? r : null })}
        allowCreate={false}
      />
      {hasIFTTT && (
        <Input
          label="Tell IFTTT when it finishes (optional)"
          hint="An IFTTT Webhooks event name; gets the name, the outcome and a short summary"
          value={d.notify.ifttt_event ?? ""}
          onChange={(e) => setD({ ...d, notify: { ifttt_event: e.target.value } })}
        />
      )}
      {d.id && (
        <Toggle
          label="Switched on"
          checked={d.enabled}
          onChange={(v) => setD({ ...d, enabled: v })}
        />
      )}
    </Modal>
  );
}

/** Things that start a run by themselves: on a schedule, from a web address, or when a watch finds something new. */
export function Automations({ onOpenSession }: { onOpenSession: (id: string) => void }) {
  const toast = useToast();
  const [list, setList] = useState<Automation[] | null>(null);
  const [kinds, setKinds] = useState<Kinds | null>(null);
  const [models, setModels] = useState<ModelOpt[]>([]);
  const [edit, setEdit] = useState<Draft | null>(null);
  const [hook, setHook] = useState("");
  const load = useCallback(() => {
    void get<Automation[]>("/automations").then(setList);
  }, []);
  useEffect(() => {
    load();
    void get<Kinds>("/automation-kinds").then(setKinds);
    void get<ModelOpt[]>("/models").then((m) =>
      setModels([...m].sort((a, b) => Number(b.recommended) - Number(a.recommended))),
    );
  }, [load]);
  if (!list || !kinds) return <Skeleton lines={3} />;
  const toDraft = (a: Automation): Draft => ({
    id: a.id,
    name: a.name,
    trigger: a.trigger,
    config: a.config,
    prompt: a.prompt,
    model: a.model,
    autonomy: a.autonomy,
    notify: a.notify ?? {},
    repo: a.repo
      ? {
          kind: a.repo.kind,
          full_name: a.repo.full_name,
          clone_url: a.repo.clone_url,
          default_branch: a.repo.default_branch,
        }
      : null,
    enabled: a.enabled,
  });
  return (
    <div className="stack" style={{ maxWidth: 820 }}>
      <div className="row">
        <h2 className="grow" style={{ margin: 0 }}>
          Automations
        </h2>
        <Button
          variant="primary"
          onClick={() => setEdit(empty(models[0]?.id ?? ""))}
          disabled={models.length === 0}
        >
          New automation
        </Button>
      </div>
      <p className="muted">
        An automation starts a session by itself, as you and within your limits: on a schedule, when
        a web address is called, or when something you watch changes. Each run appears in your
        session list.
      </p>
      {hook && (
        <Banner tone="warn">
          Web address for this automation (shown once; copy it now):
          <br />
          <code style={{ wordBreak: "break-all" }}>{hook}</code>
          <div>
            <Button onClick={() => void navigator.clipboard?.writeText(hook)}>Copy</Button>{" "}
            <Button variant="ghost" onClick={() => setHook("")}>
              I have copied it
            </Button>
          </div>
        </Banner>
      )}
      {list.length === 0 && (
        <EmptyState
          title="No automations yet"
          hint="For example: every night, update the dependencies and open a pull request."
        />
      )}
      {list.map((a) => (
        <Card key={a.id} title={a.name}>
          <div className="row">
            <Badge tone={a.enabled ? "success" : undefined}>{a.enabled ? "On" : "Off"}</Badge>
            <span className="muted grow">{describe(a)}</span>
            <Button
              onClick={async () => {
                try {
                  const r = await post<{ session: string }>(`/automations/${a.id}/run`);
                  toast("Started.", "success");
                  load();
                  onOpenSession(r.session);
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Run now
            </Button>
            <Button onClick={() => setEdit(toDraft(a))}>Edit</Button>
            {a.trigger === "webhook" && (
              <Button
                onClick={async () => {
                  const r = await post<{ webhook_url: string }>(`/automations/${a.id}/token`);
                  setHook(r.webhook_url);
                }}
              >
                New address
              </Button>
            )}
            <Button
              variant="danger"
              onClick={async () => {
                await del(`/automations/${a.id}`);
                load();
              }}
            >
              Delete
            </Button>
          </div>
          <p className="muted">
            {a.last_run_at && !a.last_run_at.startsWith("0001")
              ? `Last run ${ago(a.last_run_at)}. `
              : "Has not run yet. "}
            {a.next_run_at && !a.next_run_at.startsWith("0001")
              ? `Next check ${new Date(a.next_run_at).toLocaleString()}. `
              : ""}
            {a.last_status && `Status: ${a.last_status}.`}
          </p>
          {a.last_error && <Banner tone="warn">{a.last_error}</Banner>}
        </Card>
      ))}
      {edit && (
        <Editor
          initial={edit}
          kinds={kinds}
          models={models}
          onClose={() => setEdit(null)}
          onSaved={(h) => {
            setEdit(null);
            if (h) setHook(h);
            load();
          }}
        />
      )}
    </div>
  );
}
