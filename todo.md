# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed. Tests use real services (see CONTRIBUTING.md); where one is not available the test says `NOT TESTED` rather than pretend.

## 1. Fixes and verification

- [ ] Test and document building images inside a sandbox (no claim without a passing test).

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

## 4. Features

Ordered by priority within each tier. Effort is relative to the existing code: **Easy** is under a day, **Medium** is days, **Hard** is weeks. Tier 1 is what is needed to develop Syntaxis with Syntaxis; tier 2 is the largest everyday gap against Claude Code and Jules; tier 3 is polish and scale.

### Tier 1: develop Syntaxis with Syntaxis

- [ ] **Issues and pull-request comments start sessions (Medium).** The Jules core loop. Add automation event kinds for an issue labelled, a pull-request review comment and an `@syntaxis` mention, and let a session continue on an existing pull-request branch. Webhook automations and GitHub connections already exist.
- [ ] **CI feedback loop (Medium).** After a push the agent reads check runs and failure logs through the user's connection (`ci_status` and `ci_logs` tools), then fixes and pushes again. Needed because Syntaxis's own integration tests need real Docker and gVisor, which a sandbox cannot nest: unit tests run in the sandbox, integration tests in CI.
- [ ] **Multi-edit and unified-diff apply tool, with fuzzy matching (Easy).** `patch_file` takes one exact, unique replacement, which weak local models often get wrong on whitespace. Add `apply_patch` (multi-hunk), whitespace-tolerant matching and an error that shows the nearest match. Large gain for local models.
- [ ] **Task list tool shown in the UI (Easy).** A `todo_write` tool and a progress list in the chat. Keeps small models on track over long runs.
- [ ] **Self-management setup (Easy to configure, Medium to verify).** A "Syntaxis development" environment image, and a rule that the agent may open pull requests to the repository but never trigger the host update itself. Depends on the verified update roll-back in section 3.

### Tier 2: everyday gaps against Claude Code

- [ ] **File browser and editor in the web UI (Medium).** Tree, view, edit, download and upload, using a code editor component and file endpoints that run through the sandbox. Today there is only Output, Context, Changes, Checkpoints, Resources, Tools and the terminal.
- [ ] **Attachments and image input (Medium).** Upload files and screenshots into the workspace, and send images to vision-capable models. Needs content parts in the provider adapters and in message storage.
- [ ] **Web search tool (Easy to Medium).** Only `fetch_url` exists. Use a self-hosted SearXNG or a search API, reachable through the egress allowlist.
- [ ] **Edit, resend, regenerate and branch a conversation (Easy to Medium).** Checkpoint restore with conversation rewind exists; this adds the fork and the UI.
- [ ] **Repo map and code intelligence (Medium; language server Hard).** A tree-sitter or ctags outline tool and an automatic repo map in the prompt, for small-context models. Language servers (`gopls` is already in the Go image) are a later step.
- [ ] **Persistent memory (Easy).** Per-user and per-repository notes the agent reads and writes across sessions, with writes needing approval.
- [ ] **Hooks and custom slash commands (Easy to Medium).** Commands that run before or after a tool call, and saved prompt templates (for example `/review`, `/test`). Builds on skills.

### Tier 3: polish and scale

- [ ] **Remote (streamable HTTP) MCP servers (Medium).** Only stdio servers run in a sandbox today; GitLab's and Cloudflare's official servers are remote only.
- [ ] **Subagents and delegation (Medium).** An explore or review agent with its own context so the main context stays small. The agent loop and the parallel runner already exist.
- [ ] **Background processes (Easy to Medium).** Dev servers and watchers that outlive one command, with log tailing. `run_shell` is capped at 1800 seconds today; the preview proxy exists.
- [ ] **Per-role model routing (Medium).** A planner model and a coder model, and fallback when a model is unavailable. Check what `internal/routing` already covers first.
- [ ] **Push notifications (Medium).** Web Push for "finished" and "needs approval", since the chat app is already a PWA.
- [ ] **Agent-level evaluations (Medium).** Extend the model benchmark to task suites run against real repositories, to choose models for agent work.
- [ ] **Browser tool for checking a UI (Hard).** Playwright screenshots fed to a vision model. Depends on image input.
- [ ] **Team-shared project library (Medium).** Skills, rules and memory shared across the trusted users, and hand-off of a session to another user.
- [ ] **Voice input (Easy, low value).** Browser speech recognition in the message box.

