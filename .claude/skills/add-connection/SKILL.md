---
name: add-connection
description: Use when adding a service users can connect their own account to (a git host or a cloud). Lists the files to touch, the per-user and security rules, and how to test it for real.
---

# Adding a connection

Connections live in `internal/connections`. Credentials are per user and stored in that user's vault; they are never server-wide and never sent to the model.

1. **Provider** (`<name>.go`): a `Kind` (id, title, fields with `Secret`, `Required`, `Hint`, `Default`) and `Test(ctx, creds)` that asks the real service who the credentials belong to. Wrong credentials return `ErrBadCredentials`; errors must never contain the credential.
2. **Git hosts** also implement `GitProvider`: `ListRepos`, `CreateRepo`, `OpenPR`, `ReadFile` (for `devcontainer.json`). Validate repository names and paths before they go into a URL (`validFullName`, `safeRepoPath`, `safeRef`).
3. Register it in `New` (`connections.go`). Add the expected clone host in `expectedHost` (`internal/server/repos.go`) so a session can only clone from the service the user connected.
4. **Sandbox tools**: non-secret fields (an address) are injected as plain environment; secrets are redacted from output. A new git host needs the credential helper in `images/sandbox` to know it.
5. **UI**: the Connections page is generated from the `Kind`; check it shows a guide for a least-privilege credential.
6. **Tests**: a wrong-credential test against the real service (no account needed), and for a self-hostable git service a real container like `testenv.Gitea`. No test uses a real account or token.
7. **Docs**: `docs/integrations.md` (list and guide), `CHANGELOG.md`, and the `Contributing` test variable list if you added one.
