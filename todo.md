# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed. Tests use real services (see CONTRIBUTING.md); where one is not available the test says `NOT TESTED` rather than pretend.

## 1. Fixes and verification

- [ ] Test and document building images inside a sandbox (no claim without a passing test).
- [ ] **Cut the first release (Easy to Medium).** None exists yet, and the verified-update, roll-back and self-management items need one. Run the release workflow (images, binaries, signatures, SBOMs) for a first tag and fix what it finds.
- [ ] **Accessibility and mobile checks for every screen, with a skill to keep them in check (Medium).** Only the sign-in pages are in the axe flows today. Add Playwright and axe flows for the chat, Files, attachments, slash menu, task list, settings and admin pages on a phone and desktop width in light and dark, and extend the existing `.claude/skills/check-the-web-app` skill with a checklist for a new screen (contrast, focus order, labels, keyboard use, screen-reader text), with the ship-a-feature skill requiring it, so each new feature is checked before it ships.

- [ ] **Master key check at start-up (Easy).** A wrong master key is not noticed at start-up: secrets just fail to decrypt. Store a small encrypted canary and have `syntaxis doctor` and the Health page report a key that cannot open it.
- [ ] **Per-user, per-level and per-group upload limits (Easy to Medium).** Uploads have a global switch, size and allowed kinds only. Add a daily upload total per user and let level and group policies lower or switch off uploads, like the other limits.
- [ ] **Restore on a clean machine (Medium).** The restore was drilled on the real host, in a temporary data directory and on spare ports, and by a test comparing every table. A full restore on a clean virtual machine (volumes, master key and the update service) still needs a host with virtualisation.

## 2. Agent and workflow

- [ ] More helper-model jobs. The helper (set per provider in the admin app) does session titles, summaries of long watch events and compaction of long conversations. Still on the session's own model: commit and pull-request messages and plan drafts. Compaction on the helper has no test because it needs a conversation longer than a model's window.

## 3. Not yet verified with the real thing

These work as far as a test can show, but the test needs something this project does not have. Each skips as `NOT TESTED` and is listed in the CI summary.

