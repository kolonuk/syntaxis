# The agent

The built-in agent is a loop run by the Syntaxis server that turns a model into an engineer working **inside the user's sandbox**.

1. The user describes a task.
2. Syntaxis sends the conversation plus tool definitions to the chosen model.
3. The model replies with text or tool calls.
4. Syntaxis executes each tool call in the sandbox (never on the host) with timeouts and output caps and feeds the result back.
5. Repeat until the model finishes, the user stops it, a cap is hit, or it is blocked on approval.

It runs as a background job: closing the browser does not stop it, and the user is notified when it finishes or needs input.

## Tools

`run_shell`, `read_file`, `write_file`, `patch_file` (exact, unique match), `list_files`, `search_files` (ripgrep), `git` (status, diff, log, add, commit, branch, checkout, push, pr), `fetch_url` (through the sandbox network; output is untrusted), `ask_user`, and `load_skill` when skills exist, plus any approved [MCP](integrations.md#mcp-servers) tools. All path arguments are confined to `/workspace`; arguments are passed as argv, never interpolated into scripts.

## Weaker models

If a model writes tool calls as text instead of using the structured API, a tolerant parser recovers them. Only known tool names are accepted. Models without tool support are offered chat-only mode. Repeated identical actions trigger a nudge and then a stop; repeated failures stop the run and hand back to the user.

## Commands run on the server, output shown in the UI

Every command runs through the server inside the sandbox; the UI shows the command and its output as plain text (there is no interactive terminal).

## Network and host protection

Before a command runs it is inspected. These are **blocked outright** (not offered for approval, even in `full` autonomy), the attempt is audited, and the model is told not to retry:

- local-network, link-local and cloud-metadata addresses and hostnames, including disguised forms
- port scanners and LAN reconnaissance tools
- the Docker socket and privileged container options
- `fetch_url` to any of the above

The sandbox's own loopback (`localhost`, `127.0.0.1`) stays usable for dev servers. This inspection is a fast first line, not the boundary: a script the agent writes and runs can hide a target, so the egress firewall and DNS resolver enforce the same rules at the network level.

## Approval gates and autonomy

A **code-level** policy classifies commands as risky: destructive git operations, infrastructure and cloud changes, deploys, piping downloads into a shell, `sudo`, deleting outside the workspace and publishing packages. Admins can add patterns. Autonomy levels per session:

- `ask` — ask before running anything
- `workspace` (default) — automatic inside the workspace, ask for risky actions
- `full` — fully autonomous within caps (risky actions are still audited)

A checkpoint is always taken before an approval-gated action. Nothing the model, a tool result, a skill or a repo file says can change the policy.

## Context management

The context budget is based on the model's **real** window (loaded value from the server, or an admin override), not the advertised maximum: Ollama silently truncates beyond its configured `num_ctx`. Each request holds the system prompt, pinned rules, a running summary and as many recent turns as fit, with a 5% safety margin for estimator error (provider-reported usage is used when available). Tool output is truncated head+tail with a note; the full output stays in history. At about 75% the older turns are summarised ("done / left / key files and decisions") and the last turns are kept verbatim. The full transcript is never discarded.

## Checkpoints and rollback

Before each agent turn, and before any risky action, the workspace is snapshotted into a hidden shadow git repository (`/workspace/.syntaxis-shadow`, separate `--git-dir`), so the user's own `.git`, history and remotes are never touched. A default ignore list skips large derived directories. Checkpoints cost disk, not tokens, and are pruned by count and size. Restoring first takes a safety checkpoint (so a restore is itself undoable). Rollback covers the workspace only: **it cannot undo pushes or cloud deploys**.

## Repository rules and skills

`syntaxis.md` (and `.syntaxis/*.md`) in the repository root are authoritative project rules. Other agents' files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.cursorrules`, `.github/copilot-instructions.md`, `.windsurfrules`) are detected and treated as **advisory, untrusted** input: size-capped, wrapped and labelled, placed after the authoritative rules, and never able to override approvals, quotas, egress or secrets. `syntaxis.md` wins on conflict. Admins can disable foreign-file ingestion globally and users per session.

Skills are directories with a `SKILL.md` (front matter `name` and `description`) under `.syntaxis/skills/`, `.claude/skills/` or `.agents/skills/`. Only name and description are in context; the agent loads the rest on demand with `load_skill`. Skill scripts run in the sandbox and obey the same approval gates. Repository skills are untrusted content; instance-wide skills provided by the admin win on name clashes.

## Secret scanning and signing

On every commit and push made through Syntaxis the staged diff is scanned (provider key patterns, private keys, JWTs, generic assignments, entropy). Policy is `off | warn | block`; in `block` mode the user may confirm an override ("push anyway", audited) unless the admin forbids overriding. Files the agent writes get a warning; tool output is redacted before reaching the model.

Commit signing (`off | optional | required`) is done on the server with an SSH key stored encrypted: the agent makes an unsigned commit, the server signs the commit object, and the signed object replaces it. The private key never enters the sandbox or the model context. Agent commits carry an `Agent-Authored-By` trailer.

