# Configuration

## Where a value comes from

Each option takes its first value from the Docker environment (the compose file or `.env`), falling back to the default. Most options can also be changed in the admin app under **Configuration**. A value saved there is stored in the database and replaces the environment's value.

Each option on that screen shows its source: **from Docker**, **saved here** or **default**. Remove a saved value ("use the Docker value again") to go back to the environment's value. While a saved value exists, changing the same variable in Docker has no effect.

Deployment options (listen addresses, data directory, Docker proxy, sandbox image and network, DNS, master key, host service token) are shown but can only be changed where Syntaxis is deployed. The master key and token are never displayed, only whether they are set.

Options marked *live* apply at once. The others apply after a restart; the screen lists the ones waiting.

Write-only values such as the mail password are stored encrypted as instance secrets with a reserved `SYNTAXIS_` name. MCP servers can never be given these. Quotas, uploads and web search are set on the **Policies** screen.

## Environment variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `SYNTAXIS_LISTEN` | `127.0.0.1:8080` | Chat app listen address |
| `SYNTAXIS_ADMIN_LISTEN` | `127.0.0.1:8081` | Admin app listen address. Keep it on loopback, LAN or VPN; the health check reports an error if it is public |
| `SYNTAXIS_ADMIN_URL` | – | Where admins open the admin app. Adds an "Admin mode" link in the chat app for the `admin` level. The admin app's own sign-in and two-factor step still apply |
| `SYNTAXIS_BASE_URL` | `http://localhost:8080` | Public URL used in emails. `https://` turns on Secure cookies and HSTS |
| `SYNTAXIS_DATA_DIR` | `./data` | SQLite database and local state |
| `SYNTAXIS_MASTER_KEY` / `_FILE` | – | 32-byte base64 key (`syntaxis keygen`). Required for `serve`. Store it outside the data directory |
| `SYNTAXIS_LOG_LEVEL` / `_FORMAT` | `info` / `json` | Use `text` format for readable output in development |
| `SYNTAXIS_SMTP_HOST` `_PORT` `_USER` `_PASSWORD` `_FROM` | empty / 587 | Empty host: emails are logged, not sent. STARTTLS on 587 works; implicit TLS on 465 does not. Set under *Configuration → Email*, which has a test button; applies at once |
| `SYNTAXIS_DOCKER_HOST` | environment | The validating Docker proxy, `tcp://docker-socket-proxy:2375` (set by Compose) |
| `SYNTAXIS_SANDBOX_RUNTIME` | `runsc` | OCI runtime for sandboxes. Only gVisor runtimes are accepted |
| `SYNTAXIS_SANDBOX_IMAGE` | `syntaxis-sandbox:latest` | Sandbox image |
| `SYNTAXIS_SANDBOX_NETWORK` | `syntaxis-sandbox` | Dedicated Docker network |
| `SYNTAXIS_EGRESS_MODE` | `open` | `open`, `allowlist` or `none`. Overridable per user in the admin app |
| `SYNTAXIS_EGRESS_ALLOWLIST` | – | Comma-separated hostnames or CIDRs, used in `allowlist` mode |
| `SYNTAXIS_FIREWALL_TOKEN` | set by the installer | Shared secret for the host services (firewall sync and update service, `/internal/*` on the admin listener). Without it the DNS resolver applies a user's egress mode but the host firewall does not |
| `SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE` | – | Comma-separated private ranges the server may call for users' self-hosted GitLab or Gitea, for example `192.0.2.0/24`. Without it only public addresses are reachable. Link-local and cloud metadata addresses are never allowed |
| `SYNTAXIS_PASSWORD_ROTATION_DAYS` | `30` | Users without 2FA must change their password this often. `0` disables |
| `SYNTAXIS_SECRET_SCAN` | `warn` | `off`, `warn` or `block` |
| `SYNTAXIS_ALLOW_USER_OVERRIDE` | `true` | Whether a user may confirm a push despite scanner findings (audited) |
| `SYNTAXIS_SIGNING_MODE` | `off` | SSH commit signing: `off`, `optional` or `required` |
| `SYNTAXIS_IDLE_MINUTES` | `30` | Stop sandboxes idle this long |
| `SYNTAXIS_SANDBOX_MAX_AGE_DAYS` | `7` | Destroy unused sandbox containers after this many days |
| `SYNTAXIS_CPU_MINUTES_DAILY` | `240` | Core-minutes of CPU a user's sandboxes may use per day. `0` is unlimited. See [troubleshooting](pitfalls.md#resource-limits) |
| `SYNTAXIS_WORKSPACE_RETENTION_DAYS` | `30` | Delete a user's workspace after this many days unused. `0` keeps it |
| `SYNTAXIS_PROXY_AUTH_ENABLED` | `false` | Trusted reverse-proxy header authentication |
| `SYNTAXIS_PROXY_AUTH_HEADER` / `_TRUSTED` | `X-Forwarded-User` / – | Header name, and the only proxy addresses or CIDRs whose headers are trusted |
| `SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` | – | Create the first admin on first boot. Prefer `syntaxis admin create`, and remove these after use |
| `SYNTAXIS_BOOTSTRAP_ADMIN_ALLOW_SHORT_PASSWORD` | `false` | Allow a password of 6 or more characters instead of 12 for the first admin created from the environment. Common passwords and the user's name are still refused, 2FA is still mandatory, and it affects only that account creation |
| `SYNTAXIS_BOOTSTRAP_PROVIDER_URL` | – | Add a model provider at this address if the instance has none. Related: `_NAME` (default `local-llm`), `_KIND` (`ollama` (default), `llamacpp`, `litellm`, `openai`), `_CONCURRENCY` (default 1), `_ENABLE_MODELS` (`all` enables every discovered chat model, a comma-separated list enables those names, empty leaves them off). Discovery retries while the model server starts. An instance that already has a provider is never changed |

