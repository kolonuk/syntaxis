// SPDX-License-Identifier: GPL-2.0-or-later
import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { ApiError, get, post, del, errText } from "../lib/api";
import { copyText } from "../lib/fences";
import {
  Badge,
  Banner,
  Button,
  Card,
  CodeBlock,
  Input,
  Modal,
  Select,
  Skeleton,
  Toggle,
  useToast,
} from "../ui/ui";

const Markdown = lazy(() => import("../ui/Markdown"));

interface SharedView {
  title: string;
  model: string;
  state: string;
  expires_at: string;
  messages: { role: string; content: string }[];
}

/** The public, read-only page behind a shared link. Nothing here can change the session. */
export function SharedPage({ token }: { token: string }) {
  const [v, setV] = useState<SharedView | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () =>
      get<SharedView>(`/shared/${encodeURIComponent(token)}`)
        .then((x) => {
          if (!live) return;
          setV(x);
          // follow a session that is still working
          if (x.state === "running") timer = setTimeout(load, 4000);
        })
        .catch((e) => {
          if (live)
            setErr(
              e instanceof ApiError && e.status === 404
                ? "This link does not exist or has expired."
                : errText(e),
            );
        });
    load();
    return () => {
      live = false;
      if (timer) clearTimeout(timer);
    };
  }, [token]);
  return (
    <div className="shared">
      <header className="topbar">
        <h1 className="brand">Syntaxis</h1>
        <span className="muted">Shared read-only</span>
      </header>
      <main className="main" style={{ maxWidth: 860, margin: "0 auto" }}>
        {err && <Banner tone="warn">{err}</Banner>}
        {!v && !err && <Skeleton lines={4} />}
        {v && (
          <div className="stack">
            <div className="row">
              <h2 className="grow" style={{ margin: 0 }}>
                {v.title}
              </h2>
              <Badge>{v.model}</Badge>
              {v.state === "running" && <Badge tone="info">working</Badge>}
            </div>
            <p className="muted">
              A read-only copy of this session. The link expires{" "}
              {new Date(v.expires_at).toLocaleString()}.
            </p>
            {v.messages.map((m, i) =>
              m.role === "tool" ? (
                <details key={i} className="step">
                  <summary>Command output</summary>
                  <CodeBlock code={m.content} lang="output" />
                </details>
              ) : (
                <div key={i} className={`msg ${m.role}`}>
                  <div className="who">{m.role === "user" ? "User" : "Assistant"}</div>
                  <div className="body md">
                    {m.role === "assistant" ? (
                      <Suspense fallback={<span className="muted">{m.content}</span>}>
                        <Markdown text={m.content} />
                      </Suspense>
                    ) : (
                      m.content
                    )}
                  </div>
                </div>
              ),
            )}
          </div>
        )}
      </main>
    </div>
  );
}

interface Share {
  id: string;
  session_id: string;
  session_title: string;
  name: string;
  url?: string;
  expires_at: string;
  revoked: boolean;
  active: boolean;
  views: number;
  include_tools: boolean;
}

const HOURS = [
  { value: "1", label: "1 hour" },
  { value: "6", label: "6 hours" },
  { value: "24", label: "1 day" },
  { value: "72", label: "3 days" },
  { value: "168", label: "1 week" },
  { value: "720", label: "30 days" },
];

/** Make a read-only link to a session. */
export function ShareDialog({
  sessionId,
  title,
  onClose,
}: {
  sessionId: string;
  title: string;
  onClose: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(title);
  const [hours, setHours] = useState("24");
  const [tools, setTools] = useState(false);
  const [url, setUrl] = useState("");
  return (
    <Modal
      title="Share this session"
      onClose={onClose}
      actions={
        url ? (
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button
              variant="primary"
              onClick={async () => {
                try {
                  const r = await post<Share>(`/sessions/${sessionId}/shares`, {
                    name,
                    hours: Number(hours),
                    include_tools: tools,
                  });
                  setUrl(r.url ?? "");
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Create link
            </Button>
          </>
        )
      }
    >
      {url ? (
        <>
          <p>
            Anyone with this link can read the session until it expires. You can revoke it any time
            in Settings, Shared links.
          </p>
          <Input label="Link" readOnly value={url} onFocus={(e) => e.target.select()} />
          <Button
            onClick={async () => {
              if (await copyText(url)) toast("Copied.", "success");
            }}
          >
            Copy
          </Button>
        </>
      ) : (
        <>
          <p className="muted">
            Makes a public, read-only link. People with it see the conversation but cannot change
            anything. Your stored secrets are removed from what they see.
          </p>
          <Input
            label="Name, to find it again"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Select
            label="Expires after"
            value={hours}
            onChange={(e) => setHours(e.target.value)}
            options={HOURS}
          />
          <Toggle
            label="Include command output (it may show private project contents)"
            checked={tools}
            onChange={setTools}
          />
        </>
      )}
    </Modal>
  );
}

/** Settings: all the links you have made, with copy and revoke. */
export function SharedLinks() {
  const toast = useToast();
  const [list, setList] = useState<Share[] | null>(null);
  const load = useCallback(() => {
    void get<Share[]>("/shares").then(setList);
  }, []);
  useEffect(load, [load]);
  if (!list) return <Skeleton lines={2} />;
  if (list.length === 0)
    return <p className="muted">You have not shared any sessions. Use Share in a session.</p>;
  return (
    <div className="stack">
      {list.map((s) => (
        <Card key={s.id} title={s.name}>
          <p className="muted">
            Session: {s.session_title} · {s.views} view(s) ·{" "}
            {s.include_tools ? "with command output" : "conversation only"}
          </p>
          <div className="row">
            {s.active ? (
              <Badge tone="success">Active until {new Date(s.expires_at).toLocaleString()}</Badge>
            ) : (
              <Badge>{s.revoked ? "Revoked" : "Expired"}</Badge>
            )}
            <span className="grow" />
            {s.active && s.url && (
              <Button
                onClick={async () => {
                  if (await copyText(s.url ?? "")) toast("Link copied.", "success");
                }}
              >
                Copy link
              </Button>
            )}
            {s.active && (
              <Button
                variant="danger"
                onClick={async () => {
                  await del(`/shares/${s.id}`);
                  load();
                }}
              >
                Revoke
              </Button>
            )}
          </div>
        </Card>
      ))}
    </div>
  );
}
