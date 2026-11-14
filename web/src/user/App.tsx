// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { get, post, errText } from "../lib/api";
import { applyTheme, loadPref, nextPref, type ThemePref } from "../lib/theme";
import { clearUserData } from "../lib/queue";
import { ago } from "../lib/format";
import { Icon } from "../ui/ui";
import {
  Banner,
  Button,
  EmptyState,
  Input,
  Modal,
  Select,
  Skeleton,
  StatusPill,
  ToastProvider,
  useShortcuts,
  useToast,
} from "../ui/ui";
import { ChangePassword, Forgot, Login, SetPassword, TwoFactorSetup, AuthFrame } from "./Auth";
import { Chat } from "./Chat";
import { Settings, UsagePanel } from "./Settings";
import { useMe, useRoute } from "./hooks";

interface Session {
  id: string;
  title: string;
  model: string;
  state: string;
  autonomy: string;
  updated_at: string;
}
interface ModelOpt {
  id: string;
  name: string;
  provider: string;
  tools: boolean;
  recommended: boolean;
}

function NewSession({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (s: Session) => void;
}) {
  const toast = useToast();
  const [models, setModels] = useState<ModelOpt[] | null>(null);
  const [model, setModel] = useState("");
  const [autonomy, setAutonomy] = useState("workspace");
  const [title, setTitle] = useState("");
  useEffect(() => {
    void get<ModelOpt[]>("/models").then((m) => {
      const sorted = [...m].sort(
        (x, y) =>
          Number(y.recommended) - Number(x.recommended) ||
          Number(y.tools) - Number(x.tools) ||
          x.id.localeCompare(y.id),
      );
      setModels(sorted);
      if (sorted[0]) setModel(sorted[0].id);
    });
  }, []);
  async function create() {
    try {
      onCreated(await post<Session>("/sessions", { model, autonomy, title }));
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  const levels = [
    {
      id: "ask",
      name: "Ask before running anything",
      hint: "Every command and file change waits for you.",
    },
    {
      id: "workspace",
      name: "Automatic in the workspace",
      hint: "Routine work runs by itself; risky actions ask first. Recommended.",
    },
    {
      id: "full",
      name: "Fully autonomous",
      hint: "Runs everything within limits. Risky actions are still logged.",
    },
  ];
  return (
    <Modal
      title="New session"
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={create} disabled={!model}>
            Create
          </Button>
        </>
      }
    >
      {models === null ? (
        <Skeleton lines={2} />
      ) : models.length === 0 ? (
        <Banner tone="warn">
          No models are available to you yet. Ask your admin to enable one.
        </Banner>
      ) : (
        <>
          <Input
            label="Title (optional)"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="e.g. Add pagination to the API"
          />
          <Select
            label="Model"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            options={models.map((m) => ({
              value: m.id,
              label:
                m.id + (m.recommended ? " — recommended" : "") + (m.tools ? "" : " (chat only)"),
            }))}
          />
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend style={{ fontWeight: 600, fontSize: "var(--fs-sm)", marginBottom: 8 }}>
              How much should it ask you?
            </legend>
            {levels.map((l) => (
              <button
                key={l.id}
                type="button"
                className="choice"
                aria-pressed={autonomy === l.id}
                onClick={() => setAutonomy(l.id)}
              >
                <strong>{l.name}</strong>
                <small>{l.hint}</small>
              </button>
            ))}
          </fieldset>
        </>
      )}
    </Modal>
  );
}

function Shell({
  me,
  reload,
  clear,
  go,
}: {
  me: NonNullable<ReturnType<typeof useMe>["me"]>;
  reload: () => Promise<void>;
  clear: () => void;
  go: (p: string) => void;
}) {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [current, setCurrent] = useState<string>("");
  const [view, setView] = useState<"chat" | "settings">("chat");
  const [creating, setCreating] = useState(false);
  const [pref, setPref] = useState<ThemePref>(loadPref());
  const [dismissed2fa, setDismissed2fa] = useState(false);
  const [q, setQ] = useState("");
  const [drawer, setDrawer] = useState(false);
  const [panelOpen, setPanelOpen] = useState(false);

  const refresh = useCallback(async () => {
    setSessions(await get<Session[]>("/sessions" + (q ? `?q=${encodeURIComponent(q)}` : "")));
  }, [q]);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  useEffect(() => {
    if (new URLSearchParams(window.location.search).get("action") === "new") setCreating(true);
  }, []);

  useShortcuts({
    n: () => setCreating(true),
    s: () => setView((v) => (v === "settings" ? "chat" : "settings")),
  });

  const logout = async () => {
    try {
      await post("/logout");
    } catch {
      /* already signed out */
    }
    clearUserData();
    if ("caches" in window)
      void caches
        .keys()
        .then((ks) => ks.filter((k) => k.startsWith("syx-user-")).forEach((k) => caches.delete(k)));
    clear();
    go("/");
  };
  const cur = sessions?.find((s) => s.id === current);
  const showReminder = me.remind_2fa && !dismissed2fa;
  const theme = <Icon name={pref === "dark" ? "moon" : pref === "light" ? "sun" : "auto"} />;

  return (
    <div className="shell">
      <header className="topbar">
        <Button
          variant="icon"
          className="sidebar-toggle"
          aria-label="Sessions"
          aria-expanded={drawer}
          onClick={() => setDrawer(!drawer)}
        >
          <Icon name="menu" />
        </Button>
        <h1 className="brand">Syntaxis</h1>
        <Button
          variant="ghost"
          className="icon-only"
          aria-label="New session"
          onClick={() => {
            setCreating(true);
            setDrawer(false);
          }}
        >
          <Icon name="plus" />
          <span className="label">New session</span>
        </Button>
        <Button
          variant="icon"
          aria-label={`Theme: ${pref}. Click to change`}
          onClick={() => {
            const n = nextPref(pref);
            setPref(n);
            applyTheme(n);
          }}
        >
          {theme}
        </Button>
        <Button
          variant="ghost"
          className="icon-only"
          aria-label={view === "settings" ? "Back to sessions" : "Settings"}
          onClick={() => {
            setView(view === "settings" ? "chat" : "settings");
            setDrawer(false);
          }}
        >
          <Icon name={view === "settings" ? "back" : "gear"} />
          <span className="label">{view === "settings" ? "Back" : "Settings"}</span>
        </Button>
        <Button variant="ghost" className="icon-only" aria-label="Sign out" onClick={logout}>
          <Icon name="logout" />
          <span className="label">Sign out</span>
        </Button>
      </header>
      {me.banners?.map((b) => (
        <Banner key={b} tone="warn">
          {b}
        </Banner>
      ))}
      {me.password_days_left >= 0 && (
        <Banner tone="warn">
          Your password expires in {me.password_days_left} day(s). Change it in Settings, or turn on
          two-factor authentication to stop rotation.
        </Banner>
      )}
      <div className="layout">
        <nav className={`sidebar ${drawer ? "open" : ""}`} aria-label="Sessions">
          <input
            className="input"
            aria-label="Search sessions"
            placeholder="Search…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          {sessions === null ? (
            <Skeleton />
          ) : sessions.length === 0 ? (
            <EmptyState
              title="No sessions yet"
              hint="Start one to give the agent a sandbox."
              action={
                <Button variant="primary" onClick={() => setCreating(true)}>
                  New session
                </Button>
              }
            />
          ) : (
            sessions.map((s) => (
              <button
                key={s.id}
                className="session-item"
                aria-current={s.id === current}
                onClick={() => {
                  setCurrent(s.id);
                  setView("chat");
                  setDrawer(false);
                }}
              >
                <div className="row">
                  <strong className="grow">{s.title}</strong>
                  <StatusPill state={s.state} />
                </div>
                <div className="sub">
                  {s.model} · {ago(s.updated_at)}
                </div>
              </button>
            ))
          )}
        </nav>
        <main className="main" style={{ padding: view === "chat" && cur ? 0 : undefined }}>
          {view === "settings" ? (
            <Settings me={me} reload={() => void reload()} onLogout={logout} />
          ) : cur ? (
            <Chat
              key={cur.id}
              session={cur}
              userKey={me.email}
              onChanged={() => void refresh()}
              onDeleted={() => {
                setCurrent("");
                void refresh();
              }}
              panelOpen={panelOpen}
              setPanelOpen={setPanelOpen}
            />
          ) : (
            <>
              <EmptyState
                title="Pick a session"
                hint="Open the session list (menu button on a phone), or press N to start a new one."
                action={
                  <Button variant="primary" onClick={() => setCreating(true)}>
                    New session
                  </Button>
                }
              />
              <UsagePanel />
            </>
          )}
        </main>
      </div>
      {creating && (
        <NewSession
          onClose={() => setCreating(false)}
          onCreated={(s) => {
            setCreating(false);
            setCurrent(s.id);
            setView("chat");
            void refresh();
          }}
        />
      )}
      {showReminder && (
        <Modal
          title="Protect your account"
          onClose={() => setDismissed2fa(true)}
          actions={
            <>
              <Button onClick={() => setDismissed2fa(true)}>Remind me next time</Button>
              <Button
                variant="primary"
                onClick={() => {
                  setDismissed2fa(true);
                  setView("settings");
                }}
              >
                Set up now
              </Button>
            </>
          }
        >
          <p>
            Two-factor authentication is not turned on for your account. It protects your sandboxes
            and credentials, and exempts you from regular password changes.
          </p>
        </Modal>
      )}
    </div>
  );
}

export function UserApp() {
  const { me, reload, clear } = useMe();
  const [path, go] = useRoute();
  const url = new URL(path, window.location.origin);
  const token = url.searchParams.get("token") ?? "";

  if (url.pathname === "/set-password") return <SetPassword kind="invite" token={token} go={go} />;
  if (url.pathname === "/reset-password") return <SetPassword kind="reset" token={token} go={go} />;
  if (me === undefined)
    return (
      <AuthFrame title="Syntaxis">
        <Skeleton />
      </AuthFrame>
    );
  if (me === null) {
    if (url.pathname === "/forgot") return <Forgot go={go} />;
    return <Login onDone={() => void reload()} go={go} />;
  }
  if (me.must_change_password)
    return (
      <AuthFrame title="Change your password">
        <ChangePassword
          forced
          onDone={() => {
            clear();
            go("/");
          }}
        />
      </AuthFrame>
    );
  return <Shell me={me} reload={reload} clear={clear} go={go} />;
}

export default function App() {
  useEffect(() => {
    applyTheme(loadPref());
  }, []);
  return (
    <ToastProvider>
      <UserApp />
    </ToastProvider>
  );
}

export { TwoFactorSetup };
