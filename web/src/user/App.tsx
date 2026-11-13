// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { get, post, errText } from "../lib/api";
import { applyTheme, loadPref, nextPref, type ThemePref } from "../lib/theme";
import { clearUserData } from "../lib/queue";
import { ago } from "../lib/format";
import {
  Banner,
  Button,
  EmptyState,
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
  useEffect(() => {
    void get<ModelOpt[]>("/models").then((m) => {
      setModels(m);
      if (m[0]) setModel(m[0].id);
    });
  }, []);
  async function create() {
    try {
      onCreated(await post<Session>("/sessions", { model, autonomy }));
    } catch (x) {
      toast(errText(x), "error");
    }
  }
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
          <Select
            label="Model"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            options={models.map((m) => ({
              value: m.id,
              label: m.id + (m.tools ? "" : " (chat only)"),
            }))}
          />
          <Select
            label="Autonomy"
            value={autonomy}
            onChange={(e) => setAutonomy(e.target.value)}
            options={[
              { value: "ask", label: "Ask before running anything" },
              {
                value: "workspace",
                label: "Automatic in the workspace, ask for risky actions (default)",
              },
              { value: "full", label: "Fully autonomous within limits" },
            ]}
          />
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

  const refresh = useCallback(async () => {
    setSessions(await get<Session[]>("/sessions" + (q ? `?q=${encodeURIComponent(q)}` : "")));
  }, [q]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

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

  // sign-in reminder: shown at EVERY login for users without 2FA until set up
  const showReminder = me.remind_2fa && !dismissed2fa;

  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">Syntaxis</span>
        <Button variant="ghost" onClick={() => setCreating(true)}>
          New session
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
          {pref === "dark" ? "☾" : pref === "light" ? "☀" : "◐"}
        </Button>
        <Button variant="ghost" onClick={() => setView(view === "settings" ? "chat" : "settings")}>
          {view === "settings" ? "Back" : "Settings"}
        </Button>
        <Button variant="ghost" onClick={logout}>
          Sign out
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
        <nav className="sidebar" aria-label="Sessions">
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
                }}
              >
                <div className="row">
                  <strong className="grow">{s.title}</strong>
                  <StatusPill state={s.state} />
                </div>
                <div className="muted">
                  {s.model} · {ago(s.updated_at)}
                </div>
              </button>
            ))
          )}
        </nav>
        <div className="main" style={{ padding: view === "chat" && cur ? 0 : undefined }}>
          {view === "settings" ? (
            <Settings me={me} reload={() => void reload()} onLogout={logout} />
          ) : cur ? (
            <Chat key={cur.id} session={cur} userKey={me.email} onChanged={() => void refresh()} />
          ) : (
            <>
              <EmptyState
                title="Pick a session"
                hint="Choose one on the left, or press N to start a new one."
              />
              <UsagePanel />
            </>
          )}
        </div>
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
