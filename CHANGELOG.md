# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/); versions follow semver once 1.0 is tagged. The running version is shown in the apps (Settings, admin header), by `syntaxis version` and at `/api/version`.

## [Unreleased]

### Added
- One-command installer (`scripts/install.sh`): checks prerequisites, installs gVisor if missing, writes `.env` with a fresh master key, builds or pulls version-pinned images, starts the stack, installs systemd units that keep the egress firewall applied (boot, Docker start, `.env` changes, every 15 minutes) and prints a single-use admin setup link. Optional `--https <domain>` adds a Caddy front end with automatic certificates; `--auto-update` adds a weekly update timer.
- `scripts/update.sh`: database backup, update, health check against the expected version, and automatic rollback of image, database and checkout on failure.
- CI installs from scratch on a clean machine with real gVisor and checks the firewall, the Docker proxy and idempotent re-runs.
- `syntaxis admin setup-link <email> <name>` creates an admin and prints a single-use link (valid 24 hours) to choose the password in the browser; the admin app opens it from `#setup=…`.
- **Security:** a validating Docker API proxy (`syntaxis docker-proxy`) replaces the generic socket proxy in Compose. Only hardened gVisor sandboxes can be created or started through it; privileged containers, host mounts, host namespaces, devices, published ports and unused endpoints are refused. The admin Health page warns when a different proxy is in front of Docker.
- Interactive terminal tab in the session view, running in the session's own sandbox, with an admin switch per user.
- Live progress line while a run is working: waiting, connecting, loading the model, reading, thinking, replying, running a command.
- CPU metering with a daily compute allowance, a Resources tab (typical and busiest-10% CPU, memory), and throttling, pausing and stopping of sustained load.
- Copy buttons on code blocks and command output.
- Build information (version, commit, date) in the binary, the apps, `/healthz` and `/api/version`.
- MCP servers, each in its own gVisor sandbox, with registry browsing and admin approval.

- Per-user egress modes and allowlisted hostnames are enforced by the host firewall, every new connection from a sandbox is logged (allowed or denied), and the workspace size is measured and enforced by the watchdog.

- Per-user connections to GitHub, GitLab, Gitea, Cloudflare, AWS, Azure and Google Cloud, checked when saved and kept in the user's own vault. Sessions can start from one of the user's repositories (or a new one): the agent works in a clone on its own branch and opens pull requests with the user's own connection. Approval requests name the connection a command acts through.

- Per-user MCP servers: an admin approves servers (pinned, each in its own gVisor sandbox) and suggests them; each user switches on the ones they want, and a server only ever gets that user's own connected credentials. The MCP client is the official Go SDK.
- Replies render as Markdown with highlighted code, and diffs in a themed diff viewer (a **Changes** tab per session), both following the app's light and dark themes and loaded only when needed.
- Schedules use `robfig/cron` and feeds use `gofeed`, replacing hand-written parsers.
- Core agent: tool loop with approval levels (ask, workspace, full), tolerant tool-call parsing, context budgeting against the model's real window with automatic summaries, rules and skills files, shadow-git checkpoints, secret scanning of model output, and server-side SSH commit signing.
- Providers: Ollama, llama.cpp, LiteLLM and OpenAI-compatible adapters with model discovery that starts models disabled, a fair queue per provider, token and rate quotas, and an admin model benchmark with a hard multi-language suite.
- Admin app: health findings with fixes, users, providers and models, audit log, egress, policies and a kill switch; chat app is an installable PWA that works offline for the shell.
- Workspace retention (warn, then delete, with a keep flag) and an authenticated preview proxy for a server running in a sandbox.
- Tests use real services only: real Docker and gVisor, Gitea and Mailpit containers, the public internet, and a real local model. What cannot be tested skips as `NOT TESTED: <reason>` and CI lists it in the job summary (`scripts/not-tested.sh`). The fake container runtime, scripted model and fake GitHub, GitLab, IFTTT and registry servers are gone, along with the test-only hooks that supported them.
- CI: Lighthouse (accessibility, best practices, SEO, performance), axe accessibility checks in Playwright, a bundle-size budget, actionlint, hadolint, a documentation link check and dependency review on pull requests.
- Environments: admin-approved sandbox images that a user can pick for a session, and that a repository's `devcontainer.json` is matched to (image, `containerEnv` and a once-only `postCreateCommand`). A repository cannot name an image of its own. Example Rust and Java images in `images/templates`. Tested with a real image under gVisor, a real Gitea, and a real GitHub repository's `npm install`.
- Helper model per provider (Providers & models): session titles, summaries of long watch events and conversation compaction run on a small model when it is loaded, and on the session's own model otherwise. Checked against a real Ollama with real model loading.
- Live probes in the admin app (Providers & models → Probe): whether a model really calls a tool when asked, and whether the provider's stream arrives live or is held back by a proxy. The tool result decides whether agent mode is offered; a buffered stream is a health warning. Checked against a real model and a real nginx that buffers.
- Parallel attempts: start 2 or more runs of one task, each in its own clone and branch, and compare their changes side by side before keeping one. The number offered comes from the machine (CPUs and memory against the sandbox limits, read from Docker), the user's session limit and the model server's concurrency, and says whether the model is loaded.

