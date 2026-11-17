# Todo

Work that is left. Finished work is not listed here; see [CHANGELOG.md](CHANGELOG.md).

Goal: as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be.

Rules for contributors: no environment-specific values in the repo (use `example.com`, `llm.internal`), no secrets, every behaviour change has tests, and security behaviour has a test that fails if the protection is removed. Tests use real services (see CONTRIBUTING.md); where one is not available the test says `NOT TESTED` rather than pretend.

## 1. Fixes and verification

- [ ] Test and document building images inside a sandbox (no claim without a passing test).

## 5. Agent and workflow

- [ ] More helper-model jobs. The helper (set per provider in the admin app) does session titles, summaries of long watch events and compaction of long conversations. Still on the session's own model: commit and pull-request messages and plan drafts. Compaction on the helper has no test because it needs a conversation longer than a model's window.
