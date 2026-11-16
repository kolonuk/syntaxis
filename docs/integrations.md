# Integrations

## Secrets and connections

Each user keeps their own credentials, encrypted, and nobody else can see or use them, admins included. They are put into the environment of that user's own commands only, and never sent to the model. Changes apply to the very next command.

- **Settings → Secrets:** any token you want available as an environment variable.
- **Settings → Connections:** guided setup for **GitHub, GitLab (including self-hosted), Gitea/Forgejo, Cloudflare, AWS, Azure, Google Cloud** and **IFTTT**. Each is checked when saved and comes with a short guide to creating a least-privilege, short-lived credential. The usual tools (`git`, `gh`, `aws`, `az`, `gcloud`, `wrangler`) then just work in the sandbox. Anything else works the same way: add its token as a secret.

Use accounts and projects that belong to you, with a budget alert. The model sees command output, so anything a command prints can reach it; redaction is best effort.

## Starting from a repository

In **New session**, pick a repository from a connected git service, or create a new one. Syntaxis clones it into the sandbox on its own working branch, so your branches are untouched. The agent commits there, pushes, and opens a pull request (merge request on GitLab) with your own connection; the token is never in a command the model writes. Commits use your account name, not your sign-in email.

## Automations

An automation starts a session by itself, as its owner and within their limits. Set them up under **Automations**.

- **Schedule:** a [cron](https://en.wikipedia.org/wiki/Cron) expression and a time zone, at most every 5 minutes.
- **Watch:** checks a source and runs when something is new: a GitHub release, a git branch or new tags, a Docker Hub image, an npm, PyPI or crates.io package, an RSS or Atom feed, or a web page. Creating one remembers the current state, so it only fires for what is new. Watches are fetched through a client that refuses private addresses.
- **Webhook:** a secret address (shown once) that anything can call: an [IFTTT](https://ifttt.com/maker_webhooks) "Make a web request", a GitHub webhook, a script.
- **IFTTT out:** connect your Webhooks key and an automation can trigger one of your applets when it finishes.

Whatever a watch found or a webhook sent reaches the agent as untrusted information, never as instructions. Limits: 20 automations per user, 48 runs per automation per day, one run at a time, and an automation that fails five times in a row is switched off.

## MCP servers

[MCP](https://modelcontextprotocol.io/) servers give the agent extra tools. Admins browse the official registry, review a server, and approve it with a pinned version and the credentials it needs. Each run starts the approved servers in their **own gVisor containers**: no workspace, no host access, only the credentials mapped to them. Their output is treated as untrusted, and tools that are not read-only ask the user first.

## Cloud and security tools

The optional `images/sandbox-cloud` image adds `gcloud`, `aws`, `az`, `wrangler`, `terraform`/`opentofu`, `kubectl`, `helm`, `flyctl` and `gh`; `images/sandbox-security` adds Trivy and `osv-scanner`, which run only when asked. Sandboxes have no Docker socket, so building images inside them is not supported; use a remote builder.
