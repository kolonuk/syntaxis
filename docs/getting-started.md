# Getting started

Allow about 15 minutes, mostly image builds. The result is Syntaxis running on one Linux machine, connected to your LLM server, with a firewall keeping sandboxes off your network.

## Requirements

| Requirement | Notes |
| --- | --- |
| A Linux machine you control: Debian, Ubuntu, Raspberry Pi OS, AlmaLinux, Rocky Linux, RHEL or Fedora, x86-64 or arm64 | Docker Engine 24+ and Docker Compose v2. The installer adds Docker if missing. Windows and macOS are not supported (gVisor needs Linux). |
| gVisor (`runsc`) registered with Docker | Sessions will not start without it. The installer adds it. |
| An LLM server: Ollama, llama.cpp, LiteLLM or anything OpenAI-compatible | Syntaxis includes no model. It never installs, loads or removes models. |
| `nftables` and systemd | Apply the firewall that blocks sandboxes from your network, the Docker host and your LLM server, and keep it applied across reboots. |
| Optional: an SMTP server | Invite and password-reset emails. Without one the links are printed in the log. |
| Optional: a domain pointing at the machine, ports 80 and 443 open | HTTPS with automatic certificates. Needed before anyone outside your network uses it. |

## 1. Install

On the Docker host:

```sh
git clone https://github.com/kolonuk/syntaxis && cd syntaxis
sudo sh scripts/install.sh --url http://<this-host>:8080 --admin-email you@example.com --llm <llm-server-address>
```

The installer checks the requirements, offers to install what is missing (`--check` prints the plan and changes nothing), writes `.env` with a new master key, builds the images, starts Syntaxis, installs the firewall units and prints a single-use admin link. Running it again keeps your settings and data.

Options (full list: `sh scripts/install.sh --help`):

- `--llm` (repeatable): the LLM server's address or hostname. Sandboxes are blocked from reaching it directly.
- `--https syntaxis.example.com`: add a Caddy front end that obtains and renews certificates for the domain.
- `--version v1.2.3`: use published, version-pinned images instead of building from source.
- `--auto-update`: update weekly, with a database backup and automatic rollback.

Back up the master key (`SYNTAXIS_MASTER_KEY` in `.env`) separately. Stored secrets and 2FA settings cannot be recovered without it.

## 2. Sign in as admin

Open the printed link (valid once, for 24 hours), choose a password and set up two-factor authentication.

The admin app listens only on the machine itself, `http://127.0.0.1:8081`. To use it from elsewhere, open an SSH tunnel, or set `SYNTAXIS_ADMIN_BIND` to a LAN or VPN address.

The admin menu has five groups, each with its own row of tabs:

| Group | Tabs |
| --- | --- |
| Health & updates | Health, Updates |
| Users | Users, Levels & groups, Admins |
| Models & tools | Providers & models, Benchmarks, Environments, MCP servers |
| Sessions & egress | Sessions, Egress, Policies |
| System | Configuration, Audit, Kill switch |

## 3. Connect your LLM server

Open **Models & tools → Providers & models → Add provider** and enter the server's address. Set **concurrency** to the number of requests the server handles at once, click **Discover models**, and enable the models users may use.

## 4. Invite a user

Open **Users → Invite user**. The invite is emailed, and the same single-use link is shown on screen with a **Copy link** button for sending it yourself. The link is valid for 3 days and lets whoever holds it set the account's password, so send it privately. **Resend** on a pending user issues a fresh link.

The user signs in at the `--url` address, starts a session and describes a task.

## 5. Check the install

The admin **Health** page lists problems with fixes, including whether the firewall is loaded (a test sandbox tries to reach the host). From the command line:

```sh
docker compose exec syntaxis syntaxis doctor
```

## Updating

**Updates** in the admin app shows the running version, the release stream you follow and what each stream would install. A banner and an **Update available** button appear in the header when a newer release exists.

| Stream | Follows | Use for |
| --- | --- | --- |
| Stable (default) | The newest published release that is not a pre-release | An instance other people depend on |
| Preview | The newest published release of any kind, including release candidates and betas. Moves to the final release when one catches up | Getting changes sooner |
| Edge | The main branch, built from source on the host | Unreleased changes |

