# Security and threat model

Syntaxis lets a **small group of trusted people** run AI-driven code on your hardware. This document says what it protects, what it does not, and where the sharp edges are.

## Trust model

- **Trusted:** the admin, the Docker host, the LLM server, the reverse proxy.
- **Semi-trusted:** chat users. They are invited by an admin and assumed not to be malicious, but accounts get phished and agents make mistakes, so the design limits blast radius.
- **Untrusted:** everything the agent reads (web pages, repository files including `AGENTS.md`, tool output, skills, MCP output) and the model itself.

Syntaxis is **not** designed for hostile users or the open internet. gVisor is a strong boundary, not a virtual machine; a kernel/gVisor escape would expose the host.

## What is enforced (and tested)

| Property | How | Tests |
| --- | --- | --- |
| Admin and chat are separate | Separate tables, credentials, login pages, cookie names, route sets, optional separate listener. Admin accounts have no sandboxes or workspaces | `internal/server`, `internal/auth` |
| No self-registration | There is no registration route; only `InviteUser` (admin-only) creates chat users | `TestNoSelfRegistrationRoute` |
| Sandboxes are isolated | `BuildSpec`/`Validate` require runtime `runsc`, non-root, `cap_drop ALL`, `no-new-privileges`, read-only rootfs, pids/memory/CPU limits, only the owner's workspace volume, no host mounts, no Docker socket, no `DOCKER_HOST` | `internal/sandbox` |
| No fallback to `runc` | `Manager.Create` refuses when `runsc` is not registered | `TestCreateRefusesWithoutRunsc` |
| User A cannot reach user B | Per-user volumes; session/secret APIs return 404 for other users' resources; secrets are bound to their owner by AEAD associated data | `internal/server`, `internal/secrets` |
| Secrets | AES-256-GCM at rest, master key from env/file; names only are ever listed; redacted from model input, UI stream and history | `internal/vault`, `internal/secrets`, `internal/agent` |
| Approval gates | A code-level policy (not a prompt) pauses risky commands; prompt-injected text cannot change it | `TestPromptInjectionCannotBypassApproval`, `TestHostileAGENTSmdCannotChangePolicy` |
| Audit | Hash-chained log; tampering is detected (`/api/audit` reports `chain_ok`) | `internal/audit` |
| Auth hardening | argon2id, TOTP + recovery codes, replay protection, lockout per account and IP (also for unknown accounts), uniform login errors, generic reset responses, server-side revocable sessions, CSRF + same-origin checks | `internal/auth`, `internal/server` |

## Egress

Modes: `open` (default; full outbound internet), `allowlist`, `none`. In **every** mode these are blocked: RFC1918/LAN, loopback, link-local and **cloud metadata** (169.254.169.254 and friends), CGNAT/tailnet ranges, the Docker bridge gateway, other sandboxes, Syntaxis internals and the LLM server (except through Syntaxis), and outbound SMTP (25/465/587).

How: `syntaxis egress-rules` generates an nftables ruleset applied **on the Docker host** (`scripts/apply-egress.sh`). Syntaxis also runs a DNS resolver for the sandbox network that applies per-domain policy, refuses answers pointing into blocked ranges (DNS rebinding), and logs queries (names and answers only, never payloads).

Limits, honestly:
- Rules must be applied by the operator and survive reboots; the health check can only see whether you told it they are applied (`SYNTAXIS_NETWORK_RULES_APPLIED`).
- Allowlist mode by hostname depends on DNS answers; a sandbox that learns an IP another way and is on an allowed CIDR is allowed.
- IPv6 is not routed on the sandbox network by default; if you enable it, extend the rules.
- DNS over HTTPS or any allowed host that proxies traffic bypasses domain policy. `open` mode is not a substitute for trusting your users.
- Connection-level (non-DNS) egress logging requires conntrack/nflog integration that is **not implemented yet**; only DNS queries are logged today.

## The Docker socket

The Syntaxis container does not mount the raw socket. A `docker-socket-proxy` does, exposing only containers, volumes, networks, images and exec. **This is still powerful**: anyone who can reach the proxy can run containers on the host. It is kept on a private Compose network, never published. A stricter allow-list proxy is future work.

## Prompt injection

Tool output, web content, repo files and skills are wrapped and labelled untrusted; policy lives in code. This **reduces** risk, it does not eliminate it: a model can still be talked into harmful-but-permitted actions inside the workspace (for example editing files, or exfiltrating workspace data over an `open` network). Use `allowlist`/`none` egress and `ask` autonomy for sensitive work.

## Secrets and cloud credentials

Redaction of tool output is best-effort (known patterns plus the user's own stored values). The model sees command output; anything printed can be sent to the model and logged. Use least-privilege, short-lived credentials and budget alerts ([integrations.md](integrations.md)).

## Privacy

Admins see session metadata (who, when, model, tokens, state), not message content, unless `ALLOW_ADMIN_READ_CONTENT=true`; reads are audited. Egress logs are visible to admins in full and to each user for their own sessions only. Set retention to match your obligations.

## Known gaps in this version

- Web Push delivery, the authenticated dev-server preview proxy, interactive PTY terminal, MCP server running, the model benchmark and connection-level egress logging are not implemented yet (see [todo.md](../todo.md)).
- Nothing here has been verified against a real gVisor host; integration and escape-resistance claims await that test.
- Pinned image digests are not yet set.

Report vulnerabilities privately: [SECURITY.md](../SECURITY.md).
