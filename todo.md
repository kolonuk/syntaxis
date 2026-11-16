# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed.

## 1. Fixes and verification

- [ ] CI fully green, including the gVisor integration job, on every push.
- [ ] Test and document building images inside a sandbox (no claim without a passing test).
- [ ] Lighthouse audit (PWA, accessibility, performance) run in CI against a started instance.
- [ ] Probe tool-calling support live rather than inferring it; probe proxy response buffering live.

## 5. Agent and workflow

Compared with Jules and Claude Code, these are missing or weaker (background runs, approvals, project instructions, skills, MCP, checkpoints and usage visibility already exist):

- [ ] Plan mode: the agent proposes a plan the user approves or edits before it changes files.
- [ ] Parallel tasks: several runs of one task (or several tasks) at once, each in its own sandbox, with a side-by-side comparison.
- [ ] Diff review in the UI before a push: per-file diff, comments that go back to the agent.

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
- [ ] Workspace size cap enforced by a filesystem quota. Today the size is measured every minute; over the limit new messages are refused until space is freed (the terminal stays open), and the sandbox is stopped when far over.
