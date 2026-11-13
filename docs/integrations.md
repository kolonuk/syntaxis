# Integrations

## Secrets vault

Users add their own tokens under **Settings → Secrets** (names are `UPPER_SNAKE_CASE`). Values are encrypted with AES-256-GCM (key from `SYNTAXIS_MASTER_KEY`), never shown again, never sent to the model, redacted from output and logs, and injected into the user's sandbox **only while it runs**. Instance secrets (provider API keys) are admin-owned and never injected into sandboxes.

## GitHub

Add a fine-grained personal access token as `GITHUB_TOKEN` (or `GH_TOKEN`): repository **Contents: read/write** and **Pull requests: read/write** for the repositories you want, nothing more. Inside a session the agent can clone, create a branch, commit, push and open a PR (`gh` is in the cloud-tools image). GitHub App support is planned.

## Generic git over HTTPS/SSH

HTTPS: use a token the same way. SSH deploy keys: store the private key as a secret and have the agent write it to a file outside `/workspace` with mode 0600 (a credential helper is planned). Works with GitLab and Gitea.

## Cloud deploys

The cloud-tools image (`images/sandbox-cloud`) adds `gcloud`, `aws`, `az`, `wrangler`, `terraform`/`opentofu`, `kubectl`, `helm`, `flyctl` and `gh` as a separate tag so the default image stays small. First-class AWS/Azure integrations are out of scope; the CLIs are simply available.

**Use your own credentials, least privilege, short-lived:**
- Prefer OIDC/workload identity or scoped API tokens over long-lived keys.
- Use a dedicated project/account with a **budget alert**.
- Cloudflare: a token scoped to the zone/Workers permissions needed.
- Remember the model sees command output; redaction is best-effort.

Safety rails: the approval gate fires on `apply`/`deploy`/`delete`/`destroy`-style commands; the system prompt tells the agent to plan first and ask; an admin can disable cloud tooling per user.

### Example: deploy a Worker (illustrative)

1. Add `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` as secrets.
2. Ask: "Create a hello-world Worker, test it locally, then deploy it."
3. The agent runs `wrangler dev` locally (no approval), then `wrangler deploy` → **approval prompt** → you approve → deployed.

### Docker builds inside a sandbox

Sandboxes have no Docker socket. Whether `docker build` / rootless buildkit works under gVisor has **not been tested** and is not claimed. Recommended: remote builds (Cloud Build, GitHub Actions) or `kaniko`/`buildah` where they work.

## Security scanners (on demand)

Trivy and `osv-scanner` live in the optional `images/sandbox-security` image and run only when asked ("Scan project" or the agent tool), never automatically and never in the base image.

## MCP servers

Planned: an admin catalogue (starting from the official MCP Registry) with review → approve → run each server in its own gVisor container. Not implemented yet.

## Integrations v2 (design only)

Cloudflare (DNS and zones) and registrar connectors will be plugins with declared permissions behind a generic "API token + base URL" connector.