You can switch stream, update to the newest version in your stream, or pin an exact version (a pre-release version puts you on Preview). Installing an older version is allowed and the page warns you: a newer version may have changed the database in a way the older one cannot open. The weekly automatic update (`--auto-update`) stays inside your stream and never goes backwards.

An update backs up the database, installs the version, restarts (running sessions stop, workspaces are kept) and waits for the new version to report healthy. If it does not, the old version and its data are restored.

The update runs through `syntaxis-update-agent`, a service the installer sets up on the Docker host, because only the host can pull images and restart containers. Syntaxis sends it only a stream, a version number or an example environment name to build, never a command. If the service is not running the page says so. Start it with:

```sh
sudo systemctl start syntaxis-update-agent
```

If Syntaxis runs in a container and the update service is not running, the top of the Updates page warns that an update made by hand is temporary: deploying the container again from your Compose file or Kubernetes manifest returns it to the image written there. To keep an update, change the image version in that file and deploy it.

After updating from 0.1.3 or earlier, restart the service once so it picks up the new script and can switch streams: `sudo systemctl restart syntaxis-update-agent`. Later updates restart it themselves.

To update by hand:

```sh
sudo sh scripts/update.sh                       # update within the current stream
sudo sh scripts/update.sh --stream stable       # follow the newest stable release
sudo sh scripts/update.sh --stream preview      # follow the newest release, pre-releases included
sudo sh scripts/update.sh --stream edge         # follow the main branch
sudo sh scripts/update.sh --release v1.4.0      # one version (a pre-release tag puts you on preview)
sudo sh scripts/update.sh --dry-run --stream preview   # print the plan, change nothing
```

The stream is stored as `SYNTAXIS_CHANNEL` in `.env`. The running version is shown in the apps, at `/api/version` and by `syntaxis version`. Release checks use GitHub's public API for the project's releases and branch, at most once an hour; nothing else is sent.

### Deployed by another tool

If you run Syntaxis from your own compose stack or deployment tool, the in-app update button does not apply, because there is no `syntaxis-update-agent` and no checkout. Change the image versions and redeploy instead. The database migrates on start, so run `syntaxis backup` first. The Updates page still shows what is available.

Building example environments from the admin app needs the same host service. Without it, build the image on the host and add it under **Environments → Add or replace an environment**.

To offer both, run `scripts/update-agent.sh` as a service on the Docker host from a directory containing the project's `scripts/` and `images/templates/`, and a `.env` with `SYNTAXIS_FIREWALL_TOKEN`, `SYNTAXIS_ADMIN_BIND` and `SYNTAXIS_ADMIN_PORT`.

## Installing by hand

These are the steps the installer automates:

1. Install gVisor with `sh scripts/install-gvisor.sh`, then check with `docker run --rm --runtime=runsc hello-world`.
2. Build the sandbox image: `docker build -t syntaxis-sandbox:latest images/sandbox`.
3. Run `cp .env.example .env`. Set `SYNTAXIS_MASTER_KEY` (from `docker run --rm ghcr.io/kolonuk/syntaxis keygen`), `SYNTAXIS_BASE_URL`, and your LLM server's address in `SYNTAXIS_EGRESS_BLOCK` (for example `192.0.2.10/32`).
4. Run `docker compose up -d`.
5. Apply the firewall with `sudo sh scripts/apply-egress.sh`. Re-apply it after every reboot, and keep `scripts/firewall-sync.sh` running (it needs `SYNTAXIS_FIREWALL_TOKEN` in `.env`). The units in `deploy/systemd` do both; replace `@ROOT@` with the checkout path.
6. Run `docker compose exec syntaxis syntaxis admin setup-link you@example.com "Your Name"` and open the link.

## See also

- [Troubleshooting](pitfalls.md)
- [Reverse proxy and TLS](pitfalls.md#reverse-proxy-and-tls) and [security](security.md)
- [Configuration](configuration.md)
