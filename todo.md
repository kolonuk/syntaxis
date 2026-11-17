# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed. Tests use real services (see CONTRIBUTING.md); where one is not available the test says `NOT TESTED` rather than pretend.

## 1. Fixes and verification

- [ ] Test and document building images inside a sandbox (no claim without a passing test).

## 5. Agent and workflow

- [ ] More helper-model jobs. The helper (set per provider in the admin app) does session titles, summaries of long watch events and compaction of long conversations. Still on the session's own model: commit and pull-request messages and plan drafts. Compaction on the helper has no test because it needs a conversation longer than a model's window.

## 2. Not yet verified with the real thing

These work as far as a test can show, but the test needs something this project does not have. Each skips as `NOT TESTED` and is listed in the CI summary.

- [ ] Connections to real GitLab, Cloudflare, AWS, Azure, Google Cloud and IFTTT accounts, and a real GitHub token in CI (set the `SYNTAXIS_TEST_*` secrets). Only wrong-credential behaviour and the SigV4 signing test vector are checked today.
- [ ] The pull-request flow end to end: the agent pushes a branch from a sandbox and a pull request is opened. Sandboxes cannot reach a private forge by design, so this needs a public test repository.
- [ ] SMTP login and STARTTLS against a real relay.
- [ ] The llama.cpp and LiteLLM adapters against real servers.
- [ ] The installer on a full virtual machine per distribution (CI checks package installation and the fresh install on one image; this host has no hardware virtualisation).
- [ ] Parallel attempts with real model runs side by side, and what "keep this one" should do with the kept branch (today it removes the others and leaves pushing to the user).

## 3. Features

- [ ] Remote (streamable HTTP) MCP servers. Only stdio servers run in a sandbox today; GitLab's and Cloudflare's official servers are remote only.

