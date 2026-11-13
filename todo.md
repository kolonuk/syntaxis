# Syntaxis: build plan and todo

This file is the working spec, rewritten from the original build prompt. Review it after every large code change. Tick items only when code AND tests exist. Mark stubs honestly.

Status legend: `[x]` done + tested, `[~]` partial/stubbed (say what), `[ ]` not started, `[!]` needs owner (auth/credentials/real host).

## Standing instructions from owner

- Work in `/home/kolon/projects/syntaxis`. Single branch `main`, small conventional commits. Public repo `kolonuk/syntaxis`.
- v1 focus: code. Skip anything needing owner auth: do NOT create the GitHub repo, push, enable secret scanning, log in to registries, send real email, or call real cloud APIs. Mark those `[!]`.
- Write as many unit tests as possible. Run code scanning (govulncheck, npm audit, gofmt, vet, golangci-lint if installable, secret scan of own tree) before finishing.
- GitHub Actions workflows: lint, vet, test, build, docker image build (no push without secrets configured), vuln scans. Never execute them here.
- Never commit secrets, real hostnames, IPs or emails. Neutral placeholders only (`example.com`, `llm.internal`).
- If session usage runs out, set a reminder to continue after reset.
- Commit to the new repo when done (local commit only; push is `[!]`).

## What Syntaxis is

Self-hosted web app giving a small group of trusted people Jules-style sandboxed coding sessions on top of an LLM server you already run (Ollama, llama.cpp, LiteLLM, any OpenAI-compatible endpoint). Each chat session = its own gVisor (`runsc`) container with shell, git, toolchain. An AI agent works inside it (runs commands, edits files, git, tests, can deploy to cloud) while the user watches or walks away. Users segregated from each other and from the host. The LLM server is not part of the project. Generic and releasable: no environment-specific names/paths/hosts/hardware anywhere; all of that is config. Not for the open internet. No sign-up; admin creates users.

## Repo / licence / docs

- [ ] GPL-2.0-or-later: full `LICENSE`, SPDX headers, `docs/licensing.md` (why "or later": Apache-2.0 deps e.g. Docker SDK), `THIRD_PARTY_LICENSES` (go-licenses + npm equivalent), CI fails on incompatible licence. Unresolvable licence: stop and tell owner.
- [ ] Docs: `README.md`, `docs/getting-started.md` (incl. exact runsc install + Docker runtime registration), `docs/configuration.md`, `docs/security.md` (honest threat model), `docs/architecture.md`, `docs/agent.md`, `docs/integrations.md`, `docs/licensing.md`, `docs/pitfalls.md`, `CONTRIBUTING.md`, `SECURITY.md`, `.env.example`, `docker-compose.yml`, `CHANGELOG.md`, `.gitignore`.
- [ ] README: first screen says what/who; tagline + 3-4 sentence pitch; Why Syntaxis (self-hosted, BYO LLM, per-session isolation, per-user quotas, no sign-up, admin-controlled, single binary) + honest "what it is not" (not hosted, not hardened for public internet, not a VM); features; Mermaid architecture; screenshot placeholders; "How a session works"; factual comparison; quick start < 10 commands; requirements (Docker, gVisor, LLM endpoint); doc links; roadmap; contributing; licence; security note (TLS + reverse proxy/SSO); badges (CI, licence, release).
- [ ] `docs/pitfalls.md` covers: context windows (advertised/configured/loaded, silent truncation, KV cache, per-server how-to Ollama/llama.cpp/LiteLLM/generic), model residency + routing (VRAM maths, off by default), tool-calling quality, concurrency/queueing, gVisor limits, egress limits, secrets/cloud creds, reverse proxy/TLS, email (SPF/DKIM, clock skew/TOTP), disk, backups/upgrades (master key loss), prompt injection, known limitations. Each entry links its health check id.

## Stack

