# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed.

## 1. Fixes and verification

- [ ] CI fully green, including the gVisor integration job, on every push.
- [ ] Test and document building images inside a sandbox (no claim without a passing test).
- [ ] Lighthouse audit (PWA, accessibility, performance) run in CI against a started instance.
- [ ] Probe tool-calling support live rather than inferring it; probe proxy response buffering live.

## 2. Access model

- [ ] User levels: `user`, `poweruser` and `admin`. Define what a poweruser may do beyond a user (for example higher limits, terminal, MCP servers, cloud tooling), and make it configurable.
- [ ] Admins get a link in the chat app to switch into admin mode, and a link back, instead of two separate logins to remember. Keep the separate admin listener, cookie and 2FA requirement.
- [ ] User groups. Permissions and settings can be set per group as well as per user: quotas, allowed models, egress mode, terminal, cloud tooling, MCP servers, session limits, retention. Document how group and user settings combine (most specific wins; an explicit user setting overrides the group).

## 3. GitHub integration, per user

- [ ] Each user connects their own GitHub account (GitHub App or OAuth, with a fine-grained token as the fallback), stored in their own secrets and never visible to other users or the model.
- [ ] Start a session from a repository: pick one from the user's repositories or create a new one (name, visibility, description) from the UI.
- [ ] The workspace is a clone of the chosen repository on a working branch; the agent can commit, push and open a pull request; the UI shows the branch, the diff and the pull request link.
- [ ] Repository list, search and a test-connection button; clear errors for missing scopes and expired tokens.
- [ ] Start from an issue or pull request; respond to review comments on a pull request the agent opened.

## 4. Per-user connections to other services

Every connection belongs to one user: their own account and credentials, kept in their own vault, available only to their own sessions, never shared across the server and never shown to the model. Admins cannot see them.

- [ ] A connections page where a user adds, tests and removes a connection, with a short guide for each service on creating a least-privilege, preferably short-lived credential, and clear errors for expired or under-scoped ones.
- [ ] GitLab (including self-hosted) and Gitea: repository pick or create, clone, branch, push and merge request, the same as GitHub.
- [ ] Cloudflare: API token with the user's own zones and Workers.
- [ ] Google Cloud: the user's own project, with a service account key or short-lived credentials, working with the `gcloud` CLI and client libraries in the sandbox.
- [ ] AWS: the user's own account or role, with temporary credentials, working with the `aws` CLI and SDKs.
- [ ] Azure: the user's own subscription and service principal, working with the `az` CLI and SDKs.
- [ ] The existing approval gate covers deploys, deletes and anything that costs money on any of these; show which connection and project an action would touch before the user approves it.

## 5. Agent and workflow

Compared with Jules and Claude Code, these are missing or weaker (background runs, approvals, project instructions, skills, MCP, checkpoints and usage visibility already exist):

- [ ] Plan mode: the agent proposes a plan the user approves or edits before it changes files.
- [ ] Parallel tasks: several runs of one task (or several tasks) at once, each in its own sandbox, with a side-by-side comparison.
- [ ] Diff review in the UI before a push: per-file diff, comments that go back to the agent.

- [ ] Scheduled runs and triggers: cron schedules, webhooks and GitHub events start a run with a stored prompt, with the same quotas and approvals, and results visible in the session list.
- [ ] `devcontainer.json` support and per-project image templates.
- [ ] Prompt presets.
- [ ] Read-only session sharing.
- [ ] Checkpoints: timeline and diff viewer in the UI, apply or discard hunks before a push, restore the conversation as well as the files (fork, with the abandoned branch kept), per-user disk budget.
- [ ] Wire helper-model routing into the agent for titles, summaries and compaction (the decision logic exists; off by default; never load or swap a model mid-session).
- [ ] An `AgentAdapter` interface with an experimental adapter that runs another agent inside the sandbox, reaching the model only through a Syntaxis-hosted endpoint with a per-session token.
- [ ] Side panel tabs for files and a dev-server preview.
- [ ] Optional terminal recording.

## 6. Operations

- [ ] LLM server discovery in the installer and admin app: detect a server on the host or LAN, walk the admin through connecting it, and prefill concurrency and context settings from what it reports.
- [ ] Run the Playwright and accessibility (axe-core) suites in CI against the instance the fresh-install job creates; add a Lighthouse PWA check.
- [ ] Workspace size cap enforced by a filesystem quota. Today the size is measured every minute, a warning is shown at the limit and the sandbox is stopped when far over it.
