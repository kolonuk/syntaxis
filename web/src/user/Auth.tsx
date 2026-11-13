// SPDX-License-Identifier: GPL-2.0-or-later
import { useState, type FormEvent } from "react";
import { post, setCsrf, errText } from "../lib/api";
import { Banner, Button, Card, Input } from "../ui/ui";

interface LoginResult {
  needs_mfa: boolean;
  csrf: string;
}

export function AuthFrame({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <main className="auth-wrap">
      <Card>
        <h1 style={{ marginTop: 0 }}>{title}</h1>
        {children}
      </Card>
    </main>
  );
}

export function Login({
  onDone,
  go,
  prefix = "",
}: {
  onDone: () => void;
  go: (p: string) => void;
  prefix?: string;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [mfa, setMfa] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      if (!mfa) {
        const r = await post<LoginResult>("/login", { email, password });
        if (r.needs_mfa) setMfa(true);
        else {
          setCsrf(r.csrf);
          onDone();
        }
      } else {
        const r = await post<LoginResult>("/login/mfa", { code });
        setCsrf(r.csrf);
        onDone();
      }
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthFrame title={prefix ? `${prefix} sign in` : "Sign in"}>
      <form onSubmit={submit}>
        {err && <Banner tone="danger">{err}</Banner>}
        {!mfa ? (
          <>
            <Input
              label="Email"
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
            />
            <Input
              label="Password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </>
        ) : (
          <Input
            label="Authentication code"
            hint="Enter the 6-digit code from your authenticator app, or a recovery code."
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
        )}
        <Button variant="primary" type="submit" loading={busy} style={{ width: "100%" }}>
          {mfa ? "Verify" : "Sign in"}
        </Button>
      </form>
      {!prefix && (
        <p className="muted">
          <a
            href="/forgot"
            onClick={(e) => {
              e.preventDefault();
              go("/forgot");
            }}
          >
            Forgot your password?
          </a>
        </p>
      )}
    </AuthFrame>
  );
}

export function Forgot({ go }: { go: (p: string) => void }) {
  const [email, setEmail] = useState("");
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await post<{ message: string }>("/password/forgot", { email });
      setMsg(r.message);
    } catch (x) {
      setMsg(errText(x));
    } finally {
      setBusy(false);
    }
  }
  return (
    <AuthFrame title="Reset your password">
      <form onSubmit={submit}>
        {msg && <Banner>{msg}</Banner>}
        <Input
          label="Email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
        />
        <Button variant="primary" type="submit" loading={busy}>
          Send reset link
        </Button>
        <Button variant="ghost" type="button" onClick={() => go("/")}>
          Back
        </Button>
      </form>
    </AuthFrame>
  );
}

/** Used for both the invite link and the reset link. */
export function SetPassword({
  kind,
  token,
  go,
}: {
  kind: "invite" | "reset";
  token: string;
  go: (p: string) => void;
}) {
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (pw !== pw2) {
      setErr("The passwords do not match.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await post(kind === "invite" ? "/invite/accept" : "/password/reset", { token, password: pw });
      setDone(true);
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  }
  if (done)
    return (
      <AuthFrame title="Password saved">
        <p>You can now sign in.</p>
        <Button variant="primary" onClick={() => go("/")}>
          Go to sign in
        </Button>
      </AuthFrame>
    );
  return (
    <AuthFrame title={kind === "invite" ? "Welcome — choose a password" : "Choose a new password"}>
      <form onSubmit={submit}>
        {err && <Banner tone="danger">{err}</Banner>}
        <Input
          label="New password"
          type="password"
          autoComplete="new-password"
          hint="At least 12 characters. A passphrase works well."
          value={pw}
          onChange={(e) => setPw(e.target.value)}
          required
        />
        <Input
          label="Repeat password"
          type="password"
          autoComplete="new-password"
          value={pw2}
          onChange={(e) => setPw2(e.target.value)}
          required
        />
        <Button variant="primary" type="submit" loading={busy}>
          Save password
        </Button>
      </form>
    </AuthFrame>
  );
}

export function ChangePassword({ onDone, forced }: { onDone: () => void; forced?: boolean }) {
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await post("/password/change", { current: cur, new: pw });
      onDone();
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit}>
      {forced && (
        <Banner tone="warn">
          Your password has expired. Choose a new one to continue. (Turning on two-factor
          authentication exempts you from regular changes.)
        </Banner>
      )}
      {err && <Banner tone="danger">{err}</Banner>}
      <Input
        label="Current password"
        type="password"
        autoComplete="current-password"
        value={cur}
        onChange={(e) => setCur(e.target.value)}
        required
      />
      <Input
        label="New password"
        type="password"
        autoComplete="new-password"
        value={pw}
        onChange={(e) => setPw(e.target.value)}
        required
      />
      <Button variant="primary" type="submit" loading={busy}>
        Change password
      </Button>
    </form>
  );
}

export function TwoFactorSetup({ onDone, mandatory }: { onDone: () => void; mandatory?: boolean }) {
  const [setup, setSetup] = useState<{ secret: string; url: string } | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function begin() {
    setBusy(true);
    try {
      setSetup(await post("/2fa/begin"));
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  }
  async function confirm(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const r = await post<{ recovery_codes: string[] }>("/2fa/confirm", { code });
      setCodes(r.recovery_codes);
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  }
  if (codes)
    return (
      <div className="stack">
        <Banner tone="warn">
          Save these recovery codes somewhere safe. Each works once and they are shown only now.
        </Banner>
        <pre aria-label="Recovery codes">{codes.join("\n")}</pre>
        <Button variant="primary" onClick={onDone}>
          I have saved them
        </Button>
      </div>
    );
  return (
    <div className="stack">
      {mandatory && (
        <Banner tone="warn">Two-factor authentication is required for this account.</Banner>
      )}
      {err && <Banner tone="danger">{err}</Banner>}
      {!setup ? (
        <Button variant="primary" onClick={begin} loading={busy}>
          Start setup
        </Button>
      ) : (
        <form onSubmit={confirm}>
          <p>
            Add this account to your authenticator app using the key below (or the otpauth link),
            then enter the 6-digit code.
          </p>
          <p>
            <code aria-label="Setup key">{setup.secret}</code>
          </p>
          <p>
            <a href={setup.url}>Open in authenticator app</a>
          </p>
          <Input
            label="6-digit code"
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
          <Button variant="primary" type="submit" loading={busy}>
            Enable two-factor
          </Button>
        </form>
      )}
    </div>
  );
}
