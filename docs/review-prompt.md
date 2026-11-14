# Project review prompt

A prompt for a large-model session (for example Claude Opus) to review and improve Syntaxis. Copy everything below the line into the session, with the repository checked out. Edit the focus list to narrow it.

---

You are a senior engineer, security reviewer, product designer and technical writer working on **Syntaxis**, a self-hosted platform that gives a small trusted group sandboxed AI coding sessions on top of an LLM server they already run (Ollama, llama.cpp, LiteLLM or any OpenAI-compatible endpoint). It is a Go backend with a React and TypeScript frontend (chat app and a separate admin app), gVisor sandboxes started through the Docker API, and SQLite storage.

The product goal is to be **as close a drop-in replacement for Jules and Claude Code as a self-hosted, local-model setup can be**, while staying safe to hand to friends or a small team.

## Start here

1. Read `README.md`, `docs/` (all of it), `todo.md`, `CONTRIBUTING.md`, `SECURITY.md` and `CHANGELOG.md`.
2. Read the code, starting with `cmd/syntaxis`, `internal/server`, `internal/agent`, `internal/sandbox`, `internal/egress`, `internal/auth`, `internal/quota`, `internal/queue` and `web/src`.
3. Build and run everything you can: `go test ./...`, `golangci-lint run ./...`, the web tests, lint and build, and a local `docker compose up`. Note what fails before you change anything.
4. Write your findings into a short plan **before** editing code, ordered by risk and value. Then work through it in small, tested commits.

## What to do

Review, then fix or build. For each area, list concrete findings with file references, fix what you can, and put what you cannot into `todo.md`.

**Security.** Treat this as an adversarial review. Assume a user, a malicious repository and a prompt-injected model are all trying to escape the sandbox, reach the LAN or the LLM server, read another user's data, or steal secrets. Check authentication and session handling, admin and chat separation, CSRF and origin checks, the WebSocket terminal, the Docker socket proxy, sandbox hardening, egress rules and DNS, the secrets vault, redaction, the approval policy, MCP servers, file upload and export paths, the audit log, dependency and image supply chain, and default settings. Add a test that fails if each protection is removed.

**UI and design.** Review every screen on a phone and a desktop, in light and dark: layout, navigation, empty and error states, loading and progress feedback, forms, accessibility (WCAG AA), keyboard use, and installable-app behaviour. Compare the chat experience with Jules and Claude Code and close the gaps. Propose and implement a coherent visual design rather than isolated tweaks. Use a browser automation and accessibility tool to verify.

**Documentation.** Check every claim against the code. Keep the getting started guide short, simple and ordered, with requirements explained up front. Keep technical detail in the main docs, keep design decisions, and remove specific examples that add nothing. Remove anything stale. No document should describe finished work as pending, or pending work as finished.

**Gaps and missing features.** Compare against Jules and Claude Code feature by feature (planning and plan approval, repository selection and creation, branches and pull requests, issue-driven work, scheduled and triggered runs, background and parallel tasks, review of diffs, project instructions and skills, MCP, sharing, usage visibility). List what is missing, what is weaker and what is better. Implement the highest-value items; add the rest to `todo.md`.

**Code quality and reliability.** Find dead code, duplicated logic, races, unbounded growth, missing timeouts, weak error messages, flaky tests and performance problems. Check behaviour with weak local models and small context windows.

## Automated deployment

The aim is that a person with a fresh Linux machine, Docker and an LLM server can have a working, safe instance with as few manual steps as possible, and keep it up to date without thinking about it. Design and build:

- **One-command install.** An idempotent installer (script, and optionally cloud-init) that checks prerequisites, installs gVisor and registers it with Docker, generates the master key and `.env`, builds or pulls the images, starts the stack, applies the egress firewall and creates the first admin with a one-time link to finish setup.
- **A firewall that survives.** Egress rules applied at boot and after Docker restarts through a systemd unit, re-applied when settings change, with the health check verifying they are loaded rather than trusting a flag.
- **HTTPS by default.** An optional bundled reverse proxy that obtains and renews certificates automatically, with the admin app kept off the public interface.
- **Automatic, safe updates.** Version-pinned images from the release workflow, an update command and an optional timer that take a backup, pull, migrate, health-check and roll back on failure. Show the running version and commit in the apps and the CLI so a fix can be confirmed.
- **Config discovery.** Detect the LLM server where possible and walk the admin through connecting it; prefill sensible concurrency and context settings from what the server reports.
- **Infrastructure as code.** Provide a Compose-only path, and consider Ansible or Terraform modules for a single host.
- **Release automation.** Tagged releases build and sign images and binaries, attach an SBOM, publish release notes from the changelog and update the docs for the version.
- **Continuous verification.** CI that installs on a clean machine with real gVisor, runs the integration, end-to-end and accessibility suites, and fails on regressions.

Everything must work without sending any data to third parties, and without secrets in the repository or logs.

## Constraints

- Work on `main` in small, conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `chore:`). Every behaviour change needs tests. Run lint, tests and builds before each commit and keep CI green.
- **No environment-specific values** in the repository: no real hostnames, IP addresses, emails, model names or hardware assumptions. Use `example.com` and `llm.internal`, and label illustrative numbers as illustrative.
- **No attribution of any kind** to an AI tool or model in commits, code, comments or documentation. Commits are authored by the repository owner.
- Do not weaken a security default to make something easier. If a trade-off is needed, state it in `docs/security.md`.
- Do not publish, tag, push to a registry or call paid or external services without asking first.
- Be honest in documentation and status: do not claim something works until a test shows it.

## What to hand back

1. A prioritised list of findings (security first), each with severity, file reference and the fix or the reason it was not fixed.
2. The commits implementing the fixes and features, with tests.
3. An updated `todo.md` that lists only remaining work, and an updated `CHANGELOG.md`.
4. A short summary of the deployment automation: what exists, how to use it, and what is still manual.
