---
name: write-real-service-tests
description: Use when writing or running Go tests in this repo. Tests use real services only (Docker, gVisor, containers, the internet, a real model) and skip with "NOT TESTED" when something is missing. Covers the helpers, running them safely on a host that also runs Syntaxis, and what never to do.
---

# Real-service tests

Nothing is faked: no pretend GitHub, model server, mail server or container engine, and no scripted model. If a real service is not available, the test **skips with `NOT TESTED: <reason>`** (`testenv.NotTested`). `scripts/not-tested.sh results.json` lists those from `go test -json`.

## Helpers (`internal/testenv`)

- `NotTested(t, reason)`, `Network(t, "host:443")`, `Env(t, "SYNTAXIS_TEST_X", "what needs it")`, `Docker(t)`, `GVisor(t)`.
- `Load(t)`: the private model environment (opt-in with `SYNTAXIS_LIVE=1`, file outside the repo, see CONTRIBUTING.md). Models on that server change, so check a model exists before relying on it.
- `Gitea(t)`: a real Gitea in a container with a user and token; `.Do` calls its API.
- `KeepDNSVolume(t, image)`: wrap any test that gives a sandbox a resolver; it restores the shared resolver file.
- In `internal/server` tests: `realSandbox`, `liveEnv` (real model and sandbox), `cleanupSandboxes`, `execSpec`.

## Rules

- Do not write a test that needs someone's account, token or key: it cannot run for anyone else. Test wrong credentials against the real service (no account needed), a local service in a container, or random users made for the test.
- A change that cannot be made to happen for real (a new upstream release) is simulated by setting the stored state back, never by a fake source.
- A real model decides for itself: ask for simple checkable results and assert on what happened in the sandbox, not on wording.
- Say plainly in the test, with a `NOT TESTED` log or skip, what the test does not cover.

## Safety on a host that also runs Syntaxis

- Use user ids above 900000 so workspace volumes never collide. Clean up containers, volumes and networks you make.
- Whole-host operations (reaper, kill switch) must be scoped to the test's containers (see `scoped` in `internal/sandbox/real_test.go`).
- Never start `syntaxis serve` against the host's Docker: its reaper would remove the other instance's sandboxes. If a server must run, set `DOCKER_HOST` to an unreachable address and use other ports.
- Never `pkill -f <pattern>` from a shell run by an agent: it matches its own command line. Use `kill $(pgrep -x name)`.
