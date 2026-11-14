# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed.

## 1. Fixes and verification

- [ ] CI fully green, including the gVisor integration job, on every push.
- [ ] Verify on real gVisor: pause and stop of sustained CPU load, and the daily compute allowance cut-off.
- [ ] Test and document building images inside a sandbox (no claim without a passing test).
- [ ] Fresh-checkout `docker compose up` verified automatically in CI.
- [ ] Lighthouse audit (PWA, accessibility, performance) run in CI against a started instance.
- [ ] Probe tool-calling support live rather than inferring it; probe proxy response buffering live.
- [ ] Pin base image digests and publish an SBOM.

## 2. Access model

- [ ] User levels: `user`, `poweruser` and `admin`. Define what a poweruser may do beyond a user (for example higher limits, terminal, MCP servers, cloud tooling), and make it configurable.
- [ ] Admins get a link in the chat app to switch into admin mode, and a link back, instead of two separate logins to remember. Keep the separate admin listener, cookie and 2FA requirement.
- [ ] User groups. Permissions and settings can be set per group as well as per user: quotas, allowed models, egress mode, terminal, cloud tooling, MCP servers, session limits, retention. Document how group and user settings combine (most specific wins; an explicit user setting overrides the group).
- [ ] Optional trusted reverse-proxy header authentication (config and health check exist; the middleware is not wired).
- [ ] Admin IP allowlist.

## 3. GitHub integration, per user

- [ ] Each user connects their own GitHub account (GitHub App or OAuth, with a fine-grained token as the fallback), stored in their own secrets and never visible to other users or the model.
- [ ] Start a session from a repository: pick one from the user's repositories or create a new one (name, visibility, description) from the UI.
- [ ] The workspace is a clone of the chosen repository on a working branch; the agent can commit, push and open a pull request; the UI shows the branch, the diff and the pull request link.
- [ ] Repository list, search and a test-connection button; clear errors for missing scopes and expired tokens.
- [ ] Start from an issue or pull request; respond to review comments on a pull request the agent opened.
- [ ] Generic git over HTTPS and SSH deploy keys with a credential helper, for GitLab and Gitea.
- [ ] Registrar and Cloudflare connectors as plugins behind a generic "API token + base URL" connector.

## 4. Agent and workflow

- [ ] Scheduled runs and triggers: cron schedules, webhooks and GitHub events start a run with a stored prompt, with the same quotas and approvals, and results visible in the session list.
- [ ] `devcontainer.json` support and per-project image templates.
- [ ] Prompt presets.
- [ ] Read-only session sharing.
- [ ] Checkpoints: timeline and diff viewer in the UI, apply or discard hunks before a push, restore the conversation as well as the files (fork, with the abandoned branch kept), per-user disk budget.
- [ ] Wire helper-model routing into the agent for titles, summaries and compaction (the decision logic exists; off by default; never load or swap a model mid-session).
- [ ] An `AgentAdapter` interface with an experimental adapter that runs another agent inside the sandbox, reaching the model only through a Syntaxis-hosted endpoint with a per-session token.
- [ ] Side panel tabs for files and a dev-server preview.
- [ ] Optional terminal recording.

## 5. Operations

- [ ] Fully automated deployment: see the review prompt in [docs/review-prompt.md](docs/review-prompt.md).
- [ ] Admin log viewer beyond audit and egress; log retention and rotation settings.
- [ ] Connection-level egress logging (today only DNS queries are logged).
- [ ] Workspace size cap enforced by the filesystem, not only by the watchdog.
- [ ] Abuse alerts and admin notification emails.
- [ ] Maintenance banner.
- [ ] Usage reports.
- [ ] PostgreSQL backend, OIDC login and multi-instance provider fallback.
