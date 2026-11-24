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
- **Admins can read everything users have set up.** The machine is the admin's: they can read every session's messages and see and change each user's notes, commands, hooks, connections, MCP choices, automations and API tokens, and every such read or change is in the audit log. People you invite should know this. The one thing nobody sees is a stored credential's value: it is encrypted with the master key and shown nowhere, to admins either.
- **Audit log:** tamper-evident; see [the audit log](#audit-log) for exactly what that means and how to check it.
- **Admin accounts** are kept in the admin app (see [admin accounts](#admin-accounts)).
- **Uploads are off until an admin turns them on** (Policies → Allow file uploads). Then only pictures, PDFs, office documents (`docx`, `xlsx`, `pptx`, `odt`, `ods`, `odp`) and plain text (`txt`, `md`, `csv`, `tsv`, `json`, `log`) are accepted, up to a size the admin sets, and the content must really be what the name says. Programs, scripts, archives, old Office files, macros, embedded objects, appended data after a picture, long base64 or hex runs in text, and PDFs with scripts, launch actions, embedded files, encryption or encodings that hide content are refused, and every refusal is audited. The check reads bytes only; it never opens a file with another program. It is a filter, not proof of safety: a file it accepts can still be a bad document, so keep sandboxes and egress limits in place.
- **API tokens** are per user, hashed, scoped (`read` or `run`), expiring and revocable. A token can never touch secrets, connections, settings, hooks, the terminal or other tokens. See the [API](api.md).
- **Repository hooks need your trust.** A repository's `.syntaxis/hooks.json` runs commands around every tool call, so it does not run until you have read the commands and trusted that exact file for that repository; any change to the file asks again. Hooks together get 5 minutes in each run, and the time they use shows on the Resources tab.

## Audit log

Every sensitive action is written to a log: who did it (an admin, a user, the host's services), what, to what, and when. Each entry stores a SHA-256 fingerprint of its own content **and of the previous entry's fingerprint**, so the log is a chain. Change, delete or insert an entry anywhere and every fingerprint after it stops matching.

"Audit chain verified" in the admin app means Syntaxis recomputed every fingerprint from the first entry and all of them matched. The **Audit** tab shows how many entries were checked and the fingerprint of the newest one, and lets you download the whole log.

To check it somewhere that does not have to trust the server's answer, download the log (Audit → Download the log) and run:

```sh
docker compose exec -T syntaxis syntaxis audit verify - < syntaxis-audit.jsonl
```

It prints the number of entries and the latest fingerprint, or says which entry no longer matches. `syntaxis audit verify` with no file checks the live database.

What the chain does **not** do: it cannot, by itself, tell that entries were cut off the end of the log, or that someone with write access to the database rewrote the whole log from some point on with fresh fingerprints, because that is a valid chain too. The defence is the fingerprint of the newest entry: copy it somewhere else (a note, a ticket, another machine). If a later check shows a different fingerprint at the same entry number, or fewer entries, the log was rewritten. Back up the database, and the log with it.

## Admin accounts

Admins are separate from chat users: they sign in to the admin app only, always with two-factor authentication. The **Admins** tab lists them with their state (2FA on, no 2FA, invited and not yet set up, disabled) and lets an admin add another, give someone who is locked out a new single-use link (which clears their password and 2FA and ends their sessions), disable, enable or remove an account. You cannot reset, disable or remove your own account there, so you cannot lock yourself out. Every change is audited. The link is never emailed; it is shown to the admin who asked, once. `syntaxis admin create` and `syntaxis admin setup-link` do the same from the command line, for the very first admin or if no admin can sign in.

The health finding "admin accounts have no 2FA" names the accounts: an account with a password but no 2FA is an error, one that was created but whose setup link was never opened is a warning, and a disabled account is not counted.

## Network

Sandboxes can reach the internet but never your network: private ranges, the Docker host, cloud metadata, other sandboxes, your LLM server and outbound email are blocked in every mode. Egress can be `open`, `allowlist` or `none`, for the whole instance or per user or group. It is enforced by firewall rules on the Docker host (nftables, kept applied by the installer's services) plus a DNS resolver that applies domain rules and logs every lookup. Every new connection from a sandbox is logged too. The generated rules are written for [nftables](https://wiki.nftables.org/) (`syntaxis egress-rules` prints them).

The update service on the host (`syntaxis-update-agent`, optional: `--no-update-agent` skips it) lets an admin start an update, or build and remove an example environment, from the admin app. It uses the same host token as the firewall sync, accepts only "latest", "edge", a version number, or the name of an example environment that has a Dockerfile in `images/templates`, builds the command itself, and runs `scripts/update.sh`, which backs up first and rolls back if the new version is not healthy. It runs as root on the host, so it only ever installs what the project publishes; the "development branch" option builds the main branch from your checkout.

A user's own (or their group's) egress mode is enforced in the host firewall by the firewall sync service the installer sets up. If that service is configured but not running, an `allowlist` sandbox starts with no network rather than open; if it is not configured at all, a per-user mode is applied by the DNS resolver only. MCP servers run under the egress mode of the user they run for.

When the server itself calls an address a user typed (a self-hosted GitLab or Gitea, a watched feed), it reaches public addresses only: not its own loopback, the Docker networks, the LAN or cloud metadata, including through redirects. An admin can allow a LAN range for a forge with `SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE`.

Limits: an allowed site that proxies traffic, or DNS over HTTPS, gets around domain rules. `open` mode does not stop a user or a manipulated agent sending workspace files to the internet. For sensitive work use `allowlist` or `none`.

## The Docker socket

The Syntaxis container never touches the Docker socket. A small proxy (`syntaxis docker-proxy`) does, and it refuses everything except creating, running and removing hardened gVisor sandboxes on the sandbox network. A compromised Syntaxis server could therefore run more sandboxes within the same limits, but not get a privileged container or the host filesystem.

## The terminal

The optional terminal is a shell in the same sandbox the agent uses, with the same limits and network rules. Only the owner can open it, it closes after 15 minutes idle, and admins can turn it off per user, level or group. The command blocklist applies to the agent only, so for a shell the egress mode is the control.

## Files, attachments, notes and hooks

- **Files and attachments** go through the session's own sandbox and are confined to `/workspace`; a path outside it is refused for every operation. Uploads are limited to 20 MB, only the owner can see or change them, and PDFs and pictures are parsed inside the sandbox (never on the host). What a document says is marked as untrusted data. The list of attached files in a message only counts at the very end of the message and only for the attachment folder, so text in a message or a tool's output cannot make the agent or the server open other files.
- **Notes** outlive a session, so a model steered by a web page could try to plant instructions for the next one. Each note is shown for approval (except in fully autonomous sessions, where it is audited), is scanned for secrets, is capped in size and number, and reaches the model as information inside a block that cannot be closed from inside a note.
- **Hooks** from a repository run commands without asking, so a hook that the approval policy would block or ask about is never run, hooks do not run while the agent is only planning, and every hook run is audited. An admin can switch repository hooks off (`repo_hooks`).
- **Web search** queries are scanned for secrets before they leave, the server makes the request with the address-checking client, and results are untrusted.

## Prompt injection

Text the model reads can try to steer it ([prompt injection](https://en.wikipedia.org/wiki/Prompt_injection)). Syntaxis labels such content as untrusted and keeps its rules in code, which limits the damage but does not remove the risk: a manipulated model can still do harmful things it is allowed to do. Use `ask` autonomy and a restricted egress mode on untrusted repositories.

## Keys, privacy and supply chain

- Back up the **master key** separately; without it stored secrets and 2FA cannot be recovered.
- Base images are pinned and kept current by Dependabot; releases are signed and carry an SBOM; CI runs `govulncheck` and `npm audit`.
- Keep Docker Engine itself up to date.

## Known gaps

- gVisor escape resistance has not been tested here.
- The workspace size limit is enforced by measuring, not by the filesystem: every minute normally, every 10 seconds above half the limit and every 3 seconds above 90%. A sandbox is slowed when it writes 100 MB/s for a while and stopped at 125% of the limit, so a fast writer can overshoot for a few seconds. Hard limits need a filesystem quota or a fixed-size volume; see [architecture](architecture.md#why-sandboxes-are-containers-with-gvisor-and-how-big-a-workspace-can-get) for why neither is used.
- A wrong master key is not noticed at start-up: secrets simply fail to decrypt. Keep the key with every backup.
