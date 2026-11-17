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

# frontend (Node 22+)
cd web && npm ci && npm test && npm run lint && npm run typecheck && npm run build
```

`npm run build` writes the UI into `internal/webui/dist`, which `go build` embeds. A clean checkout builds without it (the UI is then empty).

## Tests use the real thing

Nothing is faked: no pretend GitHub, model server, mail server or container engine. Tests run against real Docker and gVisor, a real Gitea and SMTP server in containers, the public internet (GitHub, PyPI, Docker Hub and so on) and, for the agent itself, a real local model.

When something real is not available, the test **skips with `NOT TESTED: <reason>`** instead of passing on a stand-in. `scripts/not-tested.sh results.json` lists those from a `go test -json` run, and CI prints the list on each run.

- Needs Docker and gVisor (`runsc`) and the sandbox image (`docker build -t syntaxis-sandbox:latest images/sandbox`): skips without them. The OCR tests also need an image that has poppler and Tesseract (the current Dockerfile does); to test a freshly built image without replacing the one a running instance uses, build it under another tag and set `SYNTAXIS_TEST_SANDBOX_IMAGE`.
- Web search: set `SYNTAXIS_TEST_SEARXNG_URL` to a SearXNG server with the json format enabled, or the live test skips.
- Needs an account: set `SYNTAXIS_TEST_GITHUB_TOKEN`, `SYNTAXIS_TEST_GITLAB_TOKEN`, `SYNTAXIS_TEST_CLOUDFLARE_TOKEN`, `SYNTAXIS_TEST_IFTTT_KEY`, `SYNTAXIS_TEST_AWS_ACCESS_KEY_ID` with `..._SECRET_ACCESS_KEY`, the `SYNTAXIS_TEST_AZURE_*` set, or the `SYNTAXIS_TEST_GCP_SERVICE_ACCOUNT_JSON` with `SYNTAXIS_TEST_GOOGLE_CLOUD_PROJECT`.
- Needs a model: set `SYNTAXIS_LIVE=1` and point `SYNTAXIS_TEST_ENV` at a file with `OLLAMA_URL`, `OLLAMA_KIND` and `MODEL_TINY`, `MODEL_SMALL`, `MODEL_MAIN` (default `~/.config/syntaxis-test/env`, outside the repository). A real model decides for itself, so those tests ask for simple checkable results and assert on what really happened in the sandbox.

Tests that start sandboxes use user ids far above any real user's and clean up after themselves, and whole-host operations (the reaper, the kill switch) are scoped to the test's own containers, so a Syntaxis already running on the same Docker host is not touched.

The web checks: `npm run e2e` (Playwright flows and axe accessibility) and `npm run lighthouse` need a running instance (CI runs them against the one the installer creates; see `web/playwright.config.ts`); `npm run budget` checks the built bundle sizes.

## Skills for contributors

`.claude/skills/` holds short how-to guides for the recurring jobs: writing real-service tests, adding a connection, changing the database, shipping a feature, checking the web app and fixing CI. They are plain Markdown and worth reading whatever tool you use.

## Adding a provider adapter / agent adapter

Implement `provider.Provider` (read-only toward the server) or `agent.AgentAdapter`, test it against a real server (skipping with `NOT TESTED` where none is available), and document trade-offs in `docs/`.

## Releases and versions

Versions follow semver and are tags of the form `vMAJOR.MINOR.PATCH` on `main`. Pushing a tag runs the release workflow, which publishes the images, builds the binaries and creates the GitHub release with generated notes. The version, commit and build date are compiled in (`internal/buildinfo`) and shown by `syntaxis version`, `/api/version` and the apps themselves. Add user-visible changes to the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md) as you make them; move them under the new version when tagging.
