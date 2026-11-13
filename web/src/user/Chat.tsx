// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useRef, useState } from "react";
import { get, post, errText } from "../lib/api";
import { streamEvents, type StreamEvent } from "../lib/sse";
import { enqueue, flush } from "../lib/queue";
import {
  Badge,
  Banner,
  Button,
  Meter,
  Modal,
  StatusPill,
  Tabs,
  TextArea,
  useToast,
} from "../ui/ui";
import { ActivityTerminal } from "./Terminal";

interface Msg {
  role: string;
  content: string;
  tool_calls?: string;
}
interface Session {
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

export function Chat({
  session,
  userKey,
  onChanged,
}: {
  session: Session;
  userKey: string;
  onChanged: () => void;
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
  const [panel, setPanel] = useState("terminal");
  const [running, setRunning] = useState(session.state === "running");
  const [notice, setNotice] = useState("");
  const abort = useRef<AbortController | null>(null);
  const logEnd = useRef<HTMLDivElement>(null);

  const loadMessages = useCallback(async () => {
    const m = await get<Msg[]>(`/sessions/${session.id}/messages`);
    setMsgs(m.filter((x) => x.role !== "tool"));
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
          setLines((l) => [...l, `\x1b[36m$ ${String(d.summary ?? d.tool)}\x1b[0m`]);
          break;
        case "tool_result":
          setLines((l) => [...l, String(d.output ?? "").slice(0, 4000)]);
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
        case "notice":
          setNotice(String(d.message ?? ""));
          break;
        case "compaction":
          setNotice("Older turns were summarised to free up context. Your full history is kept.");
          break;
        case "status":
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
    void loadMessages();
    attach();
    return () => abort.current?.abort();
  }, [session.id, attach, loadMessages]);

  useEffect(() => {
    logEnd.current?.scrollIntoView({ behavior: "smooth" });
  }, [msgs, live]);

  // send queued messages when we come back online
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
    setMsgs((m) => [...m, { role: "user", content: text }]);
    if (!navigator.onLine) {
      enqueue(userKey, { sessionId: session.id, text });
      toast("You are offline. The message will be sent when you reconnect.");
      return;
    }
    try {
      await post(`/sessions/${session.id}/messages`, { text });
      setRunning(true);
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

  const ctxTone = ctx && ctx.percent >= 75;

  return (
    <div className="layout" style={{ height: "100%" }}>
      <div className="main chat">
        <div className="row" style={{ marginBottom: 8 }}>
          <h2 style={{ margin: 0 }} className="grow">
            {session.title}
          </h2>
          <StatusPill
            state={running ? "running" : session.state === "running" ? "idle" : session.state}
          />
          <Badge>{session.model}</Badge>
          {running && <Button onClick={() => post(`/sessions/${session.id}/stop`)}>Stop</Button>}
        </div>
        {notice && <Banner>{notice}</Banner>}
        <div className="chat-log" role="log" aria-live="polite">
          {msgs.map((m, i) => (
            <div className={`msg ${m.role}`} key={i}>
              <div className="who">{m.role === "user" ? "You" : "Assistant"}</div>
              {m.content && <div className="body">{m.content}</div>}
            </div>
          ))}
          {live && (
            <div className="msg assistant">
              <div className="who">Assistant</div>
              <div className="body">{live}</div>
            </div>
          )}
          <div ref={logEnd} />
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
      <aside className="side-panel" aria-label="Session details">
        <Tabs
          tabs={[
            { id: "terminal", label: "Terminal" },
            { id: "context", label: "Context" },
          ]}
          value={panel}
          onChange={setPanel}
        />
        {panel === "terminal" && <ActivityTerminal lines={lines} />}
        {panel === "context" && (
          <div className="stack">
            {ctx ? (
              <>
                <Meter label="Context window" used={ctx.used} limit={ctx.window} />
                {ctxTone && (
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
    </div>
  );
}
