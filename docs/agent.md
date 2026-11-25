# The agent

The agent is a loop the server runs against a model. The model gets the conversation and a set of tools, asks to use them, the server runs each tool in the user's sandbox and returns the result, and this repeats until the model is done, the user stops it, or a limit is hit. It runs in the background and notifies the user when it finishes or needs them.

## Tools

Run a shell command, read, write and patch files, list and search files, git (including opening a pull request), fetch a URL, search the web (when set up), keep a task list, save notes, ask the user a question, load a skill. Tools from approved [MCP servers](integrations.md#mcp-servers) are added. File paths are confined to the workspace.

**Editing.** `patch_file` replaces one block of text. `apply_patch` makes several edits to one file, or applies a unified diff, all or nothing. If the exact text is not found, a whole-line match that ignores spacing differences is tried and the replacement is re-indented to the file; the result says so. A block that matches in more than one place is never guessed at. When nothing matches, the error shows the closest text with line numbers. In a diff, the `@@` line numbers are ignored and each hunk is found by its content.

**Task list.** For jobs of three or more steps the agent keeps a list with `todo_write`. It shows above the message box and survives a reload.

**Notes.** `remember` and `forget` save short notes (how to run a repository's tests, a preference) that the agent sees at the start of every later session. Each note is shown to you for approval first; in a fully autonomous session it is saved without asking and audited. Notes are scanned for secrets and kept per user; a note about a repository only appears in sessions on that repository. List, edit and delete them under **Settings → Notes**. They are given to the model as information, never as instructions that change its rules.

**Web search.** Set a [SearXNG](https://docs.searxng.org/) address in the admin app (**Settings → Web search server**) to give the agent a `web_search` tool. The server makes the request, results are marked untrusted, and a query containing a secret is refused. Without an address there is no such tool; `fetch_url` still works for known addresses.

**PDFs and pictures.** `read_file` on a PDF returns its text. On a scan or picture (png, jpg, gif, webp, bmp, tiff) it returns OCR text (English). This needs poppler and Tesseract in the sandbox image; the default image has them, and a custom image without them returns a message saying so.

**Weak tool callers.** Calls written as plain text are recovered, malformed calls are retried, loops and repeated failures are stopped. A model without tool support can be used in chat-only mode.

## Autonomy and approvals

Each session has an autonomy level:

| Level | Behaviour |
| --- | --- |
| `ask` | Every action waits for approval. |
| `workspace` (default) | Routine work runs; risky actions wait. |
| `full` | Everything runs within limits; risky actions are still logged. |

Risky means force pushes, deploys, cloud changes, `sudo`, deleting outside the workspace and opening pull requests. An approval request names the connections the command would use. Commands that target the local network are blocked at every level.

## Sessions

Sessions are named from the first exchange; rename them at any time. If an admin has set a helper model, it does this and other small jobs (see [configuration](configuration.md#providers-and-models)). An unused workspace is deleted after a warning email; the admin sets how long (default 30 days).

## Attachments

Uploads are **off until an admin turns them on** (Policies → Allow file uploads). The admin also sets the largest file (default 10 MB) and the allowed kinds.

- Attach with the paperclip, by dragging files in, or by pasting a picture. Up to 8 per message.
- Accepted: pictures, PDFs, office documents and plain text. Each file is checked by content. Programs, scripts, archives, macros, embedded files and encoded data are refused with the reason (see [security](security.md)).
- Files go to `/workspace/.attachments` in the sandbox and are listed at the end of the message; the agent opens them with `read_file`. They count against the workspace size limit.
- If the model accepts pictures, the first four are also sent to it directly, for the last two messages that carry them. Ollama reports this itself; for other servers list the model names under **Settings → Models that accept pictures**. Other models get picture text from OCR.

## Files

The **Files** tab browses the session workspace: open folders, view or edit text files (up to 1 MB, **Ctrl+S** saves), download, upload, create folders, delete. It is confined to `/workspace` and works through the sandbox. Changes are refused while the agent is working. The editor has no syntax highlighting.

## Slash commands and hooks

A **slash command** is a saved prompt. `/review the login code` sends the command's text with `$ARGUMENTS` replaced by what follows the name. `/review`, `/test` and `/explain` are built in. Add your own under **Settings → Commands**, or put them in a repository as `.syntaxis/commands/<name>.md` (an optional first line `description: ...`). Precedence: your own, then the repository's, then built-in.

A **hook** runs a command in the sandbox before (`pre_tool`) or after (`post_tool`) a tool call.

- A failing pre hook stops the tool call and tells the agent why. A post hook's output is added to the tool result.
- Write hooks as JSON under **Settings → Hooks**, or in the repository's `.syntaxis/hooks.json`.
- Repository hooks do not run until you read them in the banner above the message box and trust that exact file. An admin can switch them off with the `repo_hooks` setting.
- Hooks get 5 minutes per run in total; the time shows on the Resources tab.
- A hook never asks for approval. A hook command that the approval policy would stop or ask about is not run, and the user is told.

```json
{"hooks":[{"event":"post_tool","tools":["write_file","patch_file","apply_patch"],"command":"gofmt -l ."}]}
```

## Editing and branching a conversation

- **Edit and resend** on your message sets that message and everything after it aside and runs the agent again from there. **Regenerate** on the last reply does the same with your original words. Set-aside messages stay in the history and the export.
- Files the agent already changed are not restored by this. Use the **Checkpoints** tab.
- **Branch from here** starts a new session with a copy of the conversation up to that reply, on the same model and workspace. For a repository session, the new session gets its own working branch starting where the old one is.

## Plan first and reviewing changes

With **Plan first** the agent can only read until you approve its plan. It explores, proposes a numbered plan and stops; edit and approve it, or ask for changes. The server enforces this: the planning step has no way to change anything.

The **Changes** tab shows what the agent changed, file by file. A comment on a file can be sent back to the agent.

## Parallel attempts

A new session can run the same task 2 or more times at once. Each attempt gets its own sandbox clone and branch (`syntaxis/<id>`). The compare view shows the changes side by side, and **Keep this one** removes the rest.

The number offered is the smallest of: what the machine can run (CPUs and memory against the sandbox limits), your remaining concurrent sessions, and the model server's concurrency. A model that is not loaded yet is flagged, because the first attempt loads it.

## Context and checkpoints

The agent budgets against the model's real context window, not the advertised one (see [troubleshooting](pitfalls.md#context-windows)). Long conversations are summarised automatically and the full history is kept.

Before each turn and before any risky action, the workspace is snapshotted into a hidden git repository separate from the user's own `.git`. The **Checkpoints** tab lists them with what changed since each one, and restores either the files, or the files and the conversation (later messages are set aside, not deleted). A restore is itself checkpointed, so it can be undone. A restore cannot undo a push or a deploy.

## Project rules and skills

A `syntaxis.md` in the repository is the project's rules. Other agents' files (`AGENTS.md`, `CLAUDE.md` and similar) are advice only and never override approvals, limits or secrets.

A skill is a folder with a `SKILL.md` under `.syntaxis/skills/`, `.claude/skills/` or `.agents/skills/`. Only its name and description are in context until the agent loads it.

## Sharing a session

**Share** creates a read-only link for a number of hours you choose (up to 30 days), with a name to tell links apart. Anyone with the link can read the conversation, and the command output if you allow it. They cannot change anything, your stored secrets are removed from what they see, and the view does not show who you are.

**Settings → Shared links** lists your links with expiry and view count, copies a link again, and revokes any link at once. Unknown, expired and revoked links look the same to a visitor.

## Secrets in commits

Commits and pushes are scanned for secrets in mode `off`, `warn` or `block`. In `block` mode a user can confirm an override unless the admin forbids it. Commit signing with a server-held SSH key is optional; the key never enters the sandbox.
