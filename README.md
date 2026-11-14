# Syntaxis

[![CI](https://github.com/kolonuk/syntaxis/actions/workflows/ci.yml/badge.svg)](https://github.com/kolonuk/syntaxis/actions/workflows/ci.yml)
[![Licence: GPL v2+](https://img.shields.io/badge/licence-GPL--2.0--or--later-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/kolonuk/syntaxis?include_prereleases)](https://github.com/kolonuk/syntaxis/releases)

**Sandboxed AI coding sessions on the models you already run.**

Syntaxis lets a small, trusted group use your self-hosted LLM for real software work. Each person gets chat sessions where an agent writes code, runs commands and uses git, all inside its own isolated sandbox. You decide who gets in, which models they can use and how much they can spend. It is one self-hosted service in front of Ollama, llama.cpp, LiteLLM or any OpenAI-compatible server.

> Not for the open internet. There are no sign-ups: an admin creates every account. Run it behind HTTPS and a reverse proxy.

## Why another LLM UI?

Most chat UIs for local models stop at conversation. Coding agents can do real work, but they normally run as a command-line tool on your own machine, with your files and your network within reach. Syntaxis sits between the two:

- **It runs the code, safely.** Every session gets its own gVisor sandbox, so friends or colleagues can use the agent without a shell on your host.
- **It is built for one shared GPU.** Per-user quotas and a fair queue stop one person from using all of your model's capacity.
- **It is built for local models.** It copes with weaker tool calling and small context windows, and it never loads or swaps models behind your back.
- **You stay in control.** Admins and users have separate apps, risky actions wait for approval, and everything important is audited.

It is not a hosted service, and a gVisor container is not a virtual machine. The honest limits are in [security](docs/security.md).

## What you get

- A phone-friendly chat app with live command output, approvals and a context meter
- An agent that edits files, runs commands, uses git and can load project-specific skills
- Approval gates for risky actions, with three levels of autonomy
- Checkpoints you can roll back, without touching your own git history
- A secrets vault, secret scanning and optional signed commits
- Network controls that keep sandboxes away from your LAN and your LLM server
- MCP server support, run in their own sandboxes
- A health page that explains problems in plain language

## Quick start

You need Linux with Docker, [gVisor](https://gvisor.dev) and an LLM server you already run.

```sh
git clone https://github.com/kolonuk/syntaxis && cd syntaxis
sh scripts/install-gvisor.sh
docker build -t syntaxis-sandbox:latest images/sandbox
cp .env.example .env        # add a key from: docker run --rm ghcr.io/kolonuk/syntaxis keygen
docker compose up -d
docker compose exec syntaxis syntaxis admin create you@example.com "Your Name"
```

Then open `http://127.0.0.1:8081`. The [getting started guide](docs/getting-started.md) explains each step and why it is needed.

## Documentation

[Getting started](docs/getting-started.md) · [Configuration](docs/configuration.md) · [Security](docs/security.md) · [Architecture](docs/architecture.md) · [The agent](docs/agent.md) · [Integrations](docs/integrations.md) · [Troubleshooting](docs/pitfalls.md)

## Project

v1 is under active development; [todo.md](todo.md) tracks what works and what is planned. Issues and pull requests are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately: [SECURITY.md](SECURITY.md). Changes are in the [changelog](CHANGELOG.md).

Licensed GPL-2.0-or-later. See [LICENSE](LICENSE).
