# Configuration

Everything environment-specific is configuration. Defaults are safe-by-default. Settings marked *admin UI* can also be changed at runtime in the admin app (stored in the database; they override these first-boot values).

| Variable | Default | Meaning |
| --- | --- | --- |
| `SYNTAXIS_LISTEN` | `127.0.0.1:8080` | Chat app listen address |
| `SYNTAXIS_ADMIN_LISTEN` | `127.0.0.1:8081` | Admin app listen address. A separate port so it can stay on loopback/LAN/VPN. Binding it publicly is flagged as an error by the health check |
| `SYNTAXIS_BASE_URL` | `http://localhost:8080` | Public URL for email links; `https://` turns on Secure cookies and HSTS |
| `SYNTAXIS_DATA_DIR` | `./data` | SQLite database and local state |
| `SYNTAXIS_MASTER_KEY` / `_FILE` | – | 32-byte base64 key (`syntaxis keygen`). Required for `serve`. Keep outside the data dir |
| `SYNTAXIS_LOG_LEVEL` / `_FORMAT` | `info` / `json` | `text` for pretty dev output |
| `SYNTAXIS_SMTP_HOST` `_PORT` `_USER` `_PASSWORD` `_FROM` | empty / 587 | Empty host = dev mode: emails are logged, not sent |
| `SYNTAXIS_DOCKER_HOST` | environment | Use the socket proxy: `tcp://docker-socket-proxy:2375` |
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
| `SYNTAXIS_WORKSPACE_RETENTION_DAYS` | `30` | Delete a user's workspace after this many days unused (`0` keeps) |
| `SYNTAXIS_PROXY_AUTH_ENABLED` | `false` | Trusted reverse-proxy header authentication (off by default) |
| `SYNTAXIS_PROXY_AUTH_HEADER` / `_TRUSTED` | `X-Forwarded-User` / – | Header name and the **only** proxy addresses/CIDRs whose headers are trusted |
| `SYNTAXIS_VAPID_PUBLIC` / `_PRIVATE` | – | Web Push keys (`syntaxis keygen --vapid`) |
| `SYNTAXIS_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` | – | Create the first admin on first boot. Prefer `syntaxis admin create`; remove after use |

## Per-user policy (admin UI)

Daily and monthly token quotas (`0` = default, `-1` = unlimited), maximum concurrent sessions, egress mode, allowed models (empty = all enabled models), cloud tooling on/off.

## Providers and models

Add one or more providers (Ollama, llama.cpp, LiteLLM, OpenAI-compatible) with an optional API key (encrypted at rest). Set **concurrency** to your server's real parallelism. Discovered models start **disabled**. Set a **context override** for a model when the server cannot report its real window.

## CLI

`syntaxis serve | admin create | keygen [--vapid] | doctor | backup <dest> | rotate-key <old> <new> | egress-rules <subnet> … | version`
