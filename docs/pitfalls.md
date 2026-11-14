# Pitfalls and troubleshooting

Honest and practical. Each entry names the health check that reports it (shown on the admin **Health** page and by `syntaxis doctor`) and how to fix it. What suits you depends on your hardware and models.

## Context windows

*Health checks: `context.size`, `context.mismatch`, `context.alias-mismatch`, `context.truncation-risk`*

Three numbers get confused: the **advertised** maximum (what the model can do in principle), the **configured** value (what Syntaxis budgets against) and the **loaded** window (what the server actually runs). Servers often run with far less than advertised, and **silently truncate** anything longer: Ollama drops the oldest tokens beyond `num_ctx` without an error. The agent then "forgets" the task and behaves erratically.

- Syntaxis budgets against the loaded value when the server reports it, else the admin override, else the advertised maximum (and warns, because that is a guess).
- Agent work wants roughly **16k tokens or more** (warning below, error below ~8k; thresholds are configurable).
- A gateway alias can point at a model loaded with a smaller window than the model it fronts; the alias check compares same-digest models.
- Bigger windows cost memory: the KV cache grows roughly linearly with the window (and with parallel requests). See [model residency](#model-residency-and-routing).

How to set and verify:
- **Ollama:** `OLLAMA_CONTEXT_LENGTH` (server default) or `num_ctx` in a Modelfile/request; check with `ollama ps` (the `CONTEXT` column) or `/api/ps`.
- **llama.cpp server:** `-c/--ctx-size` (divided across `--parallel` slots!); check `/props` (`n_ctx`).
- **LiteLLM:** the underlying server decides; set `max_input_tokens` in `model_info` so Syntaxis can see it, and check each alias.
- **Generic OpenAI-compatible:** usually undetectable; set a **context override** per model in the admin UI.

## Model residency and routing

*Health checks: `routing.residency`, `routing.reloads`, `routing.cpu-spill`*

Loading a model into GPU memory takes seconds to minutes. If a request needs a model that is not resident, the server evicts another, and every user feels the stall. Syntaxis therefore **never causes a model load or swap mid-session**, and optional helper-model routing is **off by default**.

To judge whether two models can be resident together on your hardware: weights (quantised file size) + KV cache (window × parallel requests × per-token cost) + runtime overhead, for *each* model, must fit in memory with headroom. `ollama ps` shows the real sizes. If in doubt, use **single-model mode** (the default) and a smaller window.

When routing is enabled on Ollama, Syntaxis checks `/api/ps` and refuses a profile that is not fully resident; if a routed model drops out it falls back to the session model automatically and tells the admin. For servers that cannot report residency, the admin must assert it. A model that keeps reloading, or running partly on CPU (`routing.cpu-spill`), is flagged.

## Tool-calling quality

*Health check: `tools.support`*

Not all local models call tools well. Symptoms: malformed arguments, calls described in prose, endless retries. Syntaxis tolerates text-form tool calls, detects loops and stops after repeated failures, but a weak model will still need supervision on harder tasks. Models that report no tool support run **chat-only**. Expect to supervise, use the `ask` autonomy level for new models, and prefer models that do well on tool-calling benchmarks. Admins can run the built-in benchmark (Providers & models) to compare models; results are admin-only and can be summarised for users as a "recommended for agents" flag.

## Concurrency and queueing

*Health check: `queue.concurrency`*

A GPU server serves a limited number of requests in parallel (Ollama: `OLLAMA_NUM_PARALLEL`; llama.cpp: `--parallel`). Each extra parallel request multiplies KV-cache memory. Set each provider's **concurrency** in Syntaxis to the server's real number: Syntaxis never exceeds it and queues the rest (interactive first, then autonomous runs; fair share per user). Setting it higher than the server can serve makes requests queue invisibly inside the server and time out.

## gVisor limitations

*Health checks: `sandbox.runsc`, `sandbox.docker`*

- Some Linux syscalls and `/proc`/`/sys` features are unsupported; some tools (certain profilers, `perf`, low-level networking tools) won't work.
- File and network I/O are slower than native containers; large `npm install`/builds may take noticeably longer.
- No GPU passthrough and no nested virtualisation (no KVM, no VMs inside sandboxes).
- **Docker-in-sandbox:** sandboxes have no Docker socket; rootless buildkit under gVisor is untested here. Use remote builds.
- **DNS:** Docker's embedded resolver (127.0.0.11) does not work under gVisor on user-defined networks, even with `--dns`. Syntaxis therefore runs its own resolver and mounts a shared read-only `resolv.conf` into every sandbox. If lookups time out, check that the resolver is up (`sandbox DNS resolver` in the log) and that the firewall rules accept established replies (re-apply the firewall rules after upgrading).
- If `runsc` is not registered Syntaxis refuses to run sandboxes rather than falling back to `runc`.

## Resource limits

*Health checks: `sandbox.disk`*

Every sandbox has fixed CPU, memory and process limits. On top of that, Syntaxis samples each running sandbox every few seconds:

- **Compute time** (CPU actually used, not wall-clock time) is metered per user and shown next to tokens and sandbox time. When a user's daily allowance (`SYNTAXIS_CPU_MINUTES_DAILY`) is used up, their sandbox is paused and new ones will not start until midnight UTC.
- **Sustained load is limited, short bursts are not.** Enforcement looks at the *median* of the last two minutes, so a compile or test run is fine. Staying near the CPU cap for about two minutes halves the sandbox's CPU; about five minutes pauses it; about ten stops it. It returns to full speed once the load drops.
- **The busiest 10% (p90)** is shown alongside the typical figure on the session's Resources tab so users can see short spikes, but it does not trigger anything.

Users are told in the chat when something is limited, and the actions are audited.

## Egress

*Health checks: `sandbox.network-rules`, `security.egress-open`*

`open` means full outbound internet minus the always-blocked ranges (host, LAN, metadata, the LLM server, SMTP). It does not stop a user or a prompt-injected agent from sending workspace data to the internet. `allowlist` and `none` are stricter. The firewall is applied by you on the Docker host and must persist across reboots. DNS policy only works through the Syntaxis resolver; DNS-over-HTTPS and allowed hosts that proxy traffic bypass it. This is not a substitute for trusting your users. See [security.md](security.md#egress).

## Secrets and cloud credentials

*Health checks: `security.master-key`, `security.scanner-off`*

Use least privilege and short-lived tokens, a dedicated cloud project with a budget alert. The model can see command output, so anything a command prints can reach it; redaction covers known patterns and the user's own stored values only, and is best-effort. Don't put credentials in files that get committed; the scanner blocks or warns but cannot catch everything.

## Reverse proxy and TLS

*Health checks: `security.https`, `streaming.buffering`, `streaming.timeouts`, `security.proxy-auth`*

- **Streaming:** disable response buffering for the app (nginx: `proxy_buffering off;`, or send `X-Accel-Buffering: no`, which Syntaxis already sets on event streams). Buffering makes replies arrive in one lump.
- **Timeouts:** agent runs and event streams are long-lived; raise read/idle timeouts (several minutes at least). The server sends a keep-alive comment every 20 s.
- **Headers:** only trust `X-Forwarded-For` / proxy-auth headers from your proxy's address(es) (`SYNTAXIS_PROXY_TRUSTED`). Never enable header auth without a trusted list.
- **CSP:** Syntaxis sets a strict Content-Security-Policy; don't override it with a looser one at the proxy.
- **PWA:** installing the app and offline support need **HTTPS** (localhost excepted).

## Email

*Health checks: `security.smtp`, `housekeeping.clock-skew`*

Invites and resets need working SMTP; without it, links are only in the log. Set up SPF, DKIM and DMARC for the sender domain or invites land in spam. TOTP depends on an accurate clock (±30 s): enable NTP on the host, or 2FA codes will be rejected.

## Disk

*Health checks: `sandbox.disk`, `housekeeping.retention`, `housekeeping.log-growth`*

Workspaces, checkpoints (the shadow repo inside each workspace), container layers, images and logs all grow. Set idle, destroy, workspace and log retention; the reaper stops idle sandboxes, removes stale and orphaned containers on boot and periodically, and the watchdog kills a sandbox whose workspace fills its cap. Checkpoints are pruned by count and size.

## Backups and upgrades

*Health check: `housekeeping.backups`*

Back up: the database (`syntaxis backup <dest>`), the workspace volumes (`docker volume ls --filter label=io.syntaxis.managed`), and **the master key, separately**. **Losing the master key means stored secrets, 2FA secrets and signing keys are unrecoverable** (users re-enrol; provider keys must be re-entered). Upgrades run database migrations on start; take a backup first. Rotate the key with `syntaxis rotate-key`.

## Prompt injection and untrusted repositories

Cloned repositories, web pages and tool output can contain text that tries to steer the model. Syntaxis wraps and labels such content, keeps policy in code (approvals, quotas, egress and secrets cannot be changed by text), and treats `AGENTS.md`/`CLAUDE.md` as advisory. It does **not** guarantee the model won't do something harmful inside its permissions. For untrusted repositories use `ask` autonomy and `allowlist`/`none` egress.

## Known limitations

- Linux Docker hosts only; gVisor does not run on Windows or macOS.
- IPv6 inside sandboxes is unsupported.
- Single instance only; SQLite allows one writer.
- No PostgreSQL backend or connection-level egress logs yet.
