# API

Syntaxis has a small HTTP API for scripts and tools: look at sessions, start one, send it a message, follow it, answer its questions. It is the same API the chat app uses, limited to what a personal API token may do. This page lists what a token can reach. The rest of the app's `/api` is internal and changes without notice.

## Tokens

Make a token in the chat app under **Settings → API tokens**. Pick a name, a scope and when it expires. The token is shown once and stored only as a hash. You can revoke it there at any time, and an admin can revoke it too. A disabled user's tokens stop working at once, and so do expired and revoked ones. You can have up to 20.

Send it as a bearer token to the chat app's address (the one you sign in at, never the admin app):

```sh
curl -H "Authorization: Bearer syx_..." https://syntaxis.example.com/api/sessions
```

A token acts as you, with your models, quotas, approvals and audit log. Changes made with a token are recorded in the audit log under the token. No CSRF header is needed because there is no cookie.

## Scopes

| Scope | What it can do |
| --- | --- |
| `read` | Look at your sessions and what is in them: messages, the live event stream, changes (diff), checkpoints, files (list, read, download), resources, the model and environment lists, your usage. |
| `run` | Everything in `read`, and start a session, send a message, stop a run, answer approvals and questions, approve a plan, and branch a session. |

**A token can never** read or change your secrets or connections, settings, notes, hooks, slash commands, MCP choices or automations; make, list or revoke tokens; open the terminal; upload, edit or delete files; create share links; trust repository hooks; delete a session; or change your password or two-factor settings. Anything not in the table below is refused with `403`. A new endpoint is closed to tokens until it is added on purpose, and a test checks that.

`read` sees everything in a session, including whatever the agent printed or wrote. Give a token only to a tool you trust with that.

## Endpoints

All paths start with `/api`. Bodies and replies are JSON. Errors are `{"error": "..."}` with the usual status code: `401` for a missing, wrong, expired or revoked token, `403` when the scope does not allow it, `404` for something that is not yours.

### `read`

| Method and path | Returns |
| --- | --- |
| `GET /me` | Your name, email and level, what uploads the admin allows, and `api_token` (the token's name and scope). |
| `GET /models` | The models you may use, as `provider/model`. |
| `GET /usage` | Your token use against your limits. |
| `GET /environments` | The sandbox environments you can pick. |
| `GET /sessions` | Your sessions. |
| `GET /sessions/{id}` | One session: `id`, `title`, `model`, `autonomy`, `state` (`running` while the agent works), `tokens_in`, `tokens_out`, `repo`, `repo_branch`, `plan_mode`, `plan_state`, `created_at`, `updated_at`. |
| `GET /sessions/{id}/messages` | The messages: `id`, `role`, `content`, `tool_calls`, `created_at`. `?abandoned=1` includes branches that were left. |
| `GET /sessions/{id}/events` | A live stream (server-sent events). It replays what the current run has done so far, then continues until the run ends. When nothing is running it sends `event: idle` and closes. |
| `GET /sessions/{id}/diff` | What the agent changed in the repository. |
| `GET /sessions/{id}/checkpoints` and `/checkpoints/{n}/diff` | The checkpoints taken before the agent's turns, and what changed in one. |
| `GET /sessions/{id}/export` | The whole session as Markdown, or as JSON with `?format=json`. |
| `GET /sessions/{id}/resources` | CPU, memory, workspace size and hook time of the sandbox. |
| `GET /sessions/{id}/files?path=` | A folder of the workspace. |
| `GET /sessions/{id}/files/content?path=` and `/files/download?path=` | A file, as text or as a download. |

### `run`

| Method and path | Body | Does |
| --- | --- | --- |
| `POST /sessions` | `{"model": "provider/model", "title": "", "autonomy": "ask", "plan": false, "repo": {...}}` | Starts a session. `repo` is optional: `{"kind": "github", "full_name": "owner/name", "clone_url": "https://...", "default_branch": "main"}`, using your own connection. Returns the session with status `201`. |
| `POST /sessions/{id}/messages` | `{"text": "...", "attachments": []}` | Sends a message and starts the agent (`202`). Slash commands work as in the app. `attachments` are paths from uploads made in the app. |
| `POST /sessions/{id}/stop` | – | Stops the current run. |
| `POST /sessions/{id}/approvals/{call}` | `{"approve": true}` | Answers an approval request. `call` is the `call_id` of the `approval_request` event. |
| `POST /sessions/{id}/answer` | `{"text": "..."}` | Answers a question the agent asked. |
| `POST /sessions/{id}/plan` | `{"action": "approve", "plan": "..."}` or `{"action": "revise", "feedback": "..."}` | Approves (optionally with an edited plan) or asks for changes to a plan. |
| `POST /sessions/{id}/fork` | `{"message_id": 12}` | Branches the session at a message. |

### Events

The event stream sends `event: <kind>` and a JSON `data:` line. The kinds are `assistant_delta` (streamed text), `assistant_message`, `tool_call`, `tool_result`, `approval_request`, `approval_result`, `question`, `context`, `compaction`, `checkpoint`, `status`, `plan`, `phase`, `notice` and `todos`. The stream is for following a run; read `GET /sessions/{id}/messages` for the finished record. A client should reconnect after a drop: it gets the run's events again from the start.

## Example: run a task and wait

```sh
H="Authorization: Bearer $SYNTAXIS_TOKEN"; B=https://syntaxis.example.com/api
ID=$(curl -s -H "$H" -H 'Content-Type: application/json' -d '{"model":"llm/coder","autonomy":"ask"}' $B/sessions | jq -r .id)
curl -s -H "$H" -H 'Content-Type: application/json' -d '{"text":"Summarise the README"}' $B/sessions/$ID/messages
curl -N -H "$H" $B/sessions/$ID/events          # follow it; approvals still wait for a person or a `run` token
curl -s -H "$H" $B/sessions/$ID/messages | jq -r '.[-1].content'
```

## Stability

This page is the supported surface. It may still change before 1.0; changes are listed in the [changelog](../CHANGELOG.md). A versioned, machine-readable description is planned.