- Backend Go 1.23+: `net/http` + `chi`, `log/slog`, `Store` interface (SQLite WAL via `modernc.org/sqlite`, pure Go, portable SQL, migrations), Docker Go SDK, `x/crypto/argon2`, `pquerna/otp`, `coder/websocket`. Few deps. Frontend embedded with `go:embed`; one static binary + minimal (distroless/scratch) image.
- Frontend: TypeScript strict + React + Vite, xterm.js terminal, SSE chat streaming, CSS-variable design system (no heavy UI framework).
- DB: SQLite default (WAL, one writer, batched message writes). Keep SQL portable so PostgreSQL can be added v1.1. Document when to move up. No DB server in default compose.
- Tests: `go test` (unit, API, gVisor integration that skips w/o `runsc`); Vitest; Playwright (a couple of flows).
- Lint/format: golangci-lint, gofmt, ESLint, Prettier. CI: lint, vet, test, build, image build, govulncheck, npm audit, Trivy.
- `docker-compose.yml` one-command start + plain `syntaxis serve`.

## Accounts and auth

- [ ] Admin accounts and chat users fully separate: separate tables/credentials, login pages, cookies, permissions. Admin cannot open chat sessions/has no sandbox; chat user cannot reach admin routes (tests).
- [ ] Admin UI/API under separate path prefix and optional separate listener (`ADMIN_LISTEN`, default separate port, loopback).
- [ ] Admin 2FA mandatory (enforced at first login). First admin via CLI `syntaxis admin create` or env on first boot, never web.
- [ ] Admins manage users/quotas/models/providers/policies, see session metadata; cannot read message content unless `ALLOW_ADMIN_READ_CONTENT=true` (default false). Admins never see/set user passwords.
- [ ] No sign-up route. Admin creates chat user (email, name, quota, allowed models, egress policy) -> invite email with single-use expiring token -> user sets password.
- [ ] Password reset by email: single-use short-lived token, generic responses (no account enumeration).
- [ ] argon2id; min length; small common-password list.
- [ ] TOTP 2FA + recovery codes. Users without 2FA get reminder every login ("set up now" / "remind me next time"), never silently dropped.
- [ ] Password rotation (admin-controlled N days, default 30, 0 disables) for users WITHOUT 2FA; forced change at login when expired; warn a few days before; 2FA users exempt.
- [ ] Cookies: httpOnly, Secure, SameSite=Strict, rotating, server-side store w/ revocation, log out everywhere. CSRF on state-changing requests.
- [ ] Login rate limiting + temporary lockout per account and per IP.
- [ ] SMTP via env; dev mode logs emails to console; admin "send test email".
- [ ] Optional trusted-reverse-proxy-header auth (off by default; only from allowlisted proxy addrs).

## LLM connections, models, queue, routing

- [ ] Provider adapters behind one interface: Ollama native, llama.cpp (OpenAI-compat), LiteLLM, generic OpenAI-compat. Multiple providers, optional API key each.
- [ ] Dynamic model discovery (`/api/tags`, `/v1/models`) on demand + periodic; status (reachable, latency, loaded model). Admin enables models and per-user/group access. Read-only toward LLM server (never pull/delete).
- [ ] Models without tool-calling handled gracefully (chat-only offer). Streaming, cancel, clear errors.
- [ ] Scheduler per provider: admin concurrency, never exceeded. Priority classes interactive > agent-autonomous > benchmark; fair share per user within class (round-robin/weighted). Users see position + ETA, can cancel, timeout error. Queue metrics; admin live queue, bump/cancel. Quotas checked before enqueue.
- [ ] Model routing optional, OFF by default: helper model for titles/summaries/compaction. Never cause a load/swap mid-session; Ollama `/api/ps` residency check, refuse non-resident profile; others admin-asserted; auto fallback to single model + tell admin. Only runtime-discovered data; no hardcoded model/GPU assumptions.

## Health-check engine (`syntaxis doctor` + admin Health page + user banners)

