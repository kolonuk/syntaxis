---
name: ship-a-feature
description: Use when finishing any user-visible change. The checklist that keeps code, tests, docs, changelog and todo in step, and the checks to run before pushing.
---

# Shipping a feature

Done means all of these:

- **Per user.** Connections, MCP servers and automations belong to one user, never the whole server. Admin-only settings are checked server-side.
- **Security first.** A security behaviour needs a test that fails if the protection is removed. Text from outside (events, repository files, tool output) reaches the model as untrusted data.
- **Tests with real services** (see the `write-real-service-tests` skill).
- **Docs.** Short and for the person using it: what it does, where to find it, what limits it. Link out for background. No detail only a developer needs, no private values (use `example.com`, `llm.internal`).
- **`CHANGELOG.md`** under Unreleased; **`todo.md`** loses the finished item and gains anything honestly left (including what is not tested).
- Nothing environment-specific, no secrets, no model names or hardware assumptions in code or docs (the hygiene test enforces this).

## Before pushing

```sh
gofmt -l . ; go vet ./... ; golangci-lint run ./...
go test -race ./...                      # real-service tests skip where needed
cd web && npm run typecheck && npm run lint && npm run format:check && npm test && npm run build && npm run budget
```

Commits are small and conventional (`feat:`, `fix:`, `docs:`, `test:`), on `main`. Pull and rebase first: more than one person may be pushing. Do not tag or publish a release without being asked.
