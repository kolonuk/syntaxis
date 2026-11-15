# Getting started

About 15 minutes, most of it image builds. You will end up with Syntaxis running on one Linux machine, connected to your own LLM server, with a firewall that keeps sandboxes off your network.

## What you need, and why

| You need | Why |
| --- | --- |
| **A Linux machine you control**: Debian, Ubuntu, Raspberry Pi OS, AlmaLinux, Rocky Linux, RHEL or Fedora, on x86-64 or arm64, with Docker Engine 24+ and Docker Compose v2 | Syntaxis runs as containers and starts one more container for each coding session. The installer adds Docker if it is missing. Windows and macOS are not supported, because gVisor needs Linux. |
| **gVisor** (`runsc`) registered with Docker | Agents run commands inside gVisor containers rather than directly on your machine. Syntaxis refuses to start sessions without it. The installer adds it. |
| **An LLM server you already run**: Ollama, llama.cpp, LiteLLM or anything OpenAI-compatible | Syntaxis does not include a model. It sends work to yours and never installs, loads or removes models. |
| **nftables** (`nft`) and **systemd** | The firewall that stops sandboxes reaching your network, the Docker host and your LLM server, kept applied across reboots. |
| *Optional:* **an SMTP server** | For invite and password-reset emails. Without one, the links are printed in the log. |
| *Optional:* **a domain pointing at the machine**, with ports 80 and 443 open | For HTTPS with automatic certificates, needed before anyone outside your network uses it. |

## 1. Install

On the Docker host, as root:

```sh
git clone https://github.com/kolonuk/syntaxis && cd syntaxis
sudo sh scripts/install.sh --url http://<this-host>:8080 --admin-email you@example.com --llm <llm-server-address>
```

The installer checks the requirements, shows what is missing and installs Docker, nftables and gVisor (it asks first; `--check` shows the plan and changes nothing), writes `.env` with a new master key, builds the images, starts Syntaxis, installs the firewall units and prints a single-use link for your admin account. It is safe to run again; it keeps your settings and data.

Useful options (all of them: `sh scripts/install.sh --help`):

- `--llm` (repeatable): your LLM server's address or hostname. Sandboxes are blocked from reaching it directly.
- `--https syntaxis.example.com`: add a Caddy front end that gets and renews certificates for that domain.
- `--version v1.2.3`: use published, version-pinned images instead of building from source.
- `--auto-update`: update weekly, with a database backup and automatic rollback.

**Back up the master key** (`SYNTAXIS_MASTER_KEY` in `.env`) somewhere separate. It encrypts stored secrets and 2FA settings, and they cannot be recovered without it.

## 2. Sign in as admin

Open the link the installer printed (it works once, for 24 hours), choose a password and set up two-factor authentication.

The admin app is only reachable from the machine itself (`http://127.0.0.1:8081`). That is deliberate; to administer from elsewhere, use an SSH tunnel, or set `SYNTAXIS_ADMIN_BIND` to a LAN or VPN address.

## 3. Connect your LLM server

In the admin app, open **Providers & models → Add provider** and enter your server's address. Set **concurrency** to how many requests your server handles at once, click **Discover models**, and enable the ones your users may use.

## 4. Invite a user

Go to **Users → Invite user**. They get a single-use link to set their own password. Without email configured, find the link with `docker compose logs syntaxis`.

They sign in at the address you gave with `--url`, start a session and describe a task.

## 5. Check everything

The admin **Health** page lists anything wrong with how to fix it, including whether the firewall is really loaded (a test sandbox tries to reach the host). The same checks from the command line:

```sh
docker compose exec syntaxis syntaxis doctor
```

## Updating

```sh
sudo sh scripts/update.sh
```

It backs up the database, pulls (source installs) or downloads (pinned installs) the new version, restarts, and rolls back the image and database if the new version does not report healthy. The running version is shown in the apps, at `/api/version` and by `syntaxis version`.

## Installing by hand

The installer only automates these steps, if you prefer to run them yourself:

1. Install gVisor: `sh scripts/install-gvisor.sh` (any supported distribution), then check with `docker run --rm --runtime=runsc hello-world`.
2. Build the sandbox image: `docker build -t syntaxis-sandbox:latest images/sandbox`.
3. `cp .env.example .env`, set `SYNTAXIS_MASTER_KEY` (from `docker run --rm ghcr.io/kolonuk/syntaxis keygen`), `SYNTAXIS_BASE_URL`, and your LLM server's address in `SYNTAXIS_EGRESS_BLOCK` (for example `192.0.2.10/32`).
4. `docker compose up -d`.
5. Apply the firewall: `sudo sh scripts/apply-egress.sh`. It must be re-applied after every reboot, and `scripts/firewall-sync.sh` must keep running (it needs `SYNTAXIS_FIREWALL_TOKEN` in `.env`). The units in `deploy/systemd` do both (replace `@ROOT@` with the checkout path).
6. `docker compose exec syntaxis syntaxis admin setup-link you@example.com "Your Name"` and open the link.

## Next

- Something not working: [pitfalls and troubleshooting](pitfalls.md)
- Put it online safely: [reverse proxy and TLS](pitfalls.md#reverse-proxy-and-tls) and [security](security.md)
- Tune limits and options: [configuration](configuration.md)