Each check: id, severity (info/warning/error), plain explanation, suggested fix, snooze (not for errors), unit-tested with fake provider responses.
- [ ] Context size (min default ~16k warn, ~8k error; loaded vs advertised vs configured; mismatch across aliases; show detected/used/source; admin override).
- [ ] Context mismatch risk (configured > real).
- [ ] Tool calling (no support or fails probe).
- [ ] Streaming/timeouts (proxy buffering, short timeouts).
- [ ] Concurrency (too high / unset).
- [ ] Residency/memory (routing models not co-resident, repeated reloads, CPU spill).
- [ ] Provider state (unreachable, slow, auth failure, empty list, model disappeared, digest changed since benchmark).
- [ ] Sandbox platform (runsc missing/unregistered, socket proxy unreachable/too permissive, network rules missing, image missing, disk full, low memory).
- [ ] Security posture (admin 2FA, rotation off, SMTP unset, master key unset/in DB dir, no HTTPS, admin bound public, default quotas unset, scanner off, egress open (info), proxy-header misconfig).
- [ ] Housekeeping (backups stale, retention unset, log growth, clock skew, missing VAPID keys).
- [ ] Admin header summary badge; users see plain banner.

## Benchmark (admin only)

- [ ] Small coding-agent task suite (tool-call correctness, edit accuracy, instruction following, multi-step in throwaway sandbox); pass rate, tok/s, TTFT, tool-call failure rate, context behaviour; stored per model+date; lowest queue priority; schedulable; admin-only; optional "recommended for agents" flag.

## Agent (built in, v1)

