# Getting started

About 20 minutes. You will end up with Syntaxis running on one Linux machine, connected to your own LLM server, with an invite link for your first user.

## What you need, and why

| You need | Why |
| --- | --- |
| **A Linux machine you control** with Docker Engine 24+ and Docker Compose v2 | Syntaxis runs as a container and starts one more container for each coding session. Windows and macOS are not supported, because gVisor needs Linux. |
| **gVisor** (`runsc`) registered with Docker | Agents run commands, and those commands run inside gVisor containers rather than directly on your machine. Syntaxis refuses to start sessions without it. |
| **An LLM server you already run**: Ollama, llama.cpp, LiteLLM or anything OpenAI-compatible | Syntaxis does not include a model. It sends work to yours and never installs, loads or removes models. |
| **nftables** (`nft`) on that machine | A firewall rule set stops sandboxes reaching your home network, your LLM server and cloud metadata. Strongly recommended. |
| *Optional:* **an SMTP server** | Used for invite and password-reset emails. Without one, the links are printed in the log instead. |
| *Optional:* **a reverse proxy with HTTPS** | Needed before anyone outside your network uses it. See [pitfalls](pitfalls.md#reverse-proxy-and-tls). |

## 1. Install gVisor

On the Docker host, as root:

```sh
git clone https://github.com/kolonuk/syntaxis && cd syntaxis
sh scripts/install-gvisor.sh
```

Check it worked:

```sh
docker run --rm --runtime=runsc hello-world
```

## 2. Build the sandbox image

This is the environment the agent works in (git, Node, Go, Python and common tools).

```sh
docker build -t syntaxis-sandbox:latest images/sandbox
```

## 3. Create your settings

```sh
cp .env.example .env
docker run --rm ghcr.io/kolonuk/syntaxis keygen
```

Paste the output into `.env` as `SYNTAXIS_MASTER_KEY`, and set `SYNTAXIS_BASE_URL` to the address your users will open.

**Back the key up somewhere separate.** It encrypts stored secrets and 2FA settings, and they cannot be recovered without it.

Every other setting is optional; see [configuration](configuration.md).

## 4. Start Syntaxis and create an admin

```sh
docker compose up -d
docker compose exec syntaxis syntaxis admin create you@example.com "Your Name"
```

Open `http://127.0.0.1:8081`, sign in and set up two-factor authentication when asked.

The admin app is only reachable from the machine itself by default. That is deliberate; keep it on your LAN or VPN.

## 5. Turn on the firewall

Sandboxes can reach the internet but must not reach your network. Generate the rules and apply them on the host, using your LLM server's address:

```sh
docker compose exec syntaxis syntaxis egress-rules 172.30.0.0/24 172.30.0.2 172.30.0.1 <llm-server-ip>/32 | sudo nft -f -
```

Then add the same `<llm-server-ip>/32` to `SYNTAXIS_EGRESS_BLOCK` in `.env` and run `docker compose up -d` again. Re-apply the rules after a reboot or firewall reload; your distribution's nftables service can do that automatically. Details: [security](security.md#egress).

## 6. Connect your LLM server

In the admin app, open **Providers & models → Add provider** and enter your server's address. Set **concurrency** to how many requests your server handles at once, click **Discover models**, and enable the ones your users may use.

## 7. Invite a user

Go to **Users → Invite user**. They get a single-use link to set their own password. Without email configured, find the link with `docker compose logs syntaxis`.

They sign in at `http://<your-host>:8080`, start a session and describe a task.

## 8. Check everything

```sh
docker compose exec syntaxis syntaxis doctor
```

Anything wrong is listed with how to fix it. The same checks appear on the admin **Health** page.

## Next

- Something not working: [pitfalls and troubleshooting](pitfalls.md)
- Put it online safely: [reverse proxy and TLS](pitfalls.md#reverse-proxy-and-tls) and [security](security.md)
- Tune limits and options: [configuration](configuration.md)
