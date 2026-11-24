// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { errText, get, post } from "../lib/api";
import { Badge, Banner, Button, Card, Modal, Select, Skeleton, useToast } from "../ui/ui";

export interface Release {
  tag: string;
  name: string;
  published: string;
  url: string;
  prerelease: boolean;
}
export interface StreamInfo {
  id: "stable" | "preview" | "edge";
  current: boolean;
  latest?: Release;
  newer?: boolean;
  running?: boolean;
  edge?: { sha: string; date: string; subject: string; newer: boolean };
}
export interface UpdateInfo {
  current: { version: string; commit: string; date: string };
  mode: "release" | "edge";
  stream: string;
  streams: StreamInfo[];
  latest?: Release; // the newest stable release
  release_available?: boolean;
  releases: Release[];
  check_error?: string;
  agent: { running: boolean; manual: string };
  status: { state?: string; target?: string; stream?: string; message?: string; at?: string };
}

export const STREAMS: Record<string, { label: string; what: string }> = {
  stable: {
    label: "Stable",
    what: "Published releases that are not pre-releases. The most tested; the right choice for an instance other people depend on.",
  },
  preview: {
    label: "Preview",
    what: "Also takes pre-releases (release candidates and betas) as soon as they are published, so changes arrive sooner and you help find problems. When a final release catches up, preview moves to it.",
  },
  edge: {
    label: "Edge",
    what: "Builds the newest code on the main branch on this host. It has the latest changes and the newest mistakes; it is not what releases are tested as.",
  },
};

interface Confirm {
  target: string;
  stream?: string;
  label: string;
  warning?: string;
}

/**
 * Updates: which release stream this instance follows (stable, preview or edge), what each would
 * install now, and a way to pick one exact version. The update itself runs on the Docker host.
 */
