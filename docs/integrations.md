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

## Security scanners (on demand)

Trivy and `osv-scanner` live in the optional `images/sandbox-security` image and run only when asked ("Scan project" or the agent tool), never automatically and never in the base image.

## MCP servers

Admins browse the official MCP Registry (`registry.modelcontextprotocol.io`) in **MCP servers**, review what a server runs as, the credentials it needs and its capabilities, then approve it. Approval is refused unless the version is pinned exactly, the transport is stdio and every required credential is mapped to an instance secret. Server details are re-fetched from the registry on approval: the browser only chooses the package and the secrets.

At the start of each agent run the bridge starts the approved servers (minus any the user switched off for that session), each in **its own gVisor container**: no workspace, no host mounts, only the credentials mapped to it, the sandbox egress policy, destroyed when the run ends. Their tools appear to the model as `mcp__<server>__<tool>`; output is untrusted (wrapped), arguments go through the same local-network guard as shell commands, and tools that are not declared read-only ask the user for approval.