- [ ] Server-side loop: user task -> messages+tools to model -> text or tool calls -> execute in sandbox (never host) with timeouts + output caps -> feed back -> until done/stop/cap/approval-blocked. Streams to UI; runs as background job; notifies on finish/needs input.
- [ ] Tools: shell, read file, write/patch file, list/search (ripgrep), git (status/diff/commit/branch/push/PR), fetch URL, ask user / request approval, load skill, MCP tools.
- [ ] System prompt, context mgmt, loop + repeated-failure detection, tolerant tool-call parsing (text/JSON fallback for weak models).
- [ ] Approval gates (destructive git, rm -rf outside workspace, terraform apply, cloud delete/destroy, spend money); configurable patterns; default ask; per-session auto-approve opt-in. Autonomy levels: ask-all / auto-in-workspace (default) / full-autonomous within caps.
- [ ] `AgentAdapter` interface; experimental `opencode` adapter via Syntaxis-hosted OpenAI-compat proxy w/ per-session scoped token (sandbox never sees real keys or LLM address). Document trade-offs in `docs/agent.md`.
- [ ] Context management: learn real context limit (Ollama `/api/ps`, `/api/show`; admin override); stay under real (not advertised) limit; token budget per request; provider usage or estimator; head+tail tool-output truncation with note; compact at ~75% with running summary, keep last turns verbatim, never discard transcript; context meter + warning; "fresh context with summary".
- [ ] Checkpoints: shadow git repo (separate `--git-dir` in workspace volume), ignore list, volume snapshot fallback, prune count/age/size, per-user disk budget, timeline/diff/restore/undo last turn, files-only vs files+conversation (fork, abandoned branch kept), always checkpoint before approval-gated action, UI states external effects not undone. Built-in diff viewer (apply/discard hunks) before push.
- [ ] Repo rules: `syntaxis.md` (+ `.syntaxis/`) authoritative; foreign files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.cursorrules`, copilot instructions) advisory, lower-trust, wrapped as untrusted, size-capped, user include/ignore, cannot override policy; admin/user disable. Test with hostile `AGENTS.md`.
- [ ] Skills: `.syntaxis/skills/` (+ `.claude/skills/`, `.agents/skills/`) `SKILL.md` front matter; progressive disclosure; scripts only in sandbox with approval gates; user sees loaded skills; admin instance skills; disable repo skills.

## Sandboxes

- [ ] One container per session via Docker API, runtime `runsc`; configurable image; default dev image (git, curl, build-essential, python3, node, go, ripgrep, jq); cloud-tools variants.
- [ ] Isolation: non-root, `cap_drop ALL`, no-new-privileges, read-only rootfs + workspace volume + tmpfs, default seccomp, not privileged, no host mounts, no docker socket, pids/mem/CPU/disk limits.
- [ ] Per-user workspace volume (size-capped), mounted only into that user's sandboxes; tests prove A cannot see B.
- [ ] Lifecycle create/pause/resume/destroy; reaper on boot; workspace persists.
- [ ] Preview proxy `/preview/<session>/<port>/` authenticated, owner only.
- [ ] Networking: dedicated network; egress `open` (default) / `allowlist` / `none`, per-instance + per-user override; always block host + bridge gateway, RFC1918, link-local, metadata (169.254.169.254), Syntaxis internals, LLM server (except via proxy), outbound SMTP 25/465/587. Log connection metadata + DNS via Syntaxis resolver.
- [ ] Compose ships docker socket proxy (allowlist); never raw socket by default.
- [ ] `syntaxis doctor`: Docker access, runsc runtime, network rules, SMTP, provider reachability; prints fixes.

## Cloud deploy from a session

- [ ] Image variants: gcloud, aws, az, wrangler/cf, terraform/opentofu, kubectl, helm, flyctl, gh (separate tags).
- [ ] Per-user cloud creds from secrets vault, injected as short-lived env/credential files only while running, removed on stop; docs recommend least privilege.
- [ ] Output redaction (known patterns + user's own secret values) before model/logs.
- [ ] Approval gate for apply/deploy/delete/destroy (configurable patterns), "plan first then ask" in system prompt, admin option to disable cloud tooling per user.
- [ ] Docker-build-in-sandbox: test + document honestly (no claims without a passing test) `[!]` needs real gVisor host.
- [ ] Deploy-preview doc example.

## Quotas, abuse protection

- [ ] Token quota per user daily/monthly (in+out); time controls: max concurrent sessions, wall-clock per session, idle timeout, sandbox-hours/day; request rate + max parallel model requests; global caps; meters; clear exhausted message; admin top-up/reset; enforced server-side before model call and tool exec.
- [ ] cgroup limits; watchdog (CPU/mem/disk write/disk full): throttle -> pause -> kill + audit; abuse pattern detection + admin email; admin kill switch (all / per user).

## Secrets, scanning, signing, MCP

- [ ] Vault AES-256-GCM, master key from env/file (not in DB/repo), rotation command; secrets never in logs/model/images; instance + per-user scopes; reach sandbox only while needed.
- [ ] Integrations v1: GitHub (PAT/App: list repos, clone, branch, push, PR), generic git HTTPS/SSH deploy keys; status + test-connection. v2 interface designed (Cloudflare, registrars) only.
- [ ] Built-in secret scanner (gitleaks-style rules + entropy; check licence of reused rules) on staged diffs, agent-written files, tool output (redaction); policy `off|warn|block`; user override with confirmation recorded in audit unless `allow_user_override=false`. Trivy/osv-scanner only in optional security image, on demand.
- [ ] Signed commits `off|optional|required` via SSH signing; private key never in sandbox/model (signing helper/agent socket); agent-authored trailer.
- [ ] MCP: admin catalogue (official MCP Registry, pluggable source; verify API at build time), review -> approve -> run each in own gVisor container, pinned digests, minimal secrets, egress policy, output untrusted, approval gates for state-changing tools, log every call.

## Logging, history, lifecycle

- [ ] slog JSON logs with request/session ids, `LOG_LEVEL`, pretty dev; separate app/session/audit logs; audit hash chain + export; admin log viewer + redacted debug bundle; egress logging (admin all, user own); optional Prometheus `/metrics`, `/healthz`; retention/rotation.
- [ ] Session history: persist sessions, messages, tool calls, results, model, tokens, timestamps; list/search/rename/delete/resume; export MD/JSON.
- [ ] Lifecycle: stop idle (30 min), destroy after N days (7), workspace retention (30 days, 0 keep) w/ warning email + keep/export; periodic reaper; admin disk per user + cleanup now.
- [ ] Extra security: CSP/secure headers/same-origin CORS; admin IP allowlist; admin notification emails; pinned base digests + SBOM; prompt-injection stance; backup/restore command; optional terminal recording.

## UI / PWA

- [ ] Design system on CSS variables, light/dark + toggle + `prefers-color-scheme`; components: buttons (primary/secondary/danger/ghost/icon/loading), inputs, selects, toggles, cards, badges, tabs, modals, toasts, tooltips, meters, empty states, skeletons, shortcuts.
- [ ] Chat-user screens: login, set-password, forgot/reset, 2FA setup, session list + chat + terminal/files/preview panel, model picker, usage meters, settings.
- [ ] Admin screens (separate app): users, quotas, models, providers, sessions metadata, logs, audit, policies, health, kill switch.
- [ ] Responsive to phone; accessible (labels, focus, contrast, reduced motion).
- [ ] PWA: manifest (icons incl. maskable, shortcuts), service worker (app shell, offline page, update prompt), Web Push (VAPID), Badging API, offline message queue + Background Sync fallback, safe-area, no CLS; never cache API user data in shared cache; per-user caches cleared on logout. Lighthouse audit `[!]` needs browser run.

## Roadmap (post v1, document only)

Scheduled/triggered runs; devcontainer.json; PostgreSQL; OIDC; multi-instance fallback; read-only session sharing; image templates; prompt presets; webhooks; usage reports; Cloudflare/registrar plugins; Grafana JSON; maintenance banner; user groups.

## Build order (vertical slices, commit each with tests)

1. scaffold + CI + README skeleton
2. config, store + migrations, audit chain, secrets vault, redaction, secret scanner
3. auth: admin/chat separate, invite/reset/2FA/rotation/rate-limit/CSRF/sessions
4. providers + discovery + health engine
5. scheduler queue + quotas
6. sandbox manager (interface + docker impl + fake) + egress policy + watchdog + lifecycle
7. agent loop, tools, tolerant parsing, approvals, context mgmt, checkpoints, rules/skills
8. API wiring + SSE streaming + history/export
9. frontend (user + admin) + PWA
10. docs, Dockerfile, compose, GHA workflows, scans
11. final: review vs this file, update acceptance state, local commit

## Acceptance checklist (v1 done when all pass; `[!]` items need owner/real host)

- [ ] Public repo + README + LICENSE + passing CI `[!]`
- [ ] `docker compose up` from clean checkout `[!]`
- [ ] Admin/chat separation tests
- [ ] Invite/set-password/login/reset; no self-register route
- [ ] 2FA reminder, rotation, admin 2FA mandatory
- [ ] Models discovered live from Ollama + OpenAI-compat (fake servers in tests; real `[!]`)
- [ ] Session creates gVisor sandbox; agent runs/edits/tests/commits; survives browser close; notifies; history resumes `[!]` real runsc
- [ ] Cross-user isolation tests (workspace, sessions, secrets)
- [ ] Egress modes tests (rule generation unit tests; live `[!]`)
- [ ] Limits enforced; fork bomb / disk fill contained `[!]` live
- [ ] Token/time quotas enforced + shown
- [ ] Secrets encrypted + redacted
- [ ] GitHub clone/branch/push/PR `[!]` needs token
- [ ] Cloud deploy w/ approval gate `[!]` needs creds
- [ ] Logs/audit/debug bundle/doctor
- [ ] Themes polished desktop + phone
- [ ] Context compaction never exceeds real limit
- [ ] Checkpoints/undo/restore; user git untouched
- [ ] Queue position/priority/fairness/concurrency tests
- [ ] `syntaxis.md` + skills; hostile AGENTS.md test
- [ ] Scanner block/override/forbid; Trivy/osv on demand only
- [ ] Idle/expired cleanup with pre-warning
- [ ] Benchmark admin-only; routing refuses non-resident, falls back
- [ ] MCP catalogue + one real server in gVisor `[!]`
- [ ] Signed commits required mode w/o key in sandbox
- [ ] Egress log admin-all / user-own
- [ ] PWA install/offline/push `[!]`
- [ ] Health checks fire for listed cases
- [ ] Routing uses only runtime data
- [ ] `docs/pitfalls.md` complete + links to checks
- [ ] Docs complete, honest threat model, licensing notes

## Progress log

(append milestone notes here: what works, what is stubbed, what is untested)
