# The agent

The agent is a loop the Syntaxis server runs to turn a model into an engineer working **inside the user's sandbox**: the model is sent the conversation and a set of tools, asks to use them, the server runs each one in the sandbox and returns the result, and this repeats until the model is done, the user stops it, or a limit is hit. It runs in the background and notifies the user when it finishes or needs them.

## Tools

Run a shell command, read, write and patch files, list and search files, git (including opening a pull request), fetch a URL, ask the user a question, and load a skill. Tools from approved [MCP servers](integrations.md#mcp-servers) are added. File paths are confined to the workspace.

Local models are not always good at calling tools. Syntaxis recovers calls written as plain text, retries malformed ones, stops loops and repeated failures, and offers chat-only mode for models without tool support.

## Autonomy and approvals

Each session has an autonomy level: **ask** (every action waits for you), **workspace** (the default: routine work runs, risky actions wait) or **full** (everything runs within limits, risky actions are still logged). Risky means things like force pushes, deploys, cloud changes, `sudo`, deleting outside the workspace and opening pull requests. The approval request says which of the user's connections the command would use. Commands that target the local network are blocked outright at every level.

## Plan first and reviewing changes

With **Plan first** the agent can only read until you approve its plan. It explores, proposes a numbered plan, and stops; you edit and approve it, or ask for changes. The server enforces this: the planning step has no way to change anything. The **Changes** tab shows what the agent changed, file by file, and a comment on a file can be sent back to the agent.

## Context and checkpoints

The agent budgets against the model's **real** context window rather than the advertised one (many servers silently run with less; see [troubleshooting](pitfalls.md#context-windows)). Long conversations are summarised automatically and the full history is kept. Before each turn and before any risky action the workspace is snapshotted into a hidden git repository, separate from the user's own `.git`, so work can be rolled back. The **Checkpoints** tab lists them with what changed since each one, and restores the files, or the files and the conversation (later messages are set aside, not deleted). A restore is itself checkpointed, so it can be undone. Rolling back cannot undo a push or a deploy.

## Project rules and skills

A `syntaxis.md` in the repository is treated as the project's rules. Other agents' files (`AGENTS.md`, `CLAUDE.md` and similar) are shown as advice only and can never override approvals, limits or secrets. A skill is a folder with a `SKILL.md` under `.syntaxis/skills/` (or `.claude/skills/`, `.agents/skills/`); only its name and description are in context until the agent loads it.

## Sharing a session

**Share** makes a disposable, read-only link to a session for a number of hours you choose (up to 30 days), with a name so you can tell links apart. Anyone with the link can read the conversation, and the command output if you allow it, but cannot change anything; your stored secrets are removed from what they see, and the view does not say who you are. **Settings → Shared links** lists your links with their expiry and number of views, lets you copy a link again, and revokes any of them at once. An unknown, expired or revoked link looks the same to a visitor.

## Secrets in commits

Commits and pushes are scanned for secrets (`off`, `warn` or `block`; in `block` mode a user can confirm an override unless the admin forbids it). Commit signing with a server-held SSH key is optional; the key never enters the sandbox.