export function UpdatesTab() {
  const toast = useToast();
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [lost, setLost] = useState(false);
  const [pick, setPick] = useState("");
  const [confirm, setConfirm] = useState<Confirm | null>(null);
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
      await post("/update", {
        target: confirm.target,
        ...(confirm.stream ? { stream: confirm.stream } : {}),
      });
      toast("Update requested.", "success");
    } catch (x) {
      toast(errText(x), "error");
    }
    setConfirm(null);
    void load();
  }
  if (!info) return lost ? <Banner tone="warn">Reconnecting…</Banner> : <Skeleton />;
  const cur = info.current;
  const canStart = !busy && info.agent.running;
  const streamLabel = STREAMS[info.stream]?.label ?? info.stream;

  function streamAction(s: StreamInfo): { label: string; disabled: boolean; confirm?: Confirm } {
    const name = STREAMS[s.id]!.label;
    if (s.id === "edge") {
      if (s.current)
        return s.edge?.newer
          ? {
              label: "Update to the newest commit",
              disabled: !canStart,
              confirm: { target: "edge", label: "Update to the newest commit" },
            }
          : { label: "Up to date", disabled: true };
      return {
        label: "Switch to Edge",
        disabled: !canStart,
        confirm: { target: "edge", label: "Follow the development branch" },
      };
    }
    if (!s.latest) return { label: "No release yet", disabled: true };
    const tag = s.latest.tag;
    if (s.current && s.running) return { label: "Up to date", disabled: true };
    const older = !s.newer && !s.running && info!.mode === "release";
    const warning = older
      ? `${tag} is older than ${cur.version}, which is running now. The database is backed up first, but a newer version may have changed it in ways an older one cannot open; if the update fails it is rolled back.`
      : undefined;
    if (s.current)
      return {
        label: `Update to ${tag}`,
        disabled: !canStart,
        confirm: { target: s.id, label: `Update to ${tag}` },
      };
    return {
      label: `Switch to ${name} (${tag})`,
      disabled: !canStart,
      confirm: { target: s.id, label: `Switch to the ${name} stream: ${tag}`, warning },
    };
  }

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
          Updated to {info.status.target}
          {info.status.stream
            ? ` (${STREAMS[info.status.stream]?.label ?? info.status.stream} stream)`
            : ""}
          . {info.status.message}
        </Banner>
      )}
      {!info.agent.running && (
        <Banner tone="warn">
          <p>
            The update service on the Docker host is not running, so updates cannot be started from
            here. Start it with <code>sudo systemctl start syntaxis-update-agent</code>, or update
            by hand with <code>{info.agent.manual}</code>.
          </p>
          <p>
            If Syntaxis is deployed by another tool, such as a compose stack you manage yourself,
            update it there: change the image versions and redeploy. This page still shows what is
            available.
          </p>
        </Banner>
      )}
      <Card title="Running now">
        <p>
          <strong>{cur.version}</strong> {cur.commit && <code>{cur.commit.slice(0, 8)}</code>}{" "}
          <Badge>{info.mode === "release" ? "published release" : "development branch"}</Badge>{" "}
          <Badge tone="info">following {streamLabel}</Badge>
        </p>
        {cur.date && <p className="muted">Built {new Date(cur.date).toLocaleString()}</p>}
      </Card>

      <Card title="Release stream">
        <p className="muted">
          A stream is how soon you want changes. The weekly automatic update, if you installed it,
          stays inside your stream and never goes backwards. You can switch any time; the database
          is backed up first and a failed update is rolled back.
        </p>
        {info.check_error && <Banner tone="warn">{info.check_error}</Banner>}
        {info.streams.map((s) => {
          const a = streamAction(s);
          const meta = STREAMS[s.id]!;
          return (
            <div key={s.id} className="stream">
              <div className="row">
                <h3 className="grow">
                  {meta.label} {s.current && <Badge tone="info">following</Badge>}
                </h3>
                <Button
                  variant={s.current ? "primary" : "secondary"}
                  disabled={a.disabled}
                  onClick={() => a.confirm && setConfirm(a.confirm)}
                >
                  {a.label}
                </Button>
              </div>
              <p className="muted">{meta.what}</p>
              {s.id !== "edge" && s.latest && (
                <p>
                  Newest: <strong>{s.latest.tag}</strong>{" "}
                  <span className="muted">
                    {new Date(s.latest.published).toLocaleDateString()} ·{" "}
                    <a href={s.latest.url} target="_blank" rel="noreferrer">
                      release notes
                    </a>
                  </span>{" "}
                  {s.running ? (
                    <Badge>running</Badge>
                  ) : s.newer ? (
                    <Badge tone="warn">newer than what runs</Badge>
                  ) : null}
                </p>
              )}
              {s.id !== "edge" && !s.latest && !info.check_error && (
                <p className="muted">No release has been published in this stream yet.</p>
              )}
              {s.id === "edge" && s.edge && (
                <p>
                  Newest commit: <code>{s.edge.sha.slice(0, 8)}</code> {s.edge.subject}{" "}
                  {s.edge.newer ? (
                    <Badge tone="warn">different from what is running</Badge>
                  ) : (
                    <Badge>running</Badge>
                  )}
                </p>
              )}
            </div>
          );
        })}
      </Card>

      {info.releases.length > 0 && (
        <Card title="A specific version">
          <p className="muted">
            Pin an exact release. A pre-release puts you on the Preview stream; a stable one on
            Stable.
          </p>
          <div className="row">
            <Select
              label="Version"
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
              disabled={!pick || !canStart || pick === cur.version}
              onClick={() => setConfirm({ target: pick, label: `Switch to ${pick}` })}
            >
              Switch
            </Button>
          </div>
        </Card>
      )}
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
            The database is backed up first, the new version is started and checked, and if it does
            not come up healthy everything is put back. Running sessions are stopped while it
            restarts.
          </p>
          {confirm.warning && <Banner tone="warn">{confirm.warning}</Banner>}
        </Modal>
      )}
    </div>
  );
}
