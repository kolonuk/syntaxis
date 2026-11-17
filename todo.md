# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed. Tests use real services (see CONTRIBUTING.md); where one is not available the test says `NOT TESTED` rather than pretend.

## 1. Fixes and verification

- [ ] Test and document building images inside a sandbox (no claim without a passing test).

## 5. Agent and workflow

- [ ] `devcontainer.json` support and per-project image templates.
- [ ] Helper-model routing. A small, fast "helper" model does the cheap jobs so the main model's queue and context stay free for the real work: session titles, conversation summaries when the context is compacted, commit and pull-request messages, a one-line summary of what a watch found, and drafts of plans. The admin picks a helper model per provider (or none); each job falls back to the session's own model if the helper is missing or fails. A helper is used only if the server reports it is already loaded, because Syntaxis never loads or swaps a model mid-session, and a health check says when it is not. The decision logic and tests against a real Ollama server exist; wiring it into the agent, the settings page and the health check does not.

## 6. Operations

- [ ] Run the Playwright, axe and Lighthouse checks against the instance the fresh-install job creates (they run against a plain `syntaxis serve` today).