- A full workspace refuses new messages and stops a running agent at its next step, with a message to free space; the terminal stays available to clean up and the next message re-measures at once.

- Automations: runs started by a schedule, a webhook (IFTTT, GitHub, scripts) or a watch of a GitHub release, a git branch or tags, a Docker Hub image, an npm, PyPI or crates.io package, a feed or a web page. Runs use the owner's limits; event data reaches the agent as untrusted information; an IFTTT applet can be triggered when a run finishes.

- Plan first: the agent can only look until the user approves (or edits, or sends back) its plan. A Changes tab shows the agent's changes file by file, with comments that go back to the agent.

- Shared session links: a named, read-only, public link that expires after hours you choose and can be revoked; per-user MCP servers using each user's own credentials; replies rendered as Markdown with highlighted code and a themed diff viewer.

- Checkpoints tab: a timeline with what changed since each checkpoint, and restore of the files or of the files and the conversation, tested against a real gVisor sandbox.

### Changed
- User levels (user, power user, admin) and user groups. Levels and groups carry the same permissions and settings as a user; the most specific setting wins, and an on/off permission stays off if any layer turns it off. Admins get an "Admin mode" link from the chat app and a link back.
- The installer installs Docker, nftables and gVisor when missing, on Debian, Ubuntu, Raspberry Pi OS, AlmaLinux, Rocky Linux, RHEL and Fedora (x86-64 and arm64); `--check` shows the plan. Package installation is tested on each in CI.
- Frontend toolchain: React 19, Vite 8, Vitest 5, ESLint 10 (Node 22 or newer is needed to build the web apps).
- Base images are pinned by digest; tagged releases sign images and checksums with Sigstore keyless signing and attach SBOMs, with release notes taken from this changelog.
- The sandbox network name, subnet and resolver address and the server image are configurable in Compose.
- Getting started now uses the installer; the manual steps remain documented.
- The health check verifies the egress firewall by behaviour (a short-lived sandbox tries to reach the Docker host) instead of trusting `SYNTAXIS_NETWORK_RULES_APPLIED`, so a missing or outdated ruleset is reported.
- Secrets are read fresh for every command, so adding, changing or removing one applies immediately instead of needing a new session.
- Simpler getting started guide and README.

### Fixed
- **Security:** a user could aim the server at internal addresses through the address of a self-hosted GitLab or Gitea (the server's own admin app on loopback, the Docker proxy, the LLM server, the LAN), and read part of the reply in the error. Connections now go through the same address-checking client as watches, addresses with credentials, queries or fragments are refused, and LAN forges need `SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE`.
- **Security:** MCP servers, which now run with a user's own credentials, ran under the instance-wide egress mode instead of that user's; a user limited to `allowlist` or `none` could reach the internet through one. They now carry the user's id and egress mode.
- **Security:** the firewall sync service passed its token on the `curl` command line, where any local user could read it; it now uses a header file only root can read. The installer no longer writes a predictable file in `/tmp` as root.
- A compute allowance set on a level or group (not on the user) was not enforced by the sandbox monitor.
- Webhook and watch text can no longer close the block that marks it as outside data.
- Removing a container that is already being removed waits for it instead of failing.
- The Docker proxy lists only Syntaxis containers, so a compromised server cannot see what else runs on the host.
- A sandbox that was paused for sustained load and then resumed was judged on its old load; it now gets a fresh start, and is stopped if it overloads again.
- Starting a session or terminal is refused while the daily compute allowance is used up.
- **The egress ruleset could disrupt other applications on the host:** its IPv6 rule was not limited to sandbox traffic, so forwarded IPv6 to unique-local or link-local addresses was dropped for every container and VPN on the host. Every rule now matches only the sandbox subnet or the sandbox bridge (IPv6 from the bridge is dropped; the sandbox network is IPv4 only), a test enforces that, and the real-firewall test checks that an ordinary container still reaches host services. Re-apply with `scripts/apply-egress.sh`.
- Accessibility and layout (browser review on phone and desktop, light and dark, with axe-core): dark-mode danger buttons were 2.8:1 contrast, the admin header overflowed on phones and most admin tabs were out of sight, and the chat home had three identical calls to action with phone-only wording.
- The admin exposure health check reported the Compose deployment as public; it now uses the published bind address.
- **Security:** sandboxes could open connections to services listening on the Docker host itself (via the bridge gateway or the host's LAN address), because that traffic takes the input path, which the egress rules did not filter. The ruleset now has an input chain; re-apply it with `scripts/apply-egress.sh`.
- Sandboxes on the same network could reach each other on hosts without `br_netfilter` loaded; the egress rules now also filter at the bridge.
- Chat jitter while a reply streamed (only the chat log scrolls now).
- Admin Benchmarks and MCP tabs were not reachable from the navigation.
- Sandbox DNS on Docker releases that cannot mount a single file from a volume subpath.
- Several accessibility issues: badge contrast, heading order, keyboard-focusable scroll areas, icons that failed to render.

## Initial development

Authentication with separate admin and chat accounts, provider adapters, health checks, fair LLM queue, quotas, gVisor sandbox manager, egress policy and DNS resolver, built-in agent with approval gates and context management, checkpoints, rules and skills, secrets vault, secret scanner, SSH commit signing, admin and chat web apps (PWA), CLI, CI and release workflows.
