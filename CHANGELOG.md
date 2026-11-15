# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/); versions follow semver once 1.0 is tagged. The running version is shown in the apps (Settings, admin header), by `syntaxis version` and at `/api/version`.

## [Unreleased]

### Added
- **Security:** a validating Docker API proxy (`syntaxis docker-proxy`) replaces the generic socket proxy in Compose. Only hardened gVisor sandboxes can be created or started through it; privileged containers, host mounts, host namespaces, devices, published ports and unused endpoints are refused. The admin Health page warns when a different proxy is in front of Docker.
- Interactive terminal tab in the session view, running in the session's own sandbox, with an admin switch per user.
- Live progress line while a run is working: waiting, connecting, loading the model, reading, thinking, replying, running a command.
- CPU metering with a daily compute allowance, a Resources tab (typical and busiest-10% CPU, memory), and throttling, pausing and stopping of sustained load.
- Copy buttons on code blocks and command output.
- Build information (version, commit, date) in the binary, the apps, `/healthz` and `/api/version`.
- MCP servers, each in its own gVisor sandbox, with registry browsing and admin approval.

### Changed
- The health check verifies the egress firewall by behaviour (a short-lived sandbox tries to reach the Docker host) instead of trusting `SYNTAXIS_NETWORK_RULES_APPLIED`, so a missing or outdated ruleset is reported.
- Secrets are read fresh for every command, so adding, changing or removing one applies immediately instead of needing a new session.
- Simpler getting started guide and README.

### Fixed
- **Security:** sandboxes could open connections to services listening on the Docker host itself (via the bridge gateway or the host's LAN address), because that traffic takes the input path, which the egress rules did not filter. The ruleset now has an input chain; re-apply it with `scripts/apply-egress.sh`.
- Sandboxes on the same network could reach each other on hosts without `br_netfilter` loaded; the egress rules now also filter at the bridge.
- Chat jitter while a reply streamed (only the chat log scrolls now).
- Admin Benchmarks and MCP tabs were not reachable from the navigation.
- Sandbox DNS on Docker releases that cannot mount a single file from a volume subpath.
- Several accessibility issues: badge contrast, heading order, keyboard-focusable scroll areas, icons that failed to render.

## Initial development

Authentication with separate admin and chat accounts, provider adapters, health checks, fair LLM queue, quotas, gVisor sandbox manager, egress policy and DNS resolver, built-in agent with approval gates and context management, checkpoints, rules and skills, secrets vault, secret scanner, SSH commit signing, admin and chat web apps (PWA), CLI, CI and release workflows.
