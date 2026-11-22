# Troubleshooting

The admin **Health** page (and `docker compose exec syntaxis syntaxis doctor`) checks most of this and says how to fix what it finds. This page covers the things worth knowing in advance.

## Context windows

A model has an advertised maximum context, a configured one and the one the server actually loaded, and servers often run with far less than advertised. Ollama, for one, silently drops the oldest text beyond its limit, and the agent then "forgets" the task and behaves oddly. Agent work wants roughly 16k tokens or more.

Set the window on the server and check it: for Ollama see its [FAQ on context length](https://docs.ollama.com/faq); for llama.cpp use `--ctx-size` (shared across `--parallel` slots). If the server cannot report its real window, set a **context override** on the model in the admin app.

## Concurrency

A GPU serves only so many requests at once (Ollama's `OLLAMA_NUM_PARALLEL`, llama.cpp's `--parallel`). Set each provider's **concurrency** in Syntaxis to that real number: it queues the rest fairly. Set too high, requests wait invisibly inside the server and time out.

## Weak tool calling

Some local models call tools badly. Syntaxis recovers what it can and stops loops, but expect to supervise a new model: use the `ask` autonomy level first, and try the admin app's benchmark (Providers & models) to compare models. Syntaxis never loads or swaps a model by itself, so avoid configuring your LLM server to unload models between requests. The admin app's **Probe** button (Providers & models) tests a model for real: it asks for a tool call and checks the reply streams, and the result decides whether agent mode is offered.

## Reverse proxy and HTTPS

The installer's `--https <domain>` option sets up [Caddy](https://caddyserver.com/) with automatic certificates. If you use your own proxy: turn off response buffering and compression for the LLM route (replies would arrive in one lump; **Probe** in the admin app shows when that happens), allow long timeouts (agent runs are long), pass the real client address only from your proxy (`SYNTAXIS_PROXY_TRUSTED`), and do not loosen the Content-Security-Policy. Installing the app on a phone and offline support need HTTPS.

## Email

Password resets need working SMTP (invites do not: the admin app shows the invite link to copy; without SMTP the links also appear in `docker compose logs syntaxis`). Set up SPF, DKIM and DMARC for the sender domain or they land in spam. Two-factor codes need an accurate clock, so keep NTP running.

## gVisor

[gVisor](https://gvisor.dev/docs/user_guide/compatibility/) does not support every Linux feature, and file and network I/O is slower than a normal container, so big builds take longer. There is no GPU access and no nested virtualisation. Docker's built-in DNS does not work under gVisor, so Syntaxis runs its own resolver for sandboxes; if lookups fail, check the Health page.

## Resource limits

Sandboxes have fixed CPU, memory and process limits. On top of that Syntaxis watches each one: a sandbox that stays near its CPU limit for about two minutes is slowed down, after five paused, and a second overload stops it, while short bursts such as a build are fine. CPU time actually used is counted per user against a daily allowance (`SYNTAXIS_CPU_MINUTES_DAILY`). The **Resources** tab shows the numbers. Each workspace has a size limit (5 GiB); over it, new messages are refused until the user frees space in the terminal tab.

## Linux distributions

- **AlmaLinux, Rocky, RHEL, Fedora:** SELinux is enforcing by default; the compose file already lets the Docker proxy reach the socket. Look at `ausearch -m avc -ts recent` rather than turning SELinux off. firewalld may need the chat port opened for other machines.
- **Raspberry Pi OS:** use the 64-bit release. If memory limits do not apply, add `cgroup_enable=memory cgroup_memory=1` to `/boot/firmware/cmdline.txt` and reboot. Expect slow builds and room for only a couple of sessions.
- **macOS and Windows:** not supported; gVisor needs Linux.

## Backups and upgrades

Back up the database (`syntaxis backup <file>`), the workspace volumes, and the **master key, separately**; without the key stored secrets and 2FA cannot be recovered, and a wrong key is not noticed until a secret fails to decrypt.

`syntaxis backup` writes a consistent copy and then opens it on its own: SQLite must find it intact and it must have every table, or the file is removed and the command fails. Everything Syntaxis keeps (users, sessions, notes, hooks, commands, shares, automations, tokens, settings) is in that one file. A restore is: stop Syntaxis, put the file where the old one was (`<data dir>/syntaxis.db`, with the old master key in place), and start it again. This was tried on a real host with the real binary and with a database holding data in every table, and the restored copy matched table by table.

A workspace volume is a Docker volume named `syntaxis-ws-u<user id>`. To copy one, run `tar` as root in a throwaway container, one end into the other (replace the names and the image with one you have):

```sh
docker run --rm --user 0 -v OLD-VOLUME:/w:ro IMAGE tar -C /w -cpf - . | docker run --rm -i --user 0 -v NEW-VOLUME:/w IMAGE tar -C /w -xpf -
```

Without `--user 0` the sandbox image runs as a normal user and cannot write the volume. This keeps content, permissions, owners and links. `scripts/update.sh` takes a backup, upgrades and rolls back if the new version is unhealthy. To check which version is running, see Settings → Profile, `syntaxis version` or `/api/version`, and compare with the [changelog](../CHANGELOG.md).

## Known limits

Linux hosts only; one instance (SQLite has one writer); no IPv6 inside sandboxes.