- [ ] Connections to real GitLab, Cloudflare, AWS, Azure, Google Cloud and IFTTT accounts, and a real GitHub token in CI (set the `SYNTAXIS_TEST_*` secrets). Only wrong-credential behaviour and the SigV4 signing test vector are checked today.
- [ ] The pull-request flow end to end: the agent pushes a branch from a sandbox and a pull request is opened. Sandboxes cannot reach a private forge by design, so this needs a public test repository.
- [ ] SMTP login and STARTTLS against a real relay.
- [ ] The llama.cpp and LiteLLM adapters against real servers.
- [ ] The installer on a full virtual machine per distribution (CI checks package installation and the fresh install on one image; this host has no hardware virtualisation).
- [ ] A real update through the admin app on a disposable Docker host: a published release (none exists yet) and the roll-back after a failed update. CI checks the update scripts' plans, the service protocol against the real server, and a real update to the development branch on a fresh install.
- [ ] Parallel attempts with real model runs side by side, and what "keep this one" should do with the kept branch (today it removes the others and leaves pushing to the user).
- [ ] Web search against a real SearXNG server (the parser is tested; the live test needs `SYNTAXIS_TEST_SEARXNG_URL`).
- [ ] Pictures sent to a vision model through the llama.cpp and LiteLLM adapters (checked with Ollama's native and OpenAI-compatible APIs only), and OCR of languages other than English.
- [ ] The new web screens (Files tab, attachments, slash commands, task list, Notes, Commands and Hooks settings, API tokens, the repository hooks banner, the admin Policies upload switches and the admin user settings dialog) in the Playwright flows against a running instance: they pass type checks, lint, unit tests and the bundle budget, and the server behind each one is tested against a real sandbox, but no browser flow covers them yet.

- [ ] **Prompt injection with a model that falls for it.** `TestLivePromptInjection` (opt-in) plants hostile text in a repository file, an attached PDF and command output and checks what happened in a real sandbox. The one model tried (a 27B) resisted all three, so approvals, hook trust and secret redaction were not exercised end to end by a real attempt; they are covered by their own tests. Run it on a weaker or older model that attempts the attacks.
- [ ] **Prompt injection through a hostile web page.** The end-to-end test with a real model (`TestLivePromptInjection`, opt-in) covers a repository file, an attached PDF and command output. A page read with `fetch_url` is not covered: the tool refuses loopback and the local network, so the page has to be public.

## 4. Features

Ordered by priority within each tier. Effort is relative to the existing code: **Easy** is under a day, **Medium** is days, **Hard** is weeks. Tier 1 is what is needed to develop Syntaxis with Syntaxis; tier 2 is the largest everyday gap against Claude Code and Jules; tier 3 is polish and scale.

### Tier 1: develop Syntaxis with Syntaxis

- [ ] **Issues and pull-request comments start sessions (Medium).** The Jules core loop. Add automation event kinds for an issue labelled, a pull-request review comment and an `@syntaxis` mention, and let a session continue on an existing pull-request branch. Webhook automations and GitHub connections already exist.
- [ ] **CI feedback loop (Medium).** After a push the agent reads check runs and failure logs through the user's connection (`ci_status` and `ci_logs` tools), then fixes and pushes again. Needed because Syntaxis's own integration tests need real Docker and gVisor, which a sandbox cannot nest: unit tests run in the sandbox, integration tests in CI.
- [ ] **Self-management setup (Easy to configure, Medium to verify).** A "Syntaxis development" environment image, and a rule that the agent may open pull requests to the repository but never trigger the host update itself. Depends on the verified update roll-back in section 3.
- [ ] **A stable, documented API (Medium).** The chat app's `/api` is internal and changes freely. Publish a versioned, documented subset (an OpenAPI file, tested against the real server) for sessions, messages, events, approvals and changes, with a rule for deprecations.
- [ ] **Outbound webhooks and email for session events (Medium).** A general "finished", "needs approval" and "failed" notification to a URL or an email address the user sets, signed, with retries. Automations can call IFTTT today, but nothing else, and push notifications and chat integration both build on this.

### Tier 2: everyday gaps against Claude Code

- [ ] **Syntax highlighting in the file editor (Easy to Medium).** The Files tab edits in a plain text box. A code editor component (CodeMirror) loaded only when a file is opened would add highlighting, indentation and search within the bundle budget.
- [ ] **Admin switches for repository content (Easy).** `repo_skills` and `repo_hooks` (a repository's skills, slash commands and hooks) have no field in the admin app yet.
- [ ] **Branching with uncommitted work (Easy to Medium).** A branched session on a repository starts at the old branch's last commit; changes not yet committed stay with the old session.
- [ ] **Better attachments (Medium).** Attachments are uploaded one file at a time and lost from the model's view after two messages; add upload progress, previews of pictures in the chat, a way to attach a file already in the workspace, and an OCR language setting.
- [ ] **Configurable workspace size and attachment clean-up (Easy to Medium).** The 5 GiB workspace limit is fixed. Make it an admin setting (instance default, and per user, level and group), and remove old files from `.attachments` after a number of days the admin sets.
- [ ] **Per-repository agent settings and `syntaxis init` (Medium).** Rules, skills, commands, hooks and notes now live in separate places. Add one page per repository that shows what applies, and a command or button that writes a starter `syntaxis.md`, commands and hooks into the repository.
- [ ] **Cost and usage per session (Easy to Medium).** Show tokens, model time and sandbox time for each session, and a rough per-day total, so a user can see what a task cost.
- [ ] **Undo for edits made in the Files tab (Easy).** Take a checkpoint before saving, uploading over or deleting a file from the Files tab, as the agent's turns already do, and offer to undo from the Checkpoints tab.

### Tier 3: polish and scale

- [ ] **Per-role model routing (Medium).** A planner model and a coder model, and fallback when a model is unavailable. Check what `internal/routing` already covers first.
- [ ] **Push notifications (Medium).** Web Push for "finished" and "needs approval", since the chat app is already a PWA.
- [ ] **Agent-level evaluations (Medium).** Extend the model benchmark to task suites run against real repositories, to choose models for agent work.
- [ ] **Team-shared project library (Medium).** Skills, rules, commands and memory shared across the trusted users, and hand-off of a session to another user.
- [ ] **Voice input (Easy, low value).** Browser speech recognition in the message box.

### Tier 4: reach and platforms

- [ ] **Sandbox web browser for developing web apps (Medium to Hard).** View a dev server running in the sandbox and see the agent's changes live: a preview tab in the app (the authenticated preview proxy exists) with reload on change, and optionally a headless browser in the sandbox that takes screenshots for the user (and, with image input, for a vision model) and shows console errors.
- [ ] **Choice of sandbox runtime (Medium).** gVisor is the only runtime today. Make the runtime a setting (gVisor, Kata Containers, Firecracker, plain runc with a clear warning) with a health check per runtime and tests for each against real hosts.
- [ ] **An alternative to nftables (Medium).** The egress firewall needs nftables. Support iptables, and firewalld or a Docker-network-only mode, behind one interface, and say in the health page which is in use.
- [ ] **macOS and Windows support (Hard).** Today Linux only, because gVisor needs it. Options: run the whole stack in a Linux VM (Docker Desktop, WSL2, Colima) with an installer for each, then test the sandbox, firewall and update path there. Depends on the runtime choice and the firewall alternative above.
- [ ] **Chat integration (Medium).** Start and follow sessions from Slack, Microsoft Teams, Discord or Matrix: a message or `@syntaxis` mention starts a session, replies and approval requests come back in the thread, using each person's own Syntaxis account.
- [ ] **Generic guest invite (Medium).** One invite link, not tied to an email address, that lets anyone who has it create a limited account: expiry, use count, a default level and group with tight quotas, optional admin approval, and revocation. Opt-in, off by default (there are no sign-ups today).
- [ ] **Tailscale and Cloudflare Tunnel inside the app (Medium to Hard).** Share the instance without opening ports or running a reverse proxy: set up and show the status of a Tailscale node (Serve or Funnel) or a Cloudflare Tunnel from the admin app, with the secure-cookie, base URL, trusted-proxy and `SYNTAXIS_PROXY_AUTH` settings adjusted to match, and a health check for each. Must not weaken the rule that the admin app is never on the public address.
- [ ] **Syntaxis as an MCP server (Medium, scope to be decided).** Expose Syntaxis to other tools: start a session, send a message, follow progress, answer approvals and read changes, authenticated as the user (a per-user token) and bound by the same quotas, approvals and audit as the apps. Decide first which tools to expose and whether it is a separate listener or a route on the chat app.
- [ ] **VS Code extension (unknown scope, needs a design pass).** Capabilities not yet decided. Candidates: a sessions view, starting a session on the open repository and branch, showing the agent's changes as a diff in the editor, approvals and questions as notifications, and sending the selection or a file as context. Likely built on the same API as the MCP server above, so decide the two together.
- [ ] **Move away from Bash scripts (Medium to Hard).** About 940 lines of shell (`scripts/install.sh`, `update.sh`, `update-agent.sh`, `apply-egress.sh`, `firewall-sync.sh`, `install-gvisor.sh`, `build-templates.sh` and others) do the install, update, roll-back and firewall work, and are hard to test and to port to macOS and Windows. Move that logic into the Go binary as subcommands (`syntaxis install`, `update`, `firewall apply`), with real-host tests, keeping a tiny bootstrap script that only fetches and runs the binary. Do the firewall and update paths first; it is also a prerequisite for the macOS and Windows item.

