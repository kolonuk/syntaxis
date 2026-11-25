# Integrations

## Secrets and connections

Each user keeps their own credentials, encrypted. Nobody else can see or use them, admins included. They are placed in the environment of that user's own commands only and are never sent to the model. A change applies to the next command.

- **Settings → Secrets**: any token you want available as an environment variable.
- **Settings → Connections**: guided setup for GitHub, GitLab (including self-hosted), Gitea/Forgejo, Cloudflare, AWS, Azure, Google Cloud and IFTTT. Each is checked when saved and has a short guide to creating a least-privilege, short-lived credential. `git`, `gh`, `aws`, `az`, `gcloud` and `wrangler` then work in the sandbox. For anything else, add its token as a secret.

Use accounts and projects that belong to you, with a budget alert. The model sees command output, so anything a command prints can reach it. Redaction is best effort.

## Starting from a repository

In **New session**, pick a repository from a connected git service, or create one. Syntaxis clones it into the sandbox on its own working branch, leaving your branches untouched. The agent commits, pushes and opens a pull request (merge request on GitLab) using your connection. The token never appears in a command the model writes. Commits use your account name, not your sign-in email.

**Environments.** If an admin has approved sandbox environments, pick one in **New session**. Otherwise Syntaxis reads the repository's `devcontainer.json`. If its image matches an approved environment the session uses it. Anything else falls back to the default environment with a note. The setup command (for example `npm install`) runs once after the clone, unless the session is set to ask before running anything, in which case the command is shown instead. Features, ports and building from a Dockerfile are not supported.

## Automations

An automation starts a session by itself, as its owner and within their limits. Set them up under **Automations**.

- **Schedule**: a [cron](https://en.wikipedia.org/wiki/Cron) expression and a time zone, at most every 5 minutes.
- **Watch**: runs when a source has something new. Sources: a GitHub release, a git branch or new tags, a Docker Hub image, an npm, PyPI or crates.io package, an RSS or Atom feed, a web page. Creating a watch records the current state, so it fires only for later changes. Watches are fetched through a client that refuses private addresses.
- **Webhook**: a secret address, shown once, that anything can call: an [IFTTT](https://ifttt.com/maker_webhooks) "Make a web request", a GitHub webhook, a script.
- **IFTTT out**: connect your Webhooks key and an automation can trigger one of your applets when it finishes.

What a watch finds or a webhook sends reaches the agent as untrusted information, never as instructions.

Limits: 20 automations per user, 48 runs per automation per day, one run at a time. An automation that fails five times in a row is switched off.

## MCP servers

[MCP](https://modelcontextprotocol.io/) servers give the agent extra tools.

- An admin approves a server from the official registry, pinned to one exact version.
- Each user switches on the servers they want under **Settings → Tools**. A server runs only for users who did, with that user's own connected credentials.
- Each server runs in its own gVisor container with no workspace, no host access and only that user's credentials.
- Only servers that run as a local program are supported. Hosted services such as the official GitLab and Cloudflare servers are not.
- Server output is treated as untrusted, and tools that are not read-only ask the user first.

### Adding a server

In the admin app, open the **MCP servers** tab:

1. See what is approved now.
2. Choose **Add a server**: a recommended one, or a search of the public registry.
3. Review what the server can do and choose where each credential comes from: each user's own connected account (recommended), a shared credential, or a new shared credential made there. For example, map the official GitHub server's token to each user's GitHub connection.
4. Approve. A server can only be approved when pinned to one exact version.

Shared credentials are listed on the same tab.

### Servers shipped as container images

The official GitHub server is one. The image tag is the pinned version, for example `ghcr.io/github/github-mcp-server:2.2.0`.

Syntaxis cannot download images. Pull the image on the Docker host first, using the `docker pull …` command the review shows. **Approve** stays disabled until the image is present.

A tag is weaker than a digest, because the publisher can move it. For full pinning use an `@sha256:` reference.

## Cloud and security tools

- `images/sandbox-cloud` adds `gcloud`, `aws`, `az`, `wrangler`, `terraform`/`opentofu`, `kubectl`, `helm`, `flyctl` and `gh`.
- `images/sandbox-security` adds Trivy and `osv-scanner`, which run only when asked.

Sandboxes have no Docker socket, so building images inside them is not supported. Use a remote builder.
