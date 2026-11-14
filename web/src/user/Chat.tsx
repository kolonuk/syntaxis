// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { del, get, patch, post, put, errText } from "../lib/api";
import { streamEvents, type StreamEvent } from "../lib/sse";
import { enqueue, flush } from "../lib/queue";
import {
  Badge,
  Banner,
  Button,
  CodeBlock,
  Icon,
  Meter,
  Modal,
  StatusPill,
  Tabs,
  TextArea,
  Toggle,
  useToast,
  Input,
} from "../ui/ui";
import { copyText, splitFences } from "../lib/fences";

interface Msg {
  id: number;
  role: string;
  content: string;
  tool_calls?: string;
  tool_call_id?: string;
}
export interface Session {
  id: string;
  title: string;
  model: string;
  state: string;
  autonomy: string;
}
interface Approval {
  id: string;
  tool: string;
  summary: string;
  reason: string;
}
interface Ctx {
  used: number;
  window: number;
  percent: number;
}
interface Call {
  id: string;
  name: string;
  arguments: string;
}

/** Turns stored messages into display items: text, and tool steps paired with their results. */
export function buildItems(msgs: Msg[]) {
  const results = new Map<string, string>();
  for (const m of msgs)
    if (m.role === "tool" && m.tool_call_id) results.set(m.tool_call_id, m.content);
  const items: (
    | { kind: "user" | "assistant"; text: string; key: number }
    | { kind: "step"; call: Call; output?: string; key: string }
  )[] = [];
  for (const m of msgs) {
    if (m.role === "user") items.push({ kind: "user", text: m.content, key: m.id });
    else if (m.role === "assistant") {
      if (m.content.trim()) items.push({ kind: "assistant", text: m.content, key: m.id });
      if (m.tool_calls) {
        try {
          for (const c of JSON.parse(m.tool_calls) as Call[])
            items.push({
              kind: "step",
              call: c,
              output: results.get(c.id),
              key: `${m.id}-${c.id}`,
            });
        } catch {
          /* ignore malformed history */
        }
      }
    }
  }
  return items;
}

export function describeCall(c: Call): string {
  try {
    const a = JSON.parse(
      typeof c.arguments === "string" ? c.arguments : JSON.stringify(c.arguments),
    ) as Record<string, unknown>;
    if (typeof a.command === "string") return a.command;
    if (typeof a.path === "string") return `${c.name} ${a.path}`;
    if (typeof a.action === "string")
      return `git ${a.action}${typeof a.message === "string" ? ` — ${a.message}` : ""}`;
    if (typeof a.url === "string") return `fetch ${a.url}`;
    if (typeof a.pattern === "string") return `search ${a.pattern}`;
  } catch {
    /* fall through */
  }
  return c.name;
}

interface McpServer {
  name: string;
  enabled: boolean;
}

