# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/); versions follow semver once 1.0 is tagged. The running version is shown in the apps (Settings, admin header), by `syntaxis version` and at `/api/version`.

## [Unreleased]

## [0.2.1] - 2026-11-25

### Changed
- Release images and checksums are no longer signed with cosign. Releases still carry an SBOM, build provenance and a `SHA256SUMS` file.
- The test VM harness runs inside a Docker container (`test/vm/vm.sh`); the host needs only Docker and KVM. `test/vm/vm.sh all.sh` runs every VM test in one go.
- The Windows ISO is mounted read-only (`SYX_ISO_DIR`) instead of being copied, and is needed only for the first Windows install.
- The documentation is rewritten as instructions.

## [0.2.0] - 2026-11-24

### Upgrading from 0.1.3
- The host update service (`syntaxis-update-agent`) keeps running the old version of its script until it is restarted, and the old one cannot be told which release stream to follow. Updating from 0.1.3 works as before; afterwards run `sudo systemctl restart syntaxis-update-agent` once. The Updates page says so if a request needs it, and later updates restart the service themselves.
- `SYNTAXIS_VAPID_PUBLIC` and `SYNTAXIS_VAPID_PRIVATE` are no longer read; remove them if you set them. A value you save in Configuration now replaces the one Docker gives, so check the screen before relying on a change in the compose file.
- The weekly automatic update (`--auto-update`) now stays inside your release stream and never goes backwards.

