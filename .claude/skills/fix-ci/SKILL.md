---
name: fix-ci
description: Use after pushing, or when asked why CI is red. How to find the failing step quickly and the failures this repo has had before.
---

# Fixing CI

```sh
gh run list --limit 5
gh run view <id> --json jobs -q '.jobs[] | select(.conclusion!="success") | .name'
gh run view <id> --log-failed | grep -E "FAIL|error|Error"
```

Both Go test jobs (`go`, with -race, and the real-service job) write results to a file, so each has a "Failed tests" step that prints them; read that step. A data race only shows under `go test -race` (the `go` job): run it locally with `DOCKER_HOST=tcp://127.0.0.1:1 go test -race ./...` to see what CI sees. The "What was not tested" summary lists everything that skipped and why.

Wait for a run with a loop that polls (`until [ "$(gh run view <id> --json status -q .status)" = completed ]; do sleep 30; done`), not a long single sleep.

## Failures seen before

- **Hygiene test**: a placeholder email or host in code. Use `example.com`.
- **gVisor job**: a test that assumed the host firewall or a service that is not on the runner. Make it skip with `NOT TESTED` when it cannot decide, never loosen the assertion.
- **Link check**: a moved documentation URL. Fix the link.
- **Images job**: example environment images build on the sandbox image, which must be loaded into the local Docker (`load: true`).
- **hadolint**: unpinned distribution packages are accepted on purpose (`DL3008`, `DL3013`).
- **GitHub API limits**: unauthenticated checks share a runner address; a rate limit is a skip, not a pass.
- **Data race in a test** (shows only in the `go` job, which uses `-race`): a stand-in run writes a slice the test reads. Use the guarded `msgLog` helper in `internal/server/server_test.go`.
