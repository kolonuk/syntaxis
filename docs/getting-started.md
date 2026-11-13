# Getting started

> Verified on Debian 12 (x86-64) with Docker 28 and gVisor release-20261109: sandbox isolation, fork-bomb containment, the egress firewall and a full agent run are exercised by the integration tests (`go test -run Integration ./internal/...`, also run in CI). Run `syntaxis doctor` after each step.

## Requirements

- A Linux Docker host (x86-64 or arm64) you control, with Docker Engine 24+ and Docker Compose v2
- [gVisor](https://gvisor.dev) (`runsc`) registered as a Docker runtime
- An LLM endpoint you already run: Ollama, llama.cpp server, LiteLLM, or any OpenAI-compatible API
- `nft` (nftables) on the Docker host if you want the egress firewall (strongly recommended)
- Optional: an SMTP relay for invite and reset emails (without one, emails are written to the log)

## Install gVisor (`runsc`)

Run as root on the Docker host (review [scripts/install-gvisor.sh](../scripts/install-gvisor.sh) first):

```sh
sh scripts/install-gvisor.sh
```

By hand, that is:

```sh
curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" > /etc/apt/sources.list.d/gvisor.list
apt-get update && apt-get install -y runsc
runsc install            # registers the runtime in /etc/docker/daemon.json
systemctl restart docker
docker run --rm --runtime=runsc hello-world
```

Check it is registered: `docker info --format '{{json .Runtimes}}'` must list `runsc`. Syntaxis **refuses to start a sandbox without it** and will not fall back to plain `runc`.

Limitations to know about now (details in [pitfalls](pitfalls.md#gvisor-limitations)): some syscalls are unsupported, file and network I/O are slower, there is no GPU passthrough and no nested virtualisation.

## Build the sandbox image

```sh
docker build -t syntaxis-sandbox:latest images/sandbox
# optional variants
docker build -t syntaxis-sandbox:cloud --build-arg BASE=syntaxis-sandbox:latest images/sandbox-cloud
docker build -t syntaxis-sandbox:security --build-arg BASE=syntaxis-sandbox:latest images/sandbox-security
```

To use your own image set `SYNTAXIS_SANDBOX_IMAGE`. It needs: a non-root user with uid 10001, a POSIX shell, git; `ripgrep` and `curl` are used by agent tools.

## Configure

```sh
cp .env.example .env
syntaxis keygen            # or: docker run --rm ghcr.io/kolonuk/syntaxis keygen
```

Put the key in `.env` as `SYNTAXIS_MASTER_KEY` (or mount a file and set `SYNTAXIS_MASTER_KEY_FILE`; keep it **outside** the data directory). **Back this key up separately**: without it, stored secrets, 2FA secrets and signing keys cannot be decrypted. Set `SYNTAXIS_BASE_URL` to the public HTTPS URL users will use (it goes into emails). All options are in [configuration.md](configuration.md).

## Start

```sh
docker compose up -d
docker compose exec syntaxis syntaxis admin create you@example.com "Your Name"
```

The admin app listens on `127.0.0.1:8081` (loopback by default). Sign in, then **2FA setup is mandatory** before anything else works. Put the chat app (`:8080`) behind a TLS reverse proxy ([pitfalls](pitfalls.md#reverse-proxy-and-tls)).

## Apply the sandbox firewall

Sandboxes join a dedicated Docker network. The egress rules that block the host, LAN, cloud metadata, the LLM server and outbound SMTP are generated from your configuration and applied on the **Docker host** (the container cannot, and should not, do this):

```sh
docker network create syntaxis-sandbox --opt com.docker.network.bridge.enable_icc=false   # Syntaxis also creates it on first use
sh scripts/apply-egress.sh syntaxis-sandbox 172.30.0.2 203.0.113.5/32   # resolver IP from docker-compose.yml, then your LLM server's CIDR(s)
export SYNTAXIS_NETWORK_RULES_APPLIED=true                        # lets the health check know
```

Docker Compose already puts Syntaxis on the sandbox network at `172.30.0.2`, where its DNS resolver listens (domain policy, rebinding protection, query log). Set `SYNTAXIS_EGRESS_BLOCK` to your LLM server's CIDR so sandboxes cannot resolve or reach it. Re-run after changing the egress mode or the LLM server's address, and after a firewall reload/reboot (persist with your distribution's nftables service). See [security.md](security.md#egress).

## Connect your LLM server

In the admin app: **Providers & models → Add provider**, enter the base URL (for example `http://llm.internal:11434`), set **concurrency to match your server's parallel setting**, click **Discover models**, enable the models users may use, then fix whatever the **Health** page reports (typically context size and concurrency).

Syntaxis only reads from the LLM server. If sandboxes must not reach it directly, pass its address to the firewall step above; sandboxes use it only through Syntaxis.

## Invite users

**Users → Invite user.** They receive a single-use link, set their own password, and are reminded at every login until they turn on 2FA. Without SMTP configured the link is printed in the Syntaxis log (`docker compose logs syntaxis`).

## Check everything

```sh
docker compose exec syntaxis syntaxis doctor
```

Each finding says what is wrong and how to fix it. Run without Compose as `syntaxis serve` (set the same environment variables).
