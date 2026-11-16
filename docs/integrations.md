# Integrations

## Secrets vault

Users add their own tokens under **Settings → Secrets** (names are `UPPER_SNAKE_CASE`). Values are encrypted with AES-256-GCM (key from `SYNTAXIS_MASTER_KEY`), never shown again, never sent to the model, redacted from output and logs, and supplied to the user's sandbox commands. They are read fresh for every command, so adding, changing or removing a secret applies immediately, with no new session needed. An already-open terminal keeps the values it started with; reconnect it to pick up changes. Instance secrets (provider API keys) are admin-owned and never injected into sandboxes.

## Connections to other services

Each user connects their own accounts under **Settings → Connections**: GitHub, GitLab (including self-hosted), Gitea or Forgejo, Cloudflare, Amazon Web Services, Microsoft Azure and Google Cloud. A connection is that user's own credentials. They are checked against the service when saved, kept in the user's own vault (nobody else, admins included, can see or use them), and put into the environment of that user's own commands under the names the services' tools expect (`GITHUB_TOKEN`, `AWS_ACCESS_KEY_ID`, `AZURE_CLIENT_SECRET`, `GOOGLE_CLOUD_PROJECT` and so on), so `git`, `gh`, `aws`, `az`, `gcloud` and `wrangler` work as they do on a laptop. They are never sent to the model. Each service has a short guide on creating a least-privilege, preferably short-lived credential.

For the git services, Syntaxis also uses the connection to list the user's repositories, create a new one, and open pull requests (merge requests on GitLab).

### Starting from a repository

In **New session**, choose a connected service and one of your repositories, or create a new one. Syntaxis clones it into the session's sandbox, creates a working branch (`syntaxis/<id>`) from the repository's default branch, and starts the agent there. The agent commits to that branch, pushes it and opens a pull request with the git tool's `pr` action. The pull request is opened by the server with the user's own connection, so the token is not in any command the model writes, and opening one needs approval at the default autonomy level. Commits use the user's account name at the service, not their sign-in email. A clone address is accepted only if it is on the service the user connected and matches the repository's name.

Git in the sandbox finds the token through a credential helper that answers only for the matching host: a token is never offered to any other site. Git over SSH with deploy keys is not set up; use the token.

### Cloud accounts

Use accounts and projects that belong to you, with a budget alert, and the least privilege that does the job. Prefer temporary credentials (an AWS session token, a short secret lifetime on an Azure service principal). The approval gate fires on deploy, delete, create and destroy-style commands, and its request names the connection and project the command would act through. Remember the model sees command output; redaction is best-effort.

## Cloud deploys

The cloud-tools image (`images/sandbox-cloud`) adds `gcloud`, `aws`, `az`, `wrangler`, `terraform`/`opentofu`, `kubectl`, `helm`, `flyctl` and `gh` as a separate tag so the default image stays small. The cloud connections above provide the credentials; the CLIs are simply available.

Safety rails: the approval gate fires on `apply`/`deploy`/`delete`/`destroy`-style commands; the system prompt tells the agent to plan first and ask; an admin can disable cloud tooling per user.

### Docker builds inside a sandbox

Sandboxes have no Docker socket, and building images inside gVisor is untested. Use remote builds instead.

## Automations: schedules, webhooks and watches

An automation starts a session by itself, as its owner and within their limits (token and compute quotas, concurrent sessions, egress mode, approvals). Each run is an ordinary session in the owner's list. Set them up under **Automations**.

- **Schedule:** a cron expression and a time zone, at most every 5 minutes. Example: `0 3 * * *` runs every night at 03:00.
- **Watch:** Syntaxis checks a source every few minutes (at least 5, default 30) and starts a run when something is new. It remembers how things are when the automation is created, so it only fires for what is new from then on. Sources: a **GitHub release**, a **git branch or new tags** of any public repository, a **Docker Hub image** (optionally only tags matching a pattern), a **package release** on npm, PyPI or crates.io, an **RSS or Atom feed** (which covers release feeds, blogs and advisories), and a **web page or file** that changes. Watches use your GitHub connection, if you have one, to reach your private repositories and to avoid rate limits. They are made by the server through a client that refuses private, loopback, link-local and metadata addresses, so a watch cannot be pointed at your network.
- **Webhook:** you get a secret address (`/hooks/<id>/<secret>` on the chat app's address) when you save; it is shown once and can be replaced. Anything that can send a web request can call it: an IFTTT "Make a web request" action, a GitHub webhook, a script. The request body is passed to the run as event data.
- **Event data is untrusted.** Whatever a watch found or a webhook received is put into the run's prompt inside an `<event>` block that tells the agent it is information about what happened and never instructions. Policy (approvals, quotas, egress, secrets) is enforced by the server regardless.
- **IFTTT:** connect your Webhooks key under **Settings → Connections**, then give an automation an IFTTT event name and it triggers that applet when the run finishes, with its name, outcome and a short summary.
- **Limits:** up to 20 automations per user, 48 runs per automation per day, one run at a time per automation, and an automation that fails 5 times in a row is switched off (the reason is shown). Unattended runs at the "ask" autonomy level wait for you to approve.

## Security scanners (on demand)

Trivy and `osv-scanner` live in the optional `images/sandbox-security` image and run only when asked ("Scan project" or the agent tool), never automatically and never in the base image.

## MCP servers

Admins browse the official MCP Registry (`registry.modelcontextprotocol.io`) in **MCP servers**, review what a server runs as, the credentials it needs and its capabilities, then approve it. Approval is refused unless the version is pinned exactly, the transport is stdio and every required credential is mapped to an instance secret. Server details are re-fetched from the registry on approval: the browser only chooses the package and the secrets.

At the start of each agent run the bridge starts the approved servers (minus any the user switched off for that session), each in **its own gVisor container**: no workspace, no host mounts, only the credentials mapped to it, the sandbox egress policy, destroyed when the run ends. Their tools appear to the model as `mcp__<server>__<tool>`; output is untrusted (wrapped), arguments go through the same local-network guard as shell commands, and tools that are not declared read-only ask the user for approval.

