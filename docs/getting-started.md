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

Go to **Users → Invite user**. The invite is emailed to them, and the same single-use link is shown to you on screen with a **Copy link** button, so you can send it yourself (a message, a chat) if email is not set up or the mail goes astray. The link is valid for 3 days, and anyone who has it can set that account's password, so pass it on privately. **Resend** on a pending user gives a fresh link the same way.

They sign in at the address you gave with `--url`, start a session and describe a task.

## 5. Check everything

The admin **Health** page lists anything wrong with how to fix it, including whether the firewall is really loaded (a test sandbox tries to reach the host). The same checks from the command line:

```sh
docker compose exec syntaxis syntaxis doctor
```

## Updating

In the admin app, **Updates** shows what is running, which **release stream** you follow, and what each stream would install now. A banner and an **Update available** button in the header appear when a newer release is out.

A stream is how soon you want changes:

| Stream | What it follows | Use it for |
| --- | --- | --- |
| **Stable** (the default) | The newest published release that is not a pre-release. | An instance other people depend on. |
| **Preview** | The newest published release of any kind, release candidates and betas included. When a final release catches up, preview moves to it. | Getting changes sooner and helping find problems. |
| **Edge** | The main branch, built from source on your host. | Trying things before they are released: newest fixes, newest mistakes. |

You can switch stream at any time, update to the newest version in your stream, or pin one exact version (a pre-release puts you on Preview). Switching to an older version is allowed and the page warns you, because a newer version may have changed the database in a way an older one cannot open. The weekly automatic update, if you installed it (`--auto-update`), stays inside your stream and never goes backwards.

Syntaxis backs up its database, installs the version, restarts (running sessions stop, workspaces are kept) and waits for it to report healthy. If it does not, the old version and its data are put back automatically.

The installer sets up a small service on the Docker host that does this (`syntaxis-update-agent`), because only the host can pull images and restart containers. Syntaxis never sends it a command: it only accepts a stream, a version number, or an example environment to build. If the service is not running the page says so; start it with `sudo systemctl start syntaxis-update-agent`, or update by hand:

```sh
sudo sh scripts/update.sh                       # update within the stream you follow
sudo sh scripts/update.sh --stream stable       # follow the newest stable release
sudo sh scripts/update.sh --stream preview      # follow the newest release, pre-releases included
sudo sh scripts/update.sh --stream edge         # follow the main branch
sudo sh scripts/update.sh --release v1.4.0      # one version (a pre-release tag puts you on preview)
sudo sh scripts/update.sh --dry-run --stream preview   # say what it would do, change nothing
```

The stream is recorded as `SYNTAXIS_CHANNEL` in `.env`. The running version is shown in the apps, at `/api/version` and by `syntaxis version`. To check for releases Syntaxis asks GitHub's public API for the project's releases and branch (at most once an hour); nothing else is sent.

### When something else deploys Syntaxis

If you run Syntaxis from your own compose stack or another deployment tool, the in-app update button does not apply: there is no `syntaxis-update-agent` and no checkout for it to work from. Update there instead (change the image versions and redeploy; the database is migrated on start, so take a backup first with `syntaxis backup`). The Updates page still shows what is available. Building the example environments from the admin app needs the same host service; without it, build an image yourself on the host and add it under **Environments → Add or replace an environment**. A deployment tool that wants to offer both can run `scripts/update-agent.sh` as a service on the Docker host, from a directory holding the project's `scripts/` and `images/templates/` and a `.env` with `SYNTAXIS_FIREWALL_TOKEN`, `SYNTAXIS_ADMIN_BIND` and `SYNTAXIS_ADMIN_PORT`.

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