### Added
- **Release streams.** Updates now has three streams: **stable** (the newest release that is not a pre-release, the default), **preview** (the newest release of any kind, release candidates and betas included) and **edge** (the main branch built from source). The admin app shows which one you follow and what each would install, and switching warns when it would go to an older version. `scripts/update.sh` takes `--stream stable|preview|edge`; the stream is kept as `SYNTAXIS_CHANNEL` in `.env`, and the weekly automatic update stays inside it and never goes backwards.
- **Configuration** screen in the admin app: every option Syntaxis has, including email (SMTP, with a test button), addresses, sandbox lifetimes, egress defaults, reverse-proxy sign-in and logging. Each option says whether its value comes from **Docker**, was **saved here** (a saved value replaces the environment's) or is the **default**, whether it applies at once or after a restart, and can go back to the Docker value. Options that belong to the deployment are shown but not edited, and secrets are never shown. The mail password is kept encrypted under a reserved name that is never offered to MCP servers.
- **Admins** screen: list the admin accounts with their state (2FA on, no 2FA, invited, disabled), add another admin, give a locked-out one a new single-use link, disable, enable or remove an account. You cannot reset, disable or remove yourself. The health finding "admin accounts have no 2FA" now names the accounts and tells a missing 2FA from an unopened invitation, and Health refreshes when accounts or settings change instead of up to five minutes later.
- **Audit log proof.** The Audit tab explains what "verified" means, shows how many entries were checked and the fingerprint of the newest one, links to the documentation, and downloads the whole log. `syntaxis audit verify` checks a download (or the live database) on another machine. The documentation says plainly what the chain cannot detect and how to cover it.
- **QR code for two-factor setup**, drawn by the server (nothing is sent to a third party), with the typed key kept for when scanning is not possible.
- MCP servers shipped as container images (the official GitHub server is one) can now be approved: they are pinned by the exact version in the image tag, the review shows the `docker pull` command, and approval waits until the image is on the Docker host. The review warns that a tag is weaker than a digest.
- Example text on the admin forms (environments, providers, invites, quotas, credentials).
- `syntaxis audit verify`, and the VM test suite (`test/vm/suite.sh`) that installs Syntaxis with the installer on Debian 12 and 13, Ubuntu 24.04, Rocky 9 and AlmaLinux 9 and runs the sandbox, proxy, egress and doctor tests; `vmctl.sh win-template` for Windows test VMs.

### Changed
- The admin menu is in a sensible order (Health, people, models, workspace, policy and security, system, Kill switch last). The Policies screen keeps the policies that are only set there; options that can also be set in Docker moved to Configuration.
- The **MCP servers** screen is a guided page: what MCP servers are and three steps, then what is approved now, then adding one (recommended or searched), then shared credentials. You can make a shared credential while approving.
- Every dialog has a visible Cancel or Close button, not only Escape or a click outside.
- The Policies and Configuration forms show the value in effect (saved, or from Docker) instead of the built-in default.
- The update service reloads itself after an update, so the next request is read by the version that is running. A request names the stream only when the target does not already imply it, so an older service keeps working.
- The test email button gives up after 30 seconds and says whether the mail was sent or only logged.
- The Environments, Updates and Providers screens say what is needed when no host update service runs (for example under another deployment tool), and the wording about models is accurate: Syntaxis never downloads or deletes models, and asks the server to load one only when a session asks for it by name.

### Removed
- Push notifications and VAPID keys: `SYNTAXIS_VAPID_PUBLIC` and `_PRIVATE` are ignored, `syntaxis keygen --vapid` is gone, the unused `push_subscriptions` table is dropped, and so are the browser push code and the "no VAPID keys" health note. Nothing ever sent a push.

### Fixed
- An update's result is no longer lost when it restarts Syntaxis. The host service keeps trying to deliver it for a couple of minutes, and a server that never heard how a run went says "unknown" and what to check, instead of showing "running" for ever. Found by upgrading a real install in a VM: the rollback after a failed update worked, but the page never said so.
- A failed look-up of the releases (GitHub limiting the address) is retried after a minute instead of being remembered for an hour, and a stream request says that the look-up failed instead of "no release yet".
- The official GitHub MCP server, the first recommended one, could never be approved: it is a container image and its version is in the tag, which was read as "not pinned".
- A real-GitHub test (`TestGitHubReleases`) failed the build whenever GitHub rate-limited the CI runner; it now reports "not tested" like the others.
- The admin app went blank when you pressed Review on an MCP server with no environment variables (many in the registry): the catalogue search sent `null` where the page expected a list. The search now always sends lists and the page copes with either.

## [0.1.3] - 2026-11-23

### Added
- `SYNTAXIS_BOOTSTRAP_ADMIN_ALLOW_SHORT_PASSWORD` lets the first admin made from the environment start with a 6-character password instead of 12. Off by default; the other password rules still apply.

### Added
- `test/vm/`: disposable QEMU/KVM test VMs (Debian 12 and 13, Ubuntu 24.04, Rocky 9, AlmaLinux 9) for escape and destructive tests. Base images are fetched and verified from the vendors, VMs are rebuilt in seconds, and the default network is fully isolated from the host and LAN. Windows 11 installs unattended from a Microsoft ISO (UEFI, Secure Boot, software TPM; no product key). `vmctl.sh win-template` saves an installed Windows VM so new ones start from it in about a minute. `suite.sh` installs Syntaxis with `scripts/install.sh` on every distro and runs the sandbox, Docker proxy, egress and doctor tests against real Docker and gVisor. No images or ISOs are stored in the repository.

## [0.1.2] - 2026-11-23

### Added
- `SYNTAXIS_BOOTSTRAP_PROVIDER_URL` (with `_NAME`, `_KIND`, `_CONCURRENCY`, `_ENABLE_MODELS`) adds a model provider on a brand-new instance and switches on its discovered chat models, so a rebuilt host comes up ready to use. It never touches an instance that already has a provider.

### Fixed
- **Security:** a sandbox could reach every port of the Syntaxis container on the sandbox network, including the chat and admin listeners, because the layer-2 firewall rule let all traffic to the resolver's address through. It now allows only DNS, plus the replies to connections the server opened itself. If you installed 0.1.1, update and let the firewall be re-applied (`sudo systemctl restart syntaxis-egress`).
- `apply-egress.sh` no longer hands the whole `.env` (with the master key and passwords) to the throwaway container that prints the rules; it passes only the egress and sandbox settings.
- The firewall sync service's once-a-second polls are no longer written to the log.

## [0.1.1] - 2026-11-22

The first release. Syntaxis is pre-1.0: expect changes, and read [security](docs/security.md) for the honest limits. The `v0.1.0` tag was cut first, but its release run failed building the cloud sandbox image (the OpenTofu installer's gpg had no home directory), so no release was made from it; 0.1.1 has the same changes and the fix.

### Added
- **API tokens:** per-user, hashed, revocable tokens with a `read` or `run` scope and an optional expiry, made under Settings → API tokens and listed in the new [API](docs/api.md) page. A token can never reach secrets, connections, settings, hooks, the terminal, files-writing or other tokens, and a test fails if a new route is open to tokens by accident.
- **Uploads are checked and off by default.** An admin turns them on, sets the largest file and which kinds are allowed. Only pictures, PDFs, office documents and plain text pass, judged by content: programs, scripts, archives, macros, embedded files, data hidden after a picture, encoded text, and PDFs with scripts, launch actions, embedded files or encodings that hide content are refused and audited.
- **Admin review of user settings:** the admin app shows and edits each user's notes, commands, hooks, connections (names only), MCP choices, automations and tokens, and can read any session's messages. Changes are audited.
- **Repository hooks need trust:** a repository's `.syntaxis/hooks.json` runs only after the user has read it and trusted that exact file for that repository. Hooks together get a 5 minute budget per run, and their time shows on the Resources tab.
- `syntaxis backup` now opens the finished backup, runs SQLite's integrity check and compares its tables with the live database. The restore was drilled on a real host and with a test that compares every table.
- **Editing:** `apply_patch` makes several edits to one file, or applies a unified diff, all or nothing (line numbers in `@@` lines are ignored; hunks are found by content). `patch_file` and `apply_patch` now tolerate differences in indentation and spacing and re-indent the replacement to the file, never guess when a match is ambiguous, and show the closest text in the file when nothing matches.
- **Task list:** a `todo_write` tool and a progress list above the message box, restored after a reload.
- **Files tab:** browse the session's workspace, view and edit text files, download, upload, create folders and delete, through the session's sandbox and confined to `/workspace`; changes are refused while the agent is working.
- **Attachments:** attach files, pictures and PDFs to a message (paperclip, drag and drop, paste). Pictures are sent to models that can see them (Ollama reports this itself; the admin app lists others), and `read_file` reads PDFs (text layer, or OCR for scans) and pictures (OCR) inside the sandbox. The default sandbox image now contains poppler-utils and Tesseract (English).
- **Notes:** the agent can save short notes that it sees in every later session (`remember`, `forget`), per user and per repository; each is approved by the user first, scanned for secrets and listed in **Settings → Notes**.
- **Web search:** a `web_search` tool through a SearXNG address set in the admin app; queries with secrets are refused and results are untrusted.
- **Slash commands and hooks:** saved prompts (`/review`, `/test`, `/explain`, your own in Settings, a repository's in `.syntaxis/commands/`) and commands that run before or after tool calls (Settings, or `.syntaxis/hooks.json`). A hook that would need approval, or that the policy blocks, is never run.
- **Edit and resend, regenerate, branch:** change one of your messages and run again from there, regenerate the last reply, or start a new session from a reply (on a repository it gets its own working branch).
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
- Invites show their link: after **Invite user** (and **Resend**) the admin app displays the single-use link with a Copy button, so it can be sent by hand when email is not set up.
- Updates from the admin app: an **Updates** tab (and an "Update available" button when a release is newer) to update to the latest release, switch to any other release, or follow the development branch. A service on the host (`syntaxis-update-agent`, installed by default) runs `scripts/update.sh`, which now has `--release [version]`, `--edge` and `--dry-run`, always backs up first and rolls back automatically if the new version is not healthy.
- Tests use real services only: real Docker and gVisor, Gitea and Mailpit containers, the public internet, and a real local model. What cannot be tested skips as `NOT TESTED: <reason>` and CI lists it in the job summary (`scripts/not-tested.sh`). The fake container runtime, scripted model and fake GitHub, GitLab, IFTTT and registry servers are gone, along with the test-only hooks that supported them.
- CI: Lighthouse (accessibility, best practices, SEO, performance), axe accessibility checks in Playwright, a bundle-size budget, actionlint, hadolint, a documentation link check and dependency review on pull requests.
- Environments: admin-approved sandbox images that a user can pick for a session, and that a repository's `devcontainer.json` is matched to (image, `containerEnv` and a once-only `postCreateCommand`). A repository cannot name an image of its own. Example images in `images/templates` for Rust, Go tooling, TypeScript, Kotlin, Swift, Haskell, Zig, a headless-Chromium browser image, Java, .NET, Ruby, PHP, Elixir, C/C++ and a Python data-science stack, built only when an admin chooses them (Environments → Build, with a warning about time and disk) or with `scripts/build-templates.sh`, and removed again after 30 unused days (configurable); each is checked to build and run under gVisor. Tested with a real image under gVisor, a real Gitea, and a real GitHub repository's `npm install`.
- Helper model per provider (Providers & models): session titles, summaries of long watch events and conversation compaction run on a small model when it is loaded, and on the session's own model otherwise. Checked against a real Ollama with real model loading.
- Live probes in the admin app (Providers & models → Probe): whether a model really calls a tool when asked, and whether the provider's stream arrives live or is held back by a proxy. The tool result decides whether agent mode is offered; a buffered stream is a health warning. Checked against a real model and a real nginx that buffers.
- Parallel attempts: start 2 or more runs of one task, each in its own clone and branch, and compare their changes side by side before keeping one. The number offered comes from the machine (CPUs and memory against the sandbox limits, read from Docker), the user's session limit and the model server's concurrency, and says whether the model is loaded.

- A full workspace refuses new messages and stops a running agent at its next step, with a message to free space; the terminal stays available to clean up and the next message re-measures at once.

- Automations: runs started by a schedule, a webhook (IFTTT, GitHub, scripts) or a watch of a GitHub release, a git branch or tags, a Docker Hub image, an npm, PyPI or crates.io package, a feed or a web page. Runs use the owner's limits; event data reaches the agent as untrusted information; an IFTTT applet can be triggered when a run finishes.

- Plan first: the agent can only look until the user approves (or edits, or sends back) its plan. A Changes tab shows the agent's changes file by file, with comments that go back to the agent.

- Shared session links: a named, read-only, public link that expires after hours you choose and can be revoked; per-user MCP servers using each user's own credentials; replies rendered as Markdown with highlighted code and a themed diff viewer.

- Checkpoints tab: a timeline with what changed since each checkpoint, and restore of the files or of the files and the conversation, tested against a real gVisor sandbox.

### Changed
- **Admins can read everything.** `SYNTAXIS_ALLOW_ADMIN_READ_CONTENT` is gone: admins always see session titles and can read messages (each read is audited). Stored credential values stay encrypted and are shown to nobody.
- The workspace size limit is measured more often as a workspace fills (every 10 seconds above half the limit, every 3 seconds above 90%), so a runaway writer is caught sooner.
- The server image runs as the numeric user `65532` (same user as before), which satisfies the newer hadolint.
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
- CI was red on every push: three tests wrote a slice from the agent's goroutine while the test read it, which `go test -race` reports as a data race, and the Go job's log did not say which tests failed. The tests are fixed and the job now prints failed tests. hadolint 3.5's numeric-user rule is met by writing the server image's user as `65532:65532`, and the TypeScript 7 bump (which `typescript-eslint` does not support yet) is held back.
- The monitor tests ran a watchdog over every sandbox on the Docker host, so they could pause or stop sandboxes that were not theirs, including another Syntaxis instance's. They now see only their own.
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
