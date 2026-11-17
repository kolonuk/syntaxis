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

## About the name

*Syntaxis* is the Greek word (σύνταξις) for "arranging things together", and the origin of the English word *syntax*. That is the project in one word: a place where code is written, where models, users, quotas and sandboxes are arranged into one orderly system, and where every session gets its own well-defined place to work.

## What you get

- A phone-friendly chat app with live command output, approvals and a context meter
- An agent that edits files, runs commands, uses git and can load project-specific skills
- Sessions that start from your own GitHub, GitLab or Gitea repository, work on their own branch and open pull requests
- Approval gates for risky actions, with three levels of autonomy, and a plan-first mode where nothing changes until you approve
- Checkpoints you can roll back, without touching your own git history
- Attach files, pictures and PDFs (scans are read with OCR), browse and edit the workspace in a Files tab, and follow the agent's task list as it works
- Slash commands, hooks, notes the agent keeps between sessions, optional web search, and edit, regenerate or branch a conversation
- Several attempts at one task side by side, with a comparison to keep the best
- Automations that start a session on a schedule, a webhook or when something new appears (a release, a feed, a package)
- Connections to GitHub, GitLab, Gitea, Cloudflare, AWS, Azure, Google Cloud and IFTTT, each user's own
- Disposable, read-only links to share a session, which you can name and revoke
- Sandbox environments (for example Rust or Java) that admins approve and that a repository's `devcontainer.json` can pick
- A secrets vault, secret scanning and optional signed commits
- Network controls that keep sandboxes away from your LAN and your LLM server
- MCP server support, run in their own sandboxes
- A health page that explains problems in plain language

## Quick start

You need Linux with Docker and an LLM server you already run. On the Docker host, as root:

```sh
git clone https://github.com/kolonuk/syntaxis && cd syntaxis
sudo sh scripts/install.sh --url http://<this-host>:8080 --admin-email you@example.com --llm <llm-server-address>
```

The installer adds gVisor if it is missing, generates the keys, starts everything, keeps the sandbox firewall applied across reboots, and prints a single-use link for your admin account. The [getting started guide](docs/getting-started.md) explains each step, the options (HTTPS, pinned versions, automatic updates) and how to do it by hand.

## Documentation

[Getting started](docs/getting-started.md) · [Configuration](docs/configuration.md) · [Security](docs/security.md) · [Architecture](docs/architecture.md) · [The agent](docs/agent.md) · [Integrations](docs/integrations.md) · [Troubleshooting](docs/pitfalls.md)

## Project

v1 is under active development; [todo.md](todo.md) lists the work that is left. Issues and pull requests are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately: [SECURITY.md](SECURITY.md). Changes are in the [changelog](CHANGELOG.md).

Licensed GPL-2.0-or-later. See [LICENSE](LICENSE).