## Levels, groups and per-user settings

Every chat user has a level (`user`, `poweruser`, `admin`) and may be in a group. Levels, groups and users carry the same settings: token quotas, concurrent sessions, daily compute minutes, egress mode, allowed models, and switches for the terminal and cloud tooling.

Set levels and groups under **Levels & groups**, and a user's level and group under **Users → Edit**.

The most specific value wins: user, then group, then level, then the instance default. `0` means "not set here" and `-1` means unlimited. An on/off switch is off if any of the three turns it off.

The `admin` level is for an admin's own chat account (same email as the admin account). It adds the "Admin mode" link; see `SYNTAXIS_ADMIN_URL`.

## Providers and models

Add providers (Ollama, llama.cpp, LiteLLM, OpenAI-compatible) with an optional API key, stored encrypted. Set **concurrency** to the server's real parallelism. Discovered models start disabled. Set a **context override** for a model when the server cannot report its real context window.

**Helper model** (per provider, optional): a small, fast model for session titles, summaries of long watch events and compaction of long conversations (compaction only if its context window is at least as large as the session model's). It is used only while the server reports it as already loaded; Syntaxis never loads or swaps a model for it. If it is not loaded or a call fails, the session's own model does the job and a health warning is raised.

**Probe** (per model): asks the model to call a trivial tool, and asks for a long reply to check whether it streams or arrives as one block. It can load the model, so it runs only when you press it. The result decides whether agent mode is offered for that model. A buffered stream raises a health warning.

## Web search, pictures and repository content

Set these under **Policies**:

- **Web search server**: the address of a SearXNG instance. Its `json` format must be enabled (`search.formats` in its settings). A server on the LAN also needs `SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE`.
- **Models that accept pictures**: model names, for servers that do not report vision support.

Two settings have no field in the app and are changed through the settings API: `repo_skills` (a repository's skills and slash commands, default on) and `repo_hooks` (a repository's `.syntaxis/hooks.json`, default on).

Reading PDFs and scans needs `poppler-utils` and `tesseract-ocr` in the sandbox image. The default image has them; after an update rebuild it with `docker build -t syntaxis-sandbox:latest images/sandbox`. In a custom image without them the agent cannot read a PDF or picture as text.

## Environments

An environment is a sandbox image you have prepared and approved. The default image has Python, Node, Go and a C toolchain.

**Environments** in the admin app lists the examples below. Nothing is built until you choose it: press **Build**, read the warning (a build takes minutes, uses disk on the Docker host and downloads packages) and confirm. The build runs on the Docker host through the update service, one at a time, and the finished image is approved automatically.

To build on the host instead, run `sh scripts/build-templates.sh rust java` (no names builds all). Images are named `syntaxis-env-<name>:1`.

A built environment unused for 30 days is removed to free disk, unless a session still has a sandbox in it. Change the period on the same page (`0` keeps them). **Build** brings a removed one back.

| Name | Adds | A devcontainer image it can match |
|---|---|---|
| `rust` | Rust (rustup, cargo) | `mcr.microsoft.com/devcontainers/rust` |
| `go` | gopls, delve, staticcheck, golangci-lint, govulncheck, on top of the Go in the default image | `mcr.microsoft.com/devcontainers/go` |
| `java` | OpenJDK 17, Maven | `mcr.microsoft.com/devcontainers/java` |
| `dotnet` | .NET 8 SDK | `mcr.microsoft.com/devcontainers/dotnet` |
| `ruby` | Ruby, Bundler | `mcr.microsoft.com/devcontainers/ruby` |
| `php` | PHP 8, Composer | `mcr.microsoft.com/devcontainers/php` |
| `elixir` | Elixir, Erlang | |
| `cpp` | clang, CMake, Ninja, gdb, valgrind | `mcr.microsoft.com/devcontainers/cpp` |
| `typescript` | tsc, tsx, ts-node, ESLint, Prettier, Vitest, pnpm, Yarn | `mcr.microsoft.com/devcontainers/typescript-node` |
| `kotlin` | Kotlin compiler, OpenJDK 17, Maven | |
| `haskell` | GHC, Cabal, Stack | `mcr.microsoft.com/devcontainers/haskell` |
| `zig` | Zig, verified against the project's published checksum | |
| `swift` | Swift toolchain (about 6 GB) | `swift` |
| `browser` | Headless Chromium with Playwright and its test runner, Puppeteer, axe-core, Jest, Vitest, Mocha, pytest with pytest-playwright (about 3 GB) | |
| `data` | NumPy, pandas, SciPy, scikit-learn, Matplotlib, Jupyter | `mcr.microsoft.com/devcontainers/anaconda` |

Browser image notes: Chromium is wrapped to start with `--no-sandbox` because its own sandbox cannot start inside gVisor. Puppeteer needs `headless: "shell"`. Cypress and Lighthouse are not included because they need a full Chrome or Electron, which does not start under gVisor.

### Making your own

Copy an example Dockerfile from `images/templates/`. Keep `USER 10001:10001` and have tools write their caches under `/tmp`, because the sandbox root filesystem is read-only. Add the image under **Environments**, together with the devcontainer images a repository may name to get it (tags are ignored).

### How sessions use environments

- A user can pick an environment for a session. It runs in that image, under gVisor with the same limits.
- When a session starts from a repository, its `devcontainer.json` (comments allowed) is read through the user's own connection. If `image` matches an approved environment, the session uses it. Anything else is ignored and the session says why. A repository cannot name an image of its own, and building from a Dockerfile is not supported.
- A repository can name an approved environment directly: `"customizations": {"syntaxis": {"template": "rust"}}`.
- After the clone, `containerEnv` and `postCreateCommand` are applied. `containerEnv` takes plain variables only; it cannot change `PATH`, `HOME`, `GIT_*` and similar, and never overrides your secrets. The setup command runs once, unless the session is set to ask before running anything, in which case the command is shown instead.
- Features, forwarded ports, mounts, users and extensions in the file are ignored.

## CLI

```
syntaxis serve | admin create | keygen | doctor | backup <dest> | rotate-key <old> <new> | egress-rules <subnet> … | version
```
