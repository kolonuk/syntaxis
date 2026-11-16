# Security

Syntaxis lets a **small group of trusted people** run AI-driven code on your hardware. This page says what it protects, what it does not, and where the sharp edges are. To report a problem, see [SECURITY.md](../SECURITY.md).

## Who is trusted

- **Trusted:** the admin, the Docker host, your LLM server and your reverse proxy.
- **Semi-trusted:** the people you invite. They are assumed to be well-meaning, but accounts get phished and agents make mistakes, so the design limits the damage.
- **Untrusted:** the model, and everything it reads: web pages, repository files, tool output, feed and webhook data.

It is **not** built for hostile users or the open internet. [gVisor](https://gvisor.dev/docs/) is a strong boundary but not a virtual machine; an escape from it would expose the host.

## What is enforced

- **Sandboxes:** one gVisor container per session. Non-root, all capabilities dropped, read-only root, limits on memory, CPU and processes, only the owner's workspace mounted, no Docker socket. There is no fallback to plain `runc`.
- **People are kept apart:** admin and chat are separate apps with separate accounts. Nobody can register themselves. A user cannot see another user's sessions, secrets, connections or automations.
- **Sign-in:** argon2id passwords, optional two-factor authentication (mandatory for admins), lockout, revocable sessions, CSRF protection.
- **Secrets and connections:** encrypted at rest (AES-256-GCM), never shown again, never sent to the model, removed from output. They are read fresh for every command.
- **Approvals:** risky commands (destructive git, deploys, cloud changes, `sudo` and so on) wait for the user. This is code, not a prompt, and nothing the model reads can change it. Commands aimed at your local network are blocked outright.
- **Audit log:** tamper-evident. Admins see session metadata, not message content, unless you turn that on.

## Network

Sandboxes can reach the internet but never your network: private ranges, the Docker host, cloud metadata, other sandboxes, your LLM server and outbound email are blocked in every mode. Egress can be `open`, `allowlist` or `none`, for the whole instance or per user or group. It is enforced by firewall rules on the Docker host (nftables, kept applied by the installer's services) plus a DNS resolver that applies domain rules and logs every lookup. Every new connection from a sandbox is logged too. The generated rules are written for [nftables](https://wiki.nftables.org/) (`syntaxis egress-rules` prints them).

Limits: an allowed site that proxies traffic, or DNS over HTTPS, gets around domain rules. `open` mode does not stop a user or a manipulated agent sending workspace files to the internet. For sensitive work use `allowlist` or `none`.

## The Docker socket

The Syntaxis container never touches the Docker socket. A small proxy (`syntaxis docker-proxy`) does, and it refuses everything except creating, running and removing hardened gVisor sandboxes on the sandbox network. A compromised Syntaxis server could therefore run more sandboxes within the same limits, but not get a privileged container or the host filesystem.

## The terminal

The optional terminal is a shell in the same sandbox the agent uses, with the same limits and network rules. Only the owner can open it, it closes after 15 minutes idle, and admins can turn it off per user, level or group. The command blocklist applies to the agent only, so for a shell the egress mode is the control.

## Prompt injection

Text the model reads can try to steer it ([prompt injection](https://en.wikipedia.org/wiki/Prompt_injection)). Syntaxis labels such content as untrusted and keeps its rules in code, which limits the damage but does not remove the risk: a manipulated model can still do harmful things it is allowed to do. Use `ask` autonomy and a restricted egress mode on untrusted repositories.

## Keys, privacy and supply chain

- Back up the **master key** separately; without it stored secrets and 2FA cannot be recovered.
- Base images are pinned and kept current by Dependabot; releases are signed and carry an SBOM; CI runs `govulncheck` and `npm audit`.
- Keep Docker Engine itself up to date.

## Known gaps

gVisor escape resistance has not been tested, and the workspace size limit is checked every minute rather than enforced by the filesystem.
