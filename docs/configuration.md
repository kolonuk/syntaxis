# Configuration

Everything environment-specific is configuration. Defaults are safe-by-default. Settings marked *admin UI* can also be changed at runtime in the admin app (stored in the database; they override these first-boot values).

| Variable | Default | Meaning |
| --- | --- | --- |
| `SYNTAXIS_LISTEN` | `127.0.0.1:8080` | Chat app listen address |
| `SYNTAXIS_ADMIN_LISTEN` | `127.0.0.1:8081` | Admin app listen address. A separate port so it can stay on loopback/LAN/VPN. Binding it publicly is flagged as an error by the health check |
| `SYNTAXIS_ADMIN_URL` | – | Where admins open the admin app. Adds an "Admin mode" link in the chat app for users with the admin level. The admin app has its own sign-in and two-factor step; the link does not skip it |
| `SYNTAXIS_BASE_URL` | `http://localhost:8080` | Public URL for email links; `https://` turns on Secure cookies and HSTS |
| `SYNTAXIS_DATA_DIR` | `./data` | SQLite database and local state |
| `SYNTAXIS_MASTER_KEY` / `_FILE` | – | 32-byte base64 key (`syntaxis keygen`). Required for `serve`. Keep outside the data dir |
| `SYNTAXIS_LOG_LEVEL` / `_FORMAT` | `info` / `json` | `text` for pretty dev output |
| `SYNTAXIS_SMTP_HOST` `_PORT` `_USER` `_PASSWORD` `_FROM` | empty / 587 | Empty host = dev mode: emails are logged, not sent |
| `SYNTAXIS_DOCKER_HOST` | environment | The validating Docker proxy: `tcp://docker-socket-proxy:2375` (Compose sets it) |
| `SYNTAXIS_SANDBOX_RUNTIME` | `runsc` | OCI runtime for sandboxes. Only gVisor runtimes are accepted by the spec validator |
| `SYNTAXIS_SANDBOX_IMAGE` | `syntaxis-sandbox:latest` | Sandbox image |
| `SYNTAXIS_SANDBOX_NETWORK` | `syntaxis-sandbox` | Dedicated Docker network |
| `SYNTAXIS_EGRESS_MODE` | `open` | `open` / `allowlist` / `none`; per-user override in the admin UI |
| `SYNTAXIS_EGRESS_ALLOWLIST` | – | Comma-separated hostnames or CIDRs (allowlist mode) |
| `SYNTAXIS_ALLOW_ADMIN_READ_CONTENT` | `false` | Admins can read message content only when true; every read is audited |
| `SYNTAXIS_PASSWORD_ROTATION_DAYS` | `30` | Users **without 2FA** must change their password this often; `0` disables. *admin UI* |
| `SYNTAXIS_SECRET_SCAN` | `warn` | `off` / `warn` / `block`. *admin UI* |
| `SYNTAXIS_ALLOW_USER_OVERRIDE` | `true` | May a user confirm a push despite scanner findings (audited). *admin UI* |
| `SYNTAXIS_SIGNING_MODE` | `off` | `off` / `optional` / `required` SSH commit signing |
| `SYNTAXIS_IDLE_MINUTES` | `30` | Stop sandboxes idle this long |
| `SYNTAXIS_SANDBOX_MAX_AGE_DAYS` | `7` | Destroy unused sandbox containers after this many days |
| `SYNTAXIS_CPU_MINUTES_DAILY` | `240` | CPU time (core-minutes) a user's sandboxes may use per day; `0` = unlimited. See [troubleshooting](pitfalls.md#resource-limits) |
| `SYNTAXIS_WORKSPACE_RETENTION_DAYS` | `30` | Delete a user's workspace after this many days unused (`0` keeps) |
| `SYNTAXIS_PROXY_AUTH_ENABLED` | `false` | Trusted reverse-proxy header authentication (off by default) |
| `SYNTAXIS_PROXY_AUTH_HEADER` / `_TRUSTED` | `X-Forwarded-User` / – | Header name and the **only** proxy addresses/CIDRs whose headers are trusted |
| `SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` | – | Create the first admin on first boot. Prefer `syntaxis admin create`; remove after use |

## Levels, groups and per-user settings

Every chat user has a **level** (`user`, `poweruser`, `admin`) and may be in a **group**. Levels, groups and users all carry the same settings: token quotas, concurrent sessions, daily compute minutes, egress mode, allowed models, and switches for the terminal and cloud tooling. Set levels and groups under **Levels & groups** in the admin app, and a user's level and group under **Users → Edit**.

The most specific setting wins: the user's own, then their group's, then their level's, then the instance default (`0` means "not set here", `-1` unlimited). An on/off switch stays off if any of the three turns it off. The `admin` level is for an admin's own chat account (same email as their admin account) and adds an "Admin mode" link; see `SYNTAXIS_ADMIN_URL`.

## Providers and models

Add one or more providers (Ollama, llama.cpp, LiteLLM, OpenAI-compatible) with an optional API key (encrypted at rest). Set **concurrency** to your server's real parallelism. Discovered models start **disabled**. Set a **context override** for a model when the server cannot report its real window.

**Probe** (per model) checks for real instead of trusting what the server claims: it asks the model to call a trivial tool, and asks for a long reply to see whether it streams or arrives in one block (a proxy or compression in front of the server holding it back). It may load the model, so it only runs when you press it. The result decides whether agent mode is offered for that model, and a buffered stream shows up as a health warning.

## CLI

`syntaxis serve | admin create | keygen | doctor | backup <dest> | rotate-key <old> <new> | egress-rules <subnet> … | version`
