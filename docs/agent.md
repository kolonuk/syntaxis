# The agent

The agent is a loop the Syntaxis server runs to turn a model into an engineer working **inside the user's sandbox**: the model is sent the conversation and a set of tools, asks to use them, the server runs each one in the sandbox and returns the result, and this repeats until the model is done, the user stops it, or a limit is hit. It runs in the background and notifies the user when it finishes or needs them.

## Tools

Run a shell command, read, write and patch files, list and search files, git (including opening a pull request), fetch a URL, search the web (when set up), keep a task list, save notes, ask the user a question, and load a skill. Tools from approved [MCP servers](integrations.md#mcp-servers) are added. File paths are confined to the workspace.

**Editing.** `patch_file` replaces one block of text, and `apply_patch` makes several edits to one file, or applies a unified diff, all or nothing. Weak models often copy a block with the wrong indentation, tabs for spaces or stray trailing spaces, so when the exact text is not found, a whole-line match that ignores differences in spacing is tried and the replacement is re-indented to the file; the result says when that happened. A block that matches in more than one place is never guessed at, and when nothing matches the error shows the closest text in the file with its line numbers. In a diff, the line numbers of the `@@` lines are ignored and each hunk is found by its content.

**Task list.** For a job with three or more steps the agent keeps a list with `todo_write`. It is shown above the message box as progress (done, in progress, to do) and is still there after a reload.

**Notes.** The agent can save short notes that it sees at the start of every later session (`remember`, `forget`): how to run a repository's tests, a preference of yours. A note outlives the session, so each one is shown to you for approval first (in a fully autonomous session it is saved without asking, and audited). Notes are scanned for secrets, kept per user (a note about a repository only appears in sessions on that repository), and listed, edited and deleted under **Settings → Notes**. They are shown to the model as information, never as instructions that can change its rules.

**Web search.** With a [SearXNG](https://docs.searxng.org/) address set in the admin app (**Settings → Web search server**), the agent gets a `web_search` tool. The Syntaxis server makes the request, results are marked as untrusted, and a query that contains a secret is refused. Without an address there is no such tool; `fetch_url` still works for pages the agent already has an address for.

**Reading PDFs and pictures.** `read_file` on a PDF returns its text; on a scan or a picture (png, jpg, gif, webp, bmp, tiff) it returns the text found by OCR. This runs inside the sandbox with poppler and Tesseract (English), which the default sandbox image contains; a custom image without them gets a plain message saying so.

Local models are not always good at calling tools. Syntaxis recovers calls written as plain text, retries malformed ones, stops loops and repeated failures, and offers chat-only mode for models without tool support.

## Autonomy and approvals

Each session has an autonomy level: **ask** (every action waits for you), **workspace** (the default: routine work runs, risky actions wait) or **full** (everything runs within limits, risky actions are still logged). Risky means things like force pushes, deploys, cloud changes, `sudo`, deleting outside the workspace and opening pull requests. The approval request says which of the user's connections the command would use. Commands that target the local network are blocked outright at every level.

## Sessions

Sessions are named from the first exchange (rename them any time). Where an admin has set a helper model it does this and other small jobs; see [configuration](configuration.md#providers-and-models). A workspace that is not used for a while is deleted after a warning by email (the admin sets how long, 30 days by default).

## Attachments and pictures

The paperclip in the message box attaches files (also by dragging them in, or pasting a picture), up to 8 per message and 20 MB each. They are uploaded to `/workspace/.attachments` in the session's sandbox and listed at the end of the message, so the agent opens them with `read_file`. If the model can see pictures, the first four are also sent to it directly: Ollama says so itself, and for other servers an admin lists the model names under **Settings → Models that accept pictures**. Other models read a picture as text with OCR. Pictures stay visible to the model for the last two messages that carry them. Attachments count against the workspace size limit.

## Files

The **Files** tab browses the session's workspace: open a folder, view or edit a text file (up to 1 MB, **Ctrl+S** saves), download, upload, create folders and delete. It works through the session's sandbox like everything else, is confined to `/workspace`, and changes are refused while the agent is working so they cannot collide with its own edits. The editor is a plain text box, without syntax highlighting.

## Slash commands and hooks

A **slash command** is a saved prompt: type `/review the login code` and the command's text is sent with `$ARGUMENTS` replaced by what follows the name. Three come with Syntaxis (`/review`, `/test`, `/explain`); you can add your own under **Settings → Commands**, and a repository can add some in `.syntaxis/commands/<name>.md` (an optional first line `description: ...`). Your own win over the repository's, which win over the built-in ones. The expanded text is what is sent and shown in the chat.

A **hook** runs a command in the sandbox before (`pre_tool`) or after (`post_tool`) a tool call: a formatter after every edit, a check that refuses writes to a protected path. A failing pre hook stops the tool call and the agent is told why; a post hook's output is added to the tool's result. Hooks are written as JSON under **Settings → Hooks**, or in a repository's `.syntaxis/hooks.json` (the session says when it uses them; an admin can switch repository hooks off with the `repo_hooks` setting). A hook never asks for approval, so a hook command that the approval policy would stop or ask about is not run at all, and the user is told.

```json
{"hooks":[{"event":"post_tool","tools":["write_file","patch_file","apply_patch"],"command":"gofmt -l ."}]}
```

## Editing and branching a conversation

**Edit and resend** on one of your messages sets that message and everything after it aside and runs the agent again from there; **Regenerate** on the last reply does the same with your original words. Set-aside messages stay in the history and the export. Files the agent already changed are not put back (use the **Checkpoints** tab for that). **Branch from here** on a reply starts a new session with a copy of the conversation up to it, on the same model and workspace. In a session on a repository the new session gets its own working branch, started where the old one is, so the two can go different ways.

## Plan first and reviewing changes

With **Plan first** the agent can only read until you approve its plan. It explores, proposes a numbered plan, and stops; you edit and approve it, or ask for changes. The server enforces this: the planning step has no way to change anything. The **Changes** tab shows what the agent changed, file by file, and a comment on a file can be sent back to the agent.

## Parallel attempts

A new session can run the same task 2 or more times at once. Each attempt gets its own sandbox clone and branch (`syntaxis/<id>`), and the compare view shows their changes side by side; **Keep this one** removes the rest. The offered number is the smallest of: the machine (CPUs and memory against the sandbox limits), your remaining concurrent sessions, and the model server's concurrency (extra attempts would only wait in its queue). A model that is not loaded yet is flagged, since the first attempt loads it.

## Context and checkpoints

The agent budgets against the model's **real** context window rather than the advertised one (many servers silently run with less; see [troubleshooting](pitfalls.md#context-windows)). Long conversations are summarised automatically and the full history is kept. Before each turn and before any risky action the workspace is snapshotted into a hidden git repository, separate from the user's own `.git`, so work can be rolled back. The **Checkpoints** tab lists them with what changed since each one, and restores the files, or the files and the conversation (later messages are set aside, not deleted). A restore is itself checkpointed, so it can be undone. Rolling back cannot undo a push or a deploy.

## Project rules and skills

A `syntaxis.md` in the repository is treated as the project's rules. Other agents' files (`AGENTS.md`, `CLAUDE.md` and similar) are shown as advice only and can never override approvals, limits or secrets. A skill is a folder with a `SKILL.md` under `.syntaxis/skills/` (or `.claude/skills/`, `.agents/skills/`); only its name and description are in context until the agent loads it.

## Sharing a session

**Share** makes a disposable, read-only link to a session for a number of hours you choose (up to 30 days), with a name so you can tell links apart. Anyone with the link can read the conversation, and the command output if you allow it, but cannot change anything; your stored secrets are removed from what they see, and the view does not say who you are. **Settings → Shared links** lists your links with their expiry and number of views, lets you copy a link again, and revokes any of them at once. An unknown, expired or revoked link looks the same to a visitor.

## Secrets in commits

Commits and pushes are scanned for secrets (`off`, `warn` or `block`; in `block` mode a user can confirm an override unless the admin forbids it). Commit signing with a server-held SSH key is optional; the key never enters the sandbox.
