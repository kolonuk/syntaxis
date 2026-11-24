// SPDX-License-Identifier: GPL-2.0-or-later
import { useState, type FormEvent } from "react";
import { del, errText, patch, post } from "../lib/api";
import { copyText } from "../lib/fences";
import { Badge, Banner, Button, Card, Input, Modal, Skeleton, useToast } from "../ui/ui";
import { useLoad } from "./shared";

export interface AdminAccount {
  id: number;
  email: string;
  name: string;
  totp_enabled: boolean;
  disabled: boolean;
  pending: boolean;
  you: boolean;
  created_at: string;
}

interface SetupLink {
  id: number;
  setup_path: string;
  expires_hours: number;
}

/** What state an admin account is in, in words. */
export function adminState(a: AdminAccount): {
  label: string;
  tone: "success" | "warn" | "danger" | "neutral";
} {
  if (a.disabled) return { label: "disabled", tone: "neutral" };
  if (a.pending) return { label: "invited, not set up yet", tone: "warn" };
  if (!a.totp_enabled) return { label: "no 2FA", tone: "danger" };
  return { label: "2FA on", tone: "success" };
}

/** Admin accounts: who can sign in to this app, and how to add, reset, disable or remove one. */
export function AdminsTab() {
  const toast = useToast();
  const list = useLoad<AdminAccount[]>("/admins");
  const [adding, setAdding] = useState(false);
  const [f, setF] = useState({ name: "", email: "" });
  const [link, setLink] = useState<{ who: string; url: string; hours: number } | null>(null);
  const [confirm, setConfirm] = useState<{ a: AdminAccount; what: "reset" | "remove" } | null>(
    null,
  );

  const showLink = (who: string, r: SetupLink) =>
    setLink({ who, url: window.location.origin + r.setup_path, hours: r.expires_hours });

  async function invite(e: FormEvent) {
    e.preventDefault();
    try {
      const r = await post<SetupLink>("/admins", f);
      setAdding(false);
      showLink(f.name || f.email, r);
      setF({ name: "", email: "" });
      list.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  async function run() {
    if (!confirm) return;
    const { a, what } = confirm;
    try {
      if (what === "reset") {
        showLink(a.name, await post<SetupLink>(`/admins/${a.id}/reset`));
      } else {
        await del(`/admins/${a.id}`);
        toast(`${a.email} was removed.`, "success");
      }
      list.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
    setConfirm(null);
  }
  async function setDisabled(a: AdminAccount, disabled: boolean) {
    try {
      await patch(`/admins/${a.id}`, { disabled });
      list.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (list.err) return <Banner tone="danger">{list.err}</Banner>;
  if (!list.data) return <Skeleton />;
  const noTwoFA = list.data.filter((a) => !a.disabled && !a.pending && !a.totp_enabled).length;
  return (
    <div className="stack">
      <div className="row">
        <p className="muted grow">
          Admins sign in to this admin app only, and always with two-factor authentication. They are
          separate from the chat users on the Users tab: an admin who also wants to chat has a user
          account too.
        </p>
        <Button variant="primary" onClick={() => setAdding(true)}>
          Add an admin
        </Button>
      </div>
      {noTwoFA > 0 && (
        <Banner tone="danger">
          {noTwoFA} admin {noTwoFA === 1 ? "account has" : "accounts have"} a password but no 2FA.
          They must finish 2FA at their next sign-in. Reset or remove an account you do not
          recognise.
        </Banner>
      )}
      <Card title="Admin accounts">
        <div className="table-wrap" tabIndex={0} role="region" aria-label="Admin accounts">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Email</th>
                <th>Status</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.data.map((a) => {
                const s = adminState(a);
                return (
                  <tr key={a.id}>
                    <td>
                      {a.name} {a.you && <Badge>you</Badge>}
                    </td>
                    <td>{a.email}</td>
                    <td>
                      <Badge tone={s.tone}>{s.label}</Badge>
                    </td>
                    <td className="actions-cell">
                      {!a.you && (
                        <>
                          <Button onClick={() => setConfirm({ a, what: "reset" })}>
                            {a.pending ? "New setup link" : "Reset (new link)"}
                          </Button>{" "}
                          <Button onClick={() => void setDisabled(a, !a.disabled)}>
                            {a.disabled ? "Enable" : "Disable"}
                          </Button>{" "}
                          <Button
                            variant="danger"
                            onClick={() => setConfirm({ a, what: "remove" })}
                          >
                            Remove
                          </Button>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <p className="muted">
          You cannot reset, disable or remove your own account here, so you cannot lock yourself
          out. Change your own password or 2FA from your account.
        </p>
      </Card>
      {adding && (
        <Modal
          title="Add an admin"
          onClose={() => setAdding(false)}
          actions={
            <>
              <Button onClick={() => setAdding(false)}>Cancel</Button>
              <Button
                variant="primary"
                type="submit"
                form="admin-invite-form"
                disabled={!f.name || !f.email}
              >
                Create setup link
              </Button>
            </>
          }
        >
          <form id="admin-invite-form" onSubmit={invite}>
            <Input
              label="Name"
              placeholder="Alex Admin"
              value={f.name}
              onChange={(e) => setF({ ...f, name: e.target.value })}
              required
            />
            <Input
              label="Email"
              type="email"
              placeholder="alex@example.com"
              value={f.email}
              onChange={(e) => setF({ ...f, email: e.target.value })}
              required
              hint="Used to sign in. You get a single-use link to give them; nothing is emailed."
            />
          </form>
        </Modal>
      )}
      {link && (
        <Modal
          title={`Setup link for ${link.who}`}
          onClose={() => setLink(null)}
          actions={
            <>
              <Button
                onClick={async () =>
                  toast((await copyText(link.url)) ? "Copied." : "Could not copy.", "info")
                }
              >
                Copy link
              </Button>
              <Button variant="primary" onClick={() => setLink(null)}>
                Done
              </Button>
            </>
          }
        >
          <p>
            Give this to them directly. It works once and expires in {link.hours} hours. They choose
            a password and then set up two-factor authentication.
          </p>
          <pre className="output" aria-label="Setup link">
            {link.url}
          </pre>
        </Modal>
      )}
      {confirm && (
        <Modal
          title={
            confirm.what === "reset" ? `Reset ${confirm.a.email}?` : `Remove ${confirm.a.email}?`
          }
          onClose={() => setConfirm(null)}
          actions={
            <>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button
                variant={confirm.what === "remove" ? "danger" : "primary"}
                onClick={() => void run()}
              >
                {confirm.what === "reset" ? "Reset and make a link" : "Remove"}
              </Button>
            </>
          }
        >
          {confirm.what === "reset" ? (
            <p>
              This signs {confirm.a.name} out everywhere and clears their password and 2FA. They get
              back in with the new single-use link. Do this when they are locked out or you suspect
              the account.
            </p>
          ) : (
            <p>
              {confirm.a.name} loses access at once and the account is deleted. This is recorded in
              the audit log and cannot be undone; you can add them again later.
            </p>
          )}
        </Modal>
      )}
    </div>
  );
}