export function Chat({
  session,
  userKey,
  onChanged,
  onDeleted,
  panelOpen,
  setPanelOpen,
}: {
  session: Session;
  userKey: string;
  onChanged: () => void;
  onDeleted: () => void;
  panelOpen: boolean;
  setPanelOpen: (v: boolean) => void;
}) {
  const toast = useToast();
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [draft, setDraft] = useState("");
  const [live, setLive] = useState("");
  const [lines, setLines] = useState<string[]>([]);
  const [approval, setApproval] = useState<Approval | null>(null);
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [ctx, setCtx] = useState<Ctx | null>(null);
  const [panel, setPanel] = useState("output");
  const [running, setRunning] = useState(session.state === "running");
  const [notice, setNotice] = useState("");
  const [autonomy, setAutonomy] = useState(session.autonomy);
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(session.title);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [mcp, setMcp] = useState<McpServer[]>([]);
  const abort = useRef<AbortController | null>(null);

  const loadMessages = useCallback(async () => {
    setMsgs(await get<Msg[]>(`/sessions/${session.id}/messages`));
  }, [session.id]);

  const handle = useCallback(
    (e: StreamEvent) => {
      const d = e.data;
      switch (e.kind) {
        case "assistant_delta":
          setLive((x) => x + String(d.text ?? ""));
          break;
        case "assistant_message":
          setLive("");
          void loadMessages();
          break;
        case "tool_call":
          setLines((l) => [...l, `$ ${String(d.summary ?? d.tool)}`]);
          break;
        case "tool_result":
          setLines((l) => [...l, String(d.output ?? "").slice(0, 4000), ""]);
          void loadMessages();
          break;
        case "approval_request":
          setApproval({
            id: String(d.id),
            tool: String(d.tool),
            summary: String(d.summary),
            reason: String(d.reason ?? ""),
          });
          if ("setAppBadge" in navigator)
            void (
              navigator as Navigator & { setAppBadge: (n: number) => Promise<void> }
            ).setAppBadge(1);
          break;
        case "approval_result":
          setApproval(null);
          if ("clearAppBadge" in navigator)
            void (navigator as Navigator & { clearAppBadge: () => Promise<void> }).clearAppBadge();
          break;
        case "question":
          setQuestion(String(d.question ?? ""));
          break;
        case "context":
          setCtx({ used: Number(d.used), window: Number(d.window), percent: Number(d.percent) });
          break;
        case "phase":
          setPhase({
            name: String(d.phase ?? ""),
            detail: String(d.detail ?? ""),
            since: Date.now(),
          });
          break;
        case "notice":
          setNotice(String(d.message ?? ""));
          break;
        case "compaction":
          setNotice("Older turns were summarised to free up context. Your full history is kept.");
          break;
        case "status":
          setPhase(null);
          setRunning(false);
          onChanged();
          void loadMessages();
          break;
      }
    },
    [loadMessages, onChanged],
  );

  const attach = useCallback(() => {
    abort.current?.abort();
    const ac = new AbortController();
    abort.current = ac;
    streamEvents(session.id, handle, ac.signal).catch(() => undefined);
  }, [session.id, handle]);

  useEffect(() => {
    setMsgs([]);
    setLines([]);
    setLive("");
    setApproval(null);
    setNotice("");
    setAutonomy(session.autonomy);
    setTitle(session.title);
    void loadMessages();
    void get<McpServer[]>(`/sessions/${session.id}/mcp`)
      .then(setMcp)
      .catch(() => setMcp([]));
    attach();
    return () => abort.current?.abort();
  }, [session.id, session.autonomy, session.title, attach, loadMessages]);

  // Keep the log pinned to the bottom only while the reader is already there, and scroll
  // the log itself: scrollIntoView also moves the page, which made the chat jitter on phones.
  const [phase, setPhase] = useState<Phase | null>(null);
  const log = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const onLogScroll = () => {
    const el = log.current;
    if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  useEffect(() => {
    const el = log.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [msgs, live, lines]);

  useEffect(() => {
    const send = async () => {
      const n = await flush(userKey, async (q) => {
        await post(`/sessions/${q.sessionId}/messages`, { text: q.text });
      });
      if (n > 0) {
        toast(`Sent ${n} queued message${n > 1 ? "s" : ""}.`, "success");
        setRunning(true);
        attach();
      }
    };
    window.addEventListener("online", send);
    return () => window.removeEventListener("online", send);
  }, [userKey, attach, toast]);

  async function send() {
    const text = draft.trim();
    if (!text) return;
    setDraft("");
    setMsgs((m) => [...m, { id: Date.now(), role: "user", content: text }]);
    if (!navigator.onLine) {
      enqueue(userKey, { sessionId: session.id, text });
      toast("You are offline. The message will be sent when you reconnect.");
      return;
    }
    try {
      await post(`/sessions/${session.id}/messages`, { text });
      setRunning(true);
      setPhase({ name: "preparing", detail: "", since: Date.now() });
      setLines([]);
      attach();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function decide(ok: boolean) {
    if (!approval) return;
    try {
      await post(`/sessions/${session.id}/approvals/${encodeURIComponent(approval.id)}`, {
        approve: ok,
      });
      setApproval(null);
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function saveTitle() {
    try {
      await patch(`/sessions/${session.id}`, { title });
      setRenaming(false);
      onChanged();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function changeAutonomy(v: string) {
    setAutonomy(v);
    try {
      await patch(`/sessions/${session.id}`, { autonomy: v });
      onChanged();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function toggleMcp(name: string, enabled: boolean) {
    const next = mcp.map((m) => (m.name === name ? { ...m, enabled } : m));
    setMcp(next);
    await put(`/sessions/${session.id}/mcp`, {
      disabled: next.filter((m) => !m.enabled).map((m) => m.name),
    });
  }

  const items = useMemo(() => buildItems(msgs), [msgs]);

  return (
    <div className="layout" style={{ height: "100%" }}>
      <div className="main chat">
        <div className="chat-head">
          <h2 className="grow">{session.title}</h2>
          <StatusPill
            state={running ? "running" : session.state === "running" ? "idle" : session.state}
          />
          <Badge>{session.model}</Badge>
          <select
            className="select"
            aria-label="Autonomy"
            value={autonomy}
            onChange={(e) => void changeAutonomy(e.target.value)}
          >
            <option value="ask">Ask before running anything</option>
            <option value="workspace">Auto in workspace, ask for risky</option>
            <option value="full">Fully autonomous</option>
          </select>
          {running && <Button onClick={() => post(`/sessions/${session.id}/stop`)}>Stop</Button>}
          <Button variant="ghost" onClick={() => setRenaming(true)} aria-label="Rename session">
            Rename
          </Button>
          <a className="btn btn-ghost" href={`/api/sessions/${session.id}/export`}>
            Export
          </a>
          <Button variant="ghost" onClick={() => setConfirmDelete(true)}>
            Delete
          </Button>
          <Button
            className="sheet-toggle"
            onClick={() => setPanelOpen(!panelOpen)}
            aria-expanded={panelOpen}
          >
            Output &amp; context
          </Button>
        </div>
        {notice && (
          <div className="banner banner-info row" role="status">
            <span>{notice}</span>
            <Button
              variant="ghost"
              className="dismiss"
              onClick={() => setNotice("")}
              aria-label="Dismiss"
            >
              ✕
            </Button>
          </div>
        )}
        <div
          className="chat-log"
          ref={log}
          onScroll={onLogScroll}
          role="log"
          tabIndex={0}
          aria-label="Conversation"
        >
          {items.length === 0 && !live && (
            <p className="muted">
              Describe a task below. The agent works in its own sandbox and you can watch every
              command it runs.
            </p>
          )}
          {items.map((it, i) => {
            if (it.kind === "step") {
              const failed = !!it.output && /^exit code: [1-9]/.test(it.output);
              return (
                <details className={`step ${failed ? "failed" : ""}`} key={it.key}>
                  <summary>{describeCall(it.call)}</summary>
                  <CodeBlock code={it.output ?? "running…"} lang="output" />
                </details>
              );
            }
            const prev = items[i - 1];
            const showWho = !prev || prev.kind !== it.kind;
            return (
              <div className={`msg ${it.kind}`} key={it.key}>
                {showWho && <div className="who">{it.kind === "user" ? "You" : "Assistant"}</div>}
                <Body text={it.text} plain={it.kind === "user"} />
              </div>
            );
          })}
          {live && (
            <div className="msg assistant">
              <div className="who">Assistant</div>
              <Body text={live} />
            </div>
          )}
          {running && <Progress phase={phase} />}
        </div>
        <form
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <div className="grow">
            <TextArea
              label="Message"
              value={draft}
              placeholder="Describe a task, e.g. “add pagination to the API and run the tests”"
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void send();
              }}
            />
          </div>
          <Button variant="primary" type="submit" disabled={running}>
            Send
          </Button>
        </form>
      </div>
      <aside className={`side-panel ${panelOpen ? "open" : ""}`} aria-label="Session details">
        <div className="row">
          <div className="grow">
            <Tabs
              tabs={[
                { id: "output", label: "Output" },
                { id: "context", label: "Context" },
                { id: "tools", label: "Tools" },
              ]}
              value={panel}
              onChange={setPanel}
            />
          </div>
          <Button
            className="sheet-toggle"
            variant="ghost"
            onClick={() => setPanelOpen(false)}
            aria-label="Close panel"
          >
            ✕
          </Button>
        </div>
        {panel === "output" && <Output lines={lines} />}
        {panel === "context" && (
          <div className="stack">
            {ctx ? (
              <>
                <Meter label="Context window" used={ctx.used} limit={ctx.window} />
                {ctx.percent >= 75 && (
                  <Banner tone="warn">
                    The conversation is long. Older turns will be summarised automatically; nothing
                    is deleted.
                  </Banner>
                )}
              </>
            ) : (
              <p className="muted">The context meter appears once the agent starts working.</p>
            )}
          </div>
        )}
        {panel === "tools" && (
          <div className="stack">
            <p className="muted">
              Extra tools your admin approved. Turn one off to keep it out of this session.
            </p>
            {mcp.length === 0 ? (
              <p className="muted">No MCP servers are approved.</p>
            ) : (
              mcp.map((m) => (
                <Toggle
                  key={m.name}
                  label={m.name}
                  checked={m.enabled}
                  onChange={(v) => void toggleMcp(m.name, v)}
                />
              ))
            )}
          </div>
        )}
      </aside>
      {approval && (
        <Modal
          title="Approval needed"
          onClose={() => void decide(false)}
          actions={
            <>
              <Button onClick={() => void decide(false)}>Deny</Button>
              <Button variant="danger" onClick={() => void decide(true)}>
                Approve
              </Button>
            </>
          }
        >
          <p>The agent wants to run:</p>
          <pre>{approval.summary}</pre>
          <p className="muted">{approval.reason}</p>
          <p className="muted">
            A checkpoint of your files was taken first. Rolling back does not undo pushes or
            deploys.
          </p>
        </Modal>
      )}
      {question && (
        <Modal
          title="The agent has a question"
          onClose={() => setQuestion("")}
          actions={
            <Button
              variant="primary"
              onClick={async () => {
                try {
                  await post(`/sessions/${session.id}/answer`, { text: answer });
                  setQuestion("");
                  setAnswer("");
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Answer
            </Button>
          }
        >
          <p>{question}</p>
          <TextArea
            label="Your answer"
            value={answer}
            onChange={(e) => setAnswer(e.target.value)}
          />
        </Modal>
      )}
      {renaming && (
        <Modal
          title="Rename session"
          onClose={() => setRenaming(false)}
          actions={
            <>
              <Button onClick={() => setRenaming(false)}>Cancel</Button>
              <Button variant="primary" onClick={() => void saveTitle()}>
                Save
              </Button>
            </>
          }
        >
          <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} />
        </Modal>
      )}
      {confirmDelete && (
        <Modal
          title="Delete this session?"
          onClose={() => setConfirmDelete(false)}
          actions={
            <>
              <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  await del(`/sessions/${session.id}`);
                  setConfirmDelete(false);
                  onDeleted();
                }}
              >
                Delete
              </Button>
            </>
          }
        >
          <p>
            The conversation and its sandbox are removed. Your workspace files are kept until the
            retention period ends.
          </p>
        </Modal>
      )}
    </div>
  );
}

/** Command output as plain text: the server runs every command itself and shows what came back. */
interface Phase {
  name: string;
  detail: string;
  since: number;
}

const PHASE_TEXT: Record<string, string> = {
  preparing: "Getting your sandbox ready",
  queued: "Waiting for the model",
  connecting: "Connecting to the model",
  loading_model: "Loading the model into memory",
  reading: "Model is reading the conversation",
  thinking: "Model is thinking",
  replying: "Model is replying",
  tool: "Running",
  approval: "Waiting for your approval",
};

/** What the run is doing right now, with how long it has been doing it. */
function Progress({ phase }: { phase: Phase | null }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const label = phase ? (PHASE_TEXT[phase.name] ?? "Working") : "Working";
  const secs = phase ? Math.max(0, Math.round((now - phase.since) / 1000)) : 0;
  // Loading a large model can take minutes; say so once it has been a while.
  const slow = phase?.name === "loading_model" && secs > 20;
  return (
    <p className="progress" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      <span>
        {label}
        {phase?.detail ? `: ${phase.detail}` : ""}
        {phase && secs >= 3 ? ` · ${secs}s` : ""}
        {slow ? " (large models can take a minute or two)" : ""}
      </span>
    </p>
  );
}

/** Message text with fenced code shown as copyable blocks. */
function Body({ text, plain }: { text: string; plain?: boolean }) {
  if (plain) return <div className="body">{text}</div>;
  return (
    <div className="body">
      {splitFences(text).map((p, i) =>
        p.kind === "code" ? (
          <CodeBlock key={i} code={p.text} lang={p.lang} />
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </div>
  );
}

function Output({ lines }: { lines: string[] }) {
  const box = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const el = box.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);
  if (lines.length === 0)
    return <p className="muted">Commands the agent runs and their output appear here.</p>;
  return (
    <div>
      <button
        type="button"
        className="btn btn-ghost codeblock-copy"
        onClick={() => void copyText(lines.join("\n"))}
      >
        <Icon name="copy" />
        <span>Copy all</span>
      </button>
      <pre className="output" ref={box} role="log" aria-label="Command output" tabIndex={0}>
        {lines.join("\n")}
      </pre>
    </div>
  );
}
