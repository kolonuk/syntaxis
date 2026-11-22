// SPDX-License-Identifier: GPL-2.0-or-later
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { del, get, patch, post, put, upload, errText } from "../lib/api";
import { streamEvents, type StreamEvent } from "../lib/sse";
import { enqueue, flush } from "../lib/queue";
import {
  Badge,
  Banner,
  Button,
  Card,
  CodeBlock,
  Icon,
  Meter,
  Modal,
  Skeleton,
  StatusPill,
  Tabs,
  TextArea,
  Toggle,
  useToast,
  Input,
} from "../ui/ui";
import { copyText } from "../lib/fences";
import { commentsMessage, parseDiff } from "../lib/diff";
import { ago } from "../lib/format";
import { ShareDialog } from "./Shared";
import { Files } from "./Files";
import { RepoHooks } from "./RepoHooks";
import type { Uploads } from "./hooks";
import { TodoList } from "./Todos";
import {
  attachmentName,
  fileSize,
  MAX_ATTACHMENTS,
  MAX_UPLOAD,
  acceptFor,
  parseAttachments,
  type Attachment,
} from "../lib/attachments";
import { latestTodos, todosFromArgs, type Todo } from "../lib/todos";
import { matchCommands, type SlashCommand } from "../lib/commands";

interface Msg {
  id: number;
  role: string;
  content: string;
  tool_calls?: string;
  tool_call_id?: string;
}
export interface Session {
  plan_mode?: boolean;
  plan_state?: string;
  plan_text?: string;
  repo?: string;
  repo_branch?: string;
  environment?: { image: string; note: string };
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
    | { kind: "user" | "assistant"; text: string; key: number; calls?: boolean }
    | { kind: "step"; call: Call; output?: string; key: string }
  )[] = [];
  for (const m of msgs) {
    if (m.role === "user") items.push({ kind: "user", text: m.content, key: m.id });
    else if (m.role === "assistant") {
      if (m.content.trim())
        items.push({ kind: "assistant", text: m.content, key: m.id, calls: !!m.tool_calls });
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
    if (typeof a.query === "string") return `web search: ${a.query}`;
    if (typeof a.note === "string") return `remember: ${a.note}`;
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
  onOpen,
  panelOpen,
  setPanelOpen,
  uploads,
}: {
  uploads?: Uploads;
  session: Session;
  userKey: string;
  onChanged: () => void;
  onDeleted: () => void;
  /** Called with the id of a session that was just created from this one (a branch). */
  onOpen?: (id: string) => void;
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
  const [phase, setPhase] = useState<Phase | null>(null);
  const [planDraft, setPlanDraft] = useState(session.plan_text ?? "");
  const [planFeedback, setPlanFeedback] = useState("");
  const [sharing, setSharing] = useState(false);
  const [answer, setAnswer] = useState("");
  const [ctx, setCtx] = useState<Ctx | null>(null);
  const [panel, setPanel] = useState("output");
  const [view, setView] = useState<"chat" | "files" | "terminal">("chat");
  const [termOpened, setTermOpened] = useState(false);
  const [filesOpened, setFilesOpened] = useState(false);
  const [attached, setAttached] = useState<Attachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [commands, setCommands] = useState<SlashCommand[]>([]);
  const [cmdIndex, setCmdIndex] = useState(0);
  const [liveTodos, setLiveTodos] = useState<Todo[] | null>(null);
  const [editing, setEditing] = useState<Msg | null>(null);
  const [editText, setEditText] = useState("");
  const picker = useRef<HTMLInputElement>(null);
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
        case "plan":
          setPlanDraft(String(d.plan ?? ""));
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
        case "todos":
          setLiveTodos(todosFromArgs({ todos: d.todos }) ?? []);
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
    setLiveTodos(null);
    setAttached([]);
    setAutonomy(session.autonomy);
    setTitle(session.title);
    void loadMessages();
    void get<McpServer[]>(`/sessions/${session.id}/mcp`)
      .then(setMcp)
      .catch(() => setMcp([]));
    attach();
    return () => abort.current?.abort();
  }, [session.id, session.autonomy, session.title, attach, loadMessages]);

  // slash commands: refreshed whenever a new one is started, since a repository's can change
  const loadCommands = useCallback(async () => {
    try {
      setCommands(await get<SlashCommand[]>(`/sessions/${session.id}/commands`));
    } catch {
      setCommands([]);
    }
  }, [session.id]);
  useEffect(() => {
    void loadCommands();
  }, [loadCommands]);
  const slash = useMemo(() => matchCommands(draft, commands), [draft, commands]);
  useEffect(() => setCmdIndex(0), [draft]);
  useEffect(() => {
    if (draft === "/") void loadCommands();
  }, [draft, loadCommands]);

  // Keep the log pinned to the bottom only while the reader is already there, and scroll
  // the log itself: scrollIntoView also moves the page, which made the chat jitter on phones.
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

  async function addFiles(files: FileList | File[] | null) {
    const list = files ? Array.from(files) : [];
    if (list.length === 0) return;
    if (!uploads?.enabled) {
      toast("File uploads are switched off by the administrator.", "error");
      return;
    }
    if (!navigator.onLine) {
      toast("Attaching files needs a connection.", "error");
      return;
    }
    setUploading(true);
    try {
      for (const f of list) {
        if (attached.length >= MAX_ATTACHMENTS) {
          toast(`Attach at most ${MAX_ATTACHMENTS} files to one message.`, "error");
          break;
        }
        const maxUpload = uploads.max_bytes || MAX_UPLOAD;
        if (f.size > maxUpload) {
          toast(`${f.name} is larger than ${Math.round(maxUpload / 1024 / 1024)} MB.`, "error");
          continue;
        }
        const name = f.name || `pasted-${Date.now()}.png`;
        const r = await upload<{ path: string; size: number }>(
          `/sessions/${session.id}/files/upload?dir=${encodeURIComponent("/workspace/.attachments")}&name=${encodeURIComponent(name)}`,
          f,
        );
        const kind = /\.pdf$/i.test(name)
          ? "pdf"
          : /\.(png|jpe?g|gif|webp|bmp|tiff?)$/i.test(name)
            ? "image"
            : "text";
        setAttached((a) =>
          a.some((x) => x.path === r.path) ? a : [...a, { path: r.path, kind, size: r.size }],
        );
      }
      onChanged();
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setUploading(false);
      if (picker.current) picker.current.value = "";
    }
  }

  async function send() {
    const text = draft.trim();
    if (!text && attached.length === 0) return;
    if (attached.length > 0 && !navigator.onLine) {
      toast("Attachments cannot be sent while you are offline.", "error");
      return;
    }
    const sendFiles = attached;
    setDraft("");
    setAttached([]);
    setMsgs((m) => [...m, { id: Date.now(), role: "user", content: text }]);
    if (!navigator.onLine) {
      enqueue(userKey, { sessionId: session.id, text });
      toast("You are offline. The message will be sent when you reconnect.");
      return;
    }
    try {
      await post(`/sessions/${session.id}/messages`, {
        text,
        attachments: sendFiles.map((f) => f.path),
      });
      setRunning(true);
      setPhase({ name: "preparing", detail: "", since: Date.now() });
      setLines([]);
      attach();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  function started() {
    setRunning(true);
    setPhase({ name: "preparing", detail: "", since: Date.now() });
    setLines([]);
    setLiveTodos(null);
    attach();
  }

  async function sendEdit() {
    if (!editing) return;
    try {
      await post(`/sessions/${session.id}/edit`, { message_id: editing.id, text: editText });
      setEditing(null);
      await loadMessages();
      started();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function regenerate(assistantId: number) {
    // the user message this reply answers
    const idx = msgs.findIndex((m) => m.id === assistantId);
    for (let i = idx - 1; i >= 0; i--) {
      const m = msgs[i]!;
      if (m.role === "user") {
        try {
          await post(`/sessions/${session.id}/edit`, {
            message_id: m.id,
            text: parseAttachments(m.content).text,
          });
          await loadMessages();
          started();
        } catch (x) {
          toast(errText(x), "error");
        }
        return;
      }
    }
  }

  async function branch(messageId: number) {
    try {
      const s = await post<{ id: string }>(`/sessions/${session.id}/fork`, {
        message_id: messageId,
      });
      toast("A new session was started from this reply.", "success");
      onChanged();
      onOpen?.(s.id);
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function decidePlan(action: "approve" | "revise") {
    try {
      await post(`/sessions/${session.id}/plan`, {
        action,
        plan: planDraft,
        feedback: planFeedback,
      });
      setPlanFeedback("");
      setRunning(true);
      setPhase({ name: "preparing", detail: "", since: Date.now() });
      setLines([]);
      attach();
      onChanged();
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
  const todos = useMemo(() => liveTodos ?? latestTodos(msgs), [liveTodos, msgs]);
  const lastAssistant = useMemo(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i]!;
      if (it.kind === "assistant" && !it.calls) return it.key;
      if (it.kind === "user") return null;
    }
    return null;
  }, [items]);

  return (
    <div className="layout" style={{ height: "100%" }}>
      <div className="main chat">
        <div className="chat-head">
          <div className="chat-title">
            <h2 title={session.title}>{session.title}</h2>
            <StatusPill
              state={running ? "running" : session.state === "running" ? "idle" : session.state}
            />
            <Badge>{session.model}</Badge>
            {session.repo && (
              <Badge>
                {session.repo} · {session.repo_branch}
              </Badge>
            )}
            {session.environment?.note && (
              <span className="muted" title={session.environment.image || "default image"}>
                {session.environment.note}
              </span>
            )}
          </div>
          <div className="chat-actions">
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
            <Button variant="ghost" onClick={() => setSharing(true)}>
              Share
            </Button>
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
        <Tabs
          tabs={[
            { id: "chat", label: "Chat" },
            { id: "files", label: "Files" },
            { id: "terminal", label: "Terminal" },
          ]}
          value={view}
          onChange={(v) => {
            setView(v as "chat" | "files" | "terminal");
            if (v === "terminal") setTermOpened(true);
            if (v === "files") setFilesOpened(true);
          }}
        />
        <div className="chat-body" hidden={view !== "chat"}>
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
              if (it.kind === "step" && it.call.name === "todo_write") return null;
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
              const parsed = it.kind === "user" ? parseAttachments(it.text) : null;
              return (
                <div className={`msg ${it.kind}`} key={it.key}>
                  {showWho && <div className="who">{it.kind === "user" ? "You" : "Assistant"}</div>}
                  <Body text={parsed ? parsed.text : it.text} plain={it.kind === "user"} />
                  {parsed && parsed.items.length > 0 && (
                    <div className="chips" aria-label="Attached files">
                      {parsed.items.map((a) => (
                        <span className="chip" key={a.path} title={a.path}>
                          <span className="chip-name">{attachmentName(a.path)}</span>
                          <span className="muted">{fileSize(a.size)}</span>
                        </span>
                      ))}
                    </div>
                  )}
                  {!running && it.kind === "user" && it.key < 1e12 && (
                    <div className="msg-actions">
                      <Button
                        variant="ghost"
                        onClick={() => {
                          setEditing({ id: it.key, role: "user", content: it.text });
                          setEditText(parseAttachments(it.text).text);
                        }}
                      >
                        Edit and resend
                      </Button>
                    </div>
                  )}
                  {!running && it.kind === "assistant" && !it.calls && (
                    <div className="msg-actions">
                      {it.key === lastAssistant && (
                        <Button variant="ghost" onClick={() => void regenerate(it.key)}>
                          Regenerate
                        </Button>
                      )}
                      <Button variant="ghost" onClick={() => void branch(it.key)}>
                        Branch from here
                      </Button>
                    </div>
                  )}
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
          {session.plan_state === "proposed" && !running && (
            <Card title="The agent's plan">
              <p className="muted">
                Nothing has been changed yet. Edit the plan if you like, then approve it, or ask for
                changes.
              </p>
              <TextArea
                label="Plan"
                rows={8}
                value={planDraft}
                onChange={(e) => setPlanDraft(e.target.value)}
              />
              <div className="row">
                <Button variant="primary" onClick={() => void decidePlan("approve")}>
                  Approve and start
                </Button>
                <Input
                  label="Or ask for changes"
                  value={planFeedback}
                  onChange={(e) => setPlanFeedback(e.target.value)}
                  placeholder="What should change?"
                />
                <Button onClick={() => void decidePlan("revise")} disabled={!planFeedback.trim()}>
                  Revise
                </Button>
              </div>
            </Card>
          )}
          <TodoList todos={todos} />
          <RepoHooks sessionId={session.id} running={running} />
          <div className="composer-wrap">
            {attached.length > 0 && (
              <div className="chips" aria-label="Files to attach">
                {attached.map((a) => (
                  <span className="chip" key={a.path} title={a.path}>
                    <span className="chip-name">{attachmentName(a.path)}</span>
                    <span className="muted">{fileSize(a.size)}</span>
                    <button
                      type="button"
                      aria-label={`Remove ${attachmentName(a.path)}`}
                      onClick={() => setAttached((x) => x.filter((y) => y.path !== a.path))}
                    >
                      ✕
                    </button>
                  </span>
                ))}
              </div>
            )}
            {slash.length > 0 && (
              <ul className="slash-menu" role="listbox" aria-label="Commands">
                {slash.map((c, i) => (
                  <li key={c.name} role="presentation">
                    <button
                      type="button"
                      role="option"
                      aria-selected={i === cmdIndex}
                      onClick={() => setDraft(`/${c.name} `)}
                    >
                      <strong>/{c.name}</strong>
                      <span className="muted">{c.description}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <form
              className="composer"
              onSubmit={(e) => {
                e.preventDefault();
                void send();
              }}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                if (e.dataTransfer.files.length > 0) {
                  e.preventDefault();
                  void addFiles(e.dataTransfer.files);
                }
              }}
            >
              {uploads?.enabled && (
                <>
                  <input
                    ref={picker}
                    type="file"
                    multiple
                    hidden
                    accept={acceptFor(uploads.kinds)}
                    aria-label="Choose files to attach"
                    onChange={(e) => void addFiles(e.target.files)}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    aria-label="Attach files"
                    title="Attach files, pictures or PDFs"
                    loading={uploading}
                    disabled={running}
                    onClick={() => picker.current?.click()}
                  >
                    Attach
                  </Button>
                </>
              )}
              <div className="grow">
                <TextArea
                  label="Message"
                  value={draft}
                  placeholder="Describe a task, or type / for commands"
                  onChange={(e) => setDraft(e.target.value)}
                  onPaste={(e) => {
                    const files = Array.from(e.clipboardData.files);
                    if (files.length > 0) {
                      e.preventDefault();
                      void addFiles(files);
                    }
                  }}
                  onKeyDown={(e) => {
                    if (slash.length > 0) {
                      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                        e.preventDefault();
                        const n = slash.length;
                        setCmdIndex((i) => (i + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
                        return;
                      }
                      if (e.key === "Tab" || (e.key === "Enter" && !e.metaKey && !e.ctrlKey)) {
                        e.preventDefault();
                        setDraft(`/${slash[cmdIndex]!.name} `);
                        return;
                      }
                    }
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void send();
                  }}
                />
              </div>
              <Button variant="primary" type="submit" disabled={running || uploading}>
                Send
              </Button>
            </form>
          </div>
        </div>
        {filesOpened && (
          <div hidden={view !== "files"} className="chat-body">
            <Files sessionId={session.id} running={running} uploads={uploads} />
          </div>
        )}
        {termOpened && (
          <Suspense fallback={<p className="muted">Loading the terminal…</p>}>
            <TerminalView sessionId={session.id} visible={view === "terminal"} blocked={running} />
          </Suspense>
        )}
      </div>
      <aside className={`side-panel ${panelOpen ? "open" : ""}`} aria-label="Session details">
        <div className="row">
          <div className="grow">
            <Tabs
              tabs={[
                { id: "output", label: "Output" },
                { id: "context", label: "Context" },
                { id: "changes", label: "Changes" },
                { id: "checkpoints", label: "Checkpoints" },
                { id: "resources", label: "Resources" },
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
        {panel === "changes" && (
          <Changes
            sessionId={session.id}
            disabled={running}
            onSend={(text) => {
              setDraft(text);
              setPanelOpen(false);
              toast("The comments are in the message box. Press Send when you are ready.");
            }}
          />
        )}
        {panel === "checkpoints" && (
          <Checkpoints
            sessionId={session.id}
            disabled={running}
            onRestored={() => void loadMessages()}
          />
        )}
        {panel === "resources" && <Resources sessionId={session.id} />}
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
      {editing && (
        <Modal
          title="Edit and resend"
          onClose={() => setEditing(null)}
          actions={
            <>
              <Button onClick={() => setEditing(null)}>Cancel</Button>
              <Button variant="primary" onClick={() => void sendEdit()} disabled={!editText.trim()}>
                Resend
              </Button>
            </>
          }
        >
          <TextArea
            label="Your message"
            rows={6}
            value={editText}
            onChange={(e) => setEditText(e.target.value)}
          />
          <p className="muted">
            This message and everything after it are set aside (they stay in the exported history),
            and the agent starts again from here. Files the agent already changed stay as they are;
            use the Checkpoints tab to put them back.
          </p>
        </Modal>
      )}
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
      {sharing && (
        <ShareDialog
          sessionId={session.id}
          title={session.title}
          onClose={() => setSharing(false)}
        />
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
  const [now, setNow] = useState(() => Date.now());
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

interface Res {
  active: boolean;
  cpu_percent?: number;
  cpu_typical?: number;
  cpu_busiest?: number;
  cpu_cores?: number;
  cpu_cores_normal?: number;
  memory_bytes?: number;
  memory_limit?: number;
  throttled?: boolean;
  paused?: boolean;
  cpu_seconds?: number;
  disk_bytes?: number;
  disk_limit?: number;
  hook_seconds?: number;
}

const hookNote = (r: Res) =>
  r.hook_seconds ? (
    <p className="muted">
      Hooks have run for {Math.round(r.hook_seconds)} seconds in this session. Together they get 5
      minutes in each run, then stop until the next message.
    </p>
  ) : null;

const pct = (n: number | undefined, cores: number) =>
  Math.min(100, Math.round(((n ?? 0) / (cores * 100)) * 100));

interface DiffResp {
  available: boolean;
  reason?: string;
  text?: string;
  base?: string;
}

/** What the agent has changed, file by file, with comments that go back to the agent. */
function Changes({
  sessionId,
  disabled,
  onSend,
}: {
  sessionId: string;
  disabled: boolean;
  onSend: (text: string) => void;
}) {
  const [d, setD] = useState<DiffResp | null>(null);
  const [err, setErr] = useState("");
  const [comments, setComments] = useState<Record<string, string>>({});
  const load = useCallback(() => {
    setErr("");
    get<DiffResp>(`/sessions/${sessionId}/diff`)
      .then(setD)
      .catch((e) => setErr(e instanceof Error ? e.message : "Could not load the changes"));
  }, [sessionId]);
  useEffect(load, [load]);
  if (err) return <Banner tone="warn">{err}</Banner>;
  if (!d) return <Skeleton lines={3} />;
  if (!d.available) return <p className="muted">{d.reason}</p>;
  const p = parseDiff(d.text ?? "");
  const msg = commentsMessage(comments);
  return (
    <div className="stack">
      <div className="row">
        <span className="grow muted">
          {p.files.length} file(s) changed{d.base ? ` compared with ${d.base}` : ""}
          {p.branch ? ` on ${p.branch}` : ""}
        </span>
        <Button onClick={load}>Refresh</Button>
      </div>
      {p.files.length === 0 && <p className="muted">No changes yet.</p>}
      {p.files.map((f) => (
        <details key={f.name} className="diff-file">
          <summary>
            <code>{f.name}</code> <span className="diff-add">+{f.added}</span>{" "}
            <span className="diff-del">-{f.removed}</span>
            {comments[f.name]?.trim() ? " · commented" : ""}
          </summary>
          <Suspense fallback={<Skeleton lines={2} />}>
            <DiffView text={f.raw} />
          </Suspense>
          <Input
            label={`Comment on ${f.name}`}
            value={comments[f.name] ?? ""}
            onChange={(e) => setComments({ ...comments, [f.name]: e.target.value })}
            placeholder="What should change?"
          />
        </details>
      ))}
      {p.status.some((s) => s.startsWith("??")) && (
        <p className="muted">
          New files (not in the diff above):{" "}
          {p.status
            .filter((s) => s.startsWith("??"))
            .map((s) => s.slice(3))
            .join(", ")}
        </p>
      )}
      <Button
        variant="primary"
        disabled={!msg || disabled}
        onClick={() => {
          onSend(msg);
          setComments({});
        }}
      >
        Send comments to the agent
      </Button>
    </div>
  );
}

interface Cp {
  seq: number;
  label: string;
  time: string;
  this_session: boolean;
  can_rewind_conversation: boolean;
}

/** The workspace's checkpoints, newest first, with what changed since and a way back. */
function Checkpoints({
  sessionId,
  disabled,
  onRestored,
}: {
  sessionId: string;
  disabled: boolean;
  onRestored: () => void;
}) {
  const toast = useToast();
  const [d, setD] = useState<{ available: boolean; reason?: string; checkpoints?: Cp[] } | null>(
    null,
  );
  const [seeing, setSeeing] = useState<{ seq: number; diff: string } | null>(null);
  const [confirm, setConfirm] = useState<{ seq: number; conversation: boolean } | null>(null);
  const load = useCallback(() => {
    void get<{ available: boolean; reason?: string; checkpoints?: Cp[] }>(
      `/sessions/${sessionId}/checkpoints`,
    )
      .then(setD)
      .catch((e) => toast(errText(e), "error"));
  }, [sessionId, toast]);
  useEffect(load, [load]);
  if (!d) return <Skeleton lines={3} />;
  if (!d.available) return <p className="muted">{d.reason}</p>;
  const list = d.checkpoints ?? [];
  return (
    <div className="stack">
      <p className="muted">
        A checkpoint is saved before each of the agent's turns and before risky actions. Restoring
        puts your files back (and can rewind the conversation too). A restore is itself
        checkpointed, so it can be undone. Pushes and deploys are not undone.
      </p>
      <Button onClick={load}>Refresh</Button>
      {list.length === 0 && <p className="muted">No checkpoints yet.</p>}
      {list.map((cp) => (
        <Card key={cp.seq} title={cp.label || `Checkpoint ${cp.seq}`}>
          <p className="muted">
            {ago(cp.time)}
            {cp.this_session ? "" : " · from another session"}
          </p>
          <div className="row">
            <Button
              onClick={async () => {
                try {
                  const r = await get<{ diff: string }>(
                    `/sessions/${sessionId}/checkpoints/${cp.seq}/diff`,
                  );
                  setSeeing({ seq: cp.seq, diff: r.diff });
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              What changed since
            </Button>
            <Button
              disabled={disabled}
              onClick={() => setConfirm({ seq: cp.seq, conversation: false })}
            >
              Restore files
            </Button>
            {cp.can_rewind_conversation && (
              <Button
                disabled={disabled}
                onClick={() => setConfirm({ seq: cp.seq, conversation: true })}
              >
                Restore files and conversation
              </Button>
            )}
          </div>
          {seeing?.seq === cp.seq && (
            <Suspense fallback={<Skeleton lines={2} />}>
              <DiffView text={seeing.diff} />
            </Suspense>
          )}
        </Card>
      ))}
      {confirm && (
        <Modal
          title="Restore this checkpoint?"
          onClose={() => setConfirm(null)}
          actions={
            <>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button
                variant="primary"
                onClick={async () => {
                  try {
                    await post(`/sessions/${sessionId}/checkpoints/${confirm.seq}/restore`, {
                      conversation: confirm.conversation,
                    });
                    toast("Restored.", "success");
                    setConfirm(null);
                    onRestored();
                    load();
                  } catch (x) {
                    toast(errText(x), "error");
                  }
                }}
              >
                Restore
              </Button>
            </>
          }
        >
          <p>
            Files created since then are removed and changed files go back.{" "}
            {confirm.conversation
              ? "Messages after that point are set aside (kept, but no longer part of the conversation). "
              : ""}
            You can undo this by restoring the checkpoint taken just before it.
          </p>
        </Modal>
      )}
    </div>
  );
}

/** Live CPU and memory use of this session's sandbox. */
function Resources({ sessionId }: { sessionId: string }) {
  const [r, setR] = useState<Res | null>(null);
  useEffect(() => {
    let live = true;
    const load = () =>
      void get<Res>(`/sessions/${sessionId}/resources`)
        .then((x) => live && setR(x))
        .catch(() => undefined);
    load();
    const t = setInterval(load, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [sessionId]);
  if (!r) return <Skeleton lines={2} />;
  if (!r.active)
    return (
      <div className="stack">
        <p className="muted">The sandbox is not running. Figures appear once it starts.</p>
        {hookNote(r)}
      </div>
    );
  const cores = r.cpu_cores_normal || 1;
  const memPct = r.memory_limit ? Math.round(((r.memory_bytes ?? 0) / r.memory_limit) * 100) : 0;
  return (
    <div className="stack">
      {r.paused && <Banner tone="warn">The sandbox is paused. Send a message to resume it.</Banner>}
      {r.throttled && (
        <Banner tone="warn">
          The sandbox has been busy for a while, so it is limited to {r.cpu_cores} of {cores} CPU
          cores until things calm down.
        </Banner>
      )}
      <Meter label="CPU right now (% of allowance)" used={pct(r.cpu_percent, cores)} limit={100} />
      <Meter label="CPU typical, last 2 min" used={pct(r.cpu_typical, cores)} limit={100} />
      <Meter label="CPU busiest 10% (p90)" used={pct(r.cpu_busiest, cores)} limit={100} />
      <Meter label="Memory" used={memPct} limit={100} />
      {r.disk_limit ? (
        <Meter
          label="Workspace size (MiB)"
          used={Math.round((r.disk_bytes ?? 0) / 1048576)}
          limit={Math.round(r.disk_limit / 1048576)}
        />
      ) : null}
      <p className="muted">
        Sustained load is what gets limited, not short bursts. {Math.round(r.cpu_seconds ?? 0)} CPU
        seconds used since this sandbox started.
      </p>
      {hookNote(r)}
    </div>
  );
}

const TerminalView = lazy(() => import("./Terminal"));
const Markdown = lazy(() => import("../ui/Markdown"));
const DiffView = lazy(() => import("../ui/DiffView"));

/** Message text with fenced code shown as copyable blocks. */
function Body({ text, plain }: { text: string; plain?: boolean }) {
  if (plain) return <div className="body">{text}</div>;
  return (
    <div className="body md">
      <Suspense fallback={<span className="muted">{text}</span>}>
        <Markdown text={text} />
      </Suspense>
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
