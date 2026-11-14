# Contributing

Thanks for helping. By contributing you agree your work is licensed under the project licence (GPL-2.0-or-later).

## Ground rules

- Single branch: `main`. Small, conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `chore:`). No feature-branch workflow is required for maintainers; external contributors may use pull requests.
- **No environment-specific values** anywhere: no real hostnames, IPs, emails, model names or hardware assumptions in code, docs or defaults. Use `example.com`, `llm.internal`, and label illustrative numbers as illustrative.
- **Never commit secrets.** `.env` is git-ignored; use `.env.example`.
- Every behaviour change comes with tests. Security-relevant behaviour (isolation, auth, egress, redaction, approvals) must have a test that fails if the protection is removed.

## Development

```sh
# backend (Go 1.26+)
go test -race ./...
go vet ./...
gofmt -l .
golangci-lint run ./...

# frontend (Node 18+; CI uses 22)
cd web && npm ci && npm test && npm run lint && npm run typecheck && npm run build
```

`npm run build` writes the UI into `internal/webui/dist`, which `go build` embeds. A clean checkout builds without it (the UI is then empty).

Integration tests that need a real gVisor host skip themselves when `runsc` is absent. Playwright flows (`npm run e2e`) need a running instance; see `web/playwright.config.ts`.

## Adding a provider adapter / agent adapter

Implement `provider.Provider` (read-only toward the server) or `agent.AgentAdapter`, add fake-server tests, and document trade-offs in `docs/`.

## Releases and versions

Versions follow semver and are tags of the form `vMAJOR.MINOR.PATCH` on `main`. Pushing a tag runs the release workflow, which publishes the images, builds the binaries and creates the GitHub release with generated notes. The version, commit and build date are compiled in (`internal/buildinfo`) and shown by `syntaxis version`, `/api/version` and the apps themselves. Add user-visible changes to the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md) as you make them; move them under the new version when tagging.
