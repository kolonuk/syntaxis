// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState } from "react";
import { del, errText, get, post, put } from "../lib/api";
import { ago } from "../lib/format";
import { Banner, Button, Card, Input, Modal, Select, Skeleton, TextArea, useToast } from "../ui/ui";
import { RepoPicker } from "./RepoPicker";

interface Note {
  id: number;
  scope: "user" | "repo";
  repo: string;
  content: string;
  updated_at: string;
}

/** What the agent remembers across sessions: the user can read, add, edit and delete every note. */
export function Notes() {
  const toast = useToast();
  const [notes, setNotes] = useState<Note[] | null>(null);
  const [scope, setScope] = useState("user");
  const [repo, setRepo] = useState("");
  const [text, setText] = useState("");
  const [editing, setEditing] = useState<{ id: number; text: string } | null>(null);
  const load = () => get<Note[]>("/memory").then(setNotes);
  useEffect(() => {
    void load();
  }, []);

  async function add() {
    try {
      await post("/memory", { scope, repo, content: text });
      setText("");
      void load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  async function save() {
    if (!editing) return;
    try {
      await put(`/memory/${editing.id}`, { content: editing.text });
      setEditing(null);
      void load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  return (
    <Card title="Notes the agent remembers">
      <p className="muted">
        The agent can save short notes, such as how to run a repository&apos;s tests, and sees them
        at the start of every session. You approve each note it wants to save (unless a session is
        fully autonomous). Notes are information for the agent, not instructions that override its
        rules. Delete any you do not want kept.
      </p>
      {notes === null ? (
        <Skeleton lines={2} />
      ) : notes.length === 0 ? (
        <p className="muted">No notes yet.</p>
      ) : (
        <ul className="stack">
          {notes.map((n) => (
            <li key={n.id}>
              {editing?.id === n.id ? (
                <div className="stack">
                  <TextArea
                    label="Note"
                    rows={3}
                    maxLength={500}
                    value={editing.text}
                    onChange={(e) => setEditing({ id: n.id, text: e.target.value })}
                  />
                  <div className="row">
                    <Button variant="primary" onClick={() => void save()}>
                      Save
                    </Button>
                    <Button onClick={() => setEditing(null)}>Cancel</Button>
                  </div>
                </div>
              ) : (
                <div className="row">
                  <div className="grow">
                    <div>{n.content}</div>
                    <div className="muted">
                      {n.scope === "repo" ? `Repository ${n.repo}` : "About you"} ·{" "}
                      {ago(n.updated_at)}
                    </div>
                  </div>
                  <Button variant="ghost" onClick={() => setEditing({ id: n.id, text: n.content })}>
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={async () => {
                      await del(`/memory/${n.id}`);
                      void load();
                    }}
                    aria-label={`Delete note: ${n.content.slice(0, 40)}`}
                  >
                    Delete
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      <div className="stack">
        <h3>Add a note</h3>
        <Select
          label="About"
          value={scope}
          onChange={(e) => setScope(e.target.value)}
          options={[
            { value: "user", label: "Me (every session)" },
            { value: "repo", label: "One repository" },
          ]}
        />
        {scope === "repo" && (
          <>
            <p className="muted">
              The note is shown to the agent only in sessions that work on this repository. Choose
              it from the repositories of your connected git services.
            </p>
            <RepoPicker
              allowCreate={false}
              autoSelect
              legend="Repository"
              noneLabel="Choose a git service"
              onChange={(r) => setRepo(r?.full_name ?? "")}
            />
          </>
        )}
        <TextArea
          label="Note"
          rows={2}
          maxLength={500}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <Button
          variant="primary"
          onClick={() => void add()}
          disabled={!text.trim() || (scope === "repo" && !repo)}
        >
          Add note
        </Button>
      </div>
    </Card>
  );
}

interface Cmd {
  name: string;
  description: string;
  template: string;
  source?: string;
}

/** Saved prompts: typing /name in the message box sends the text, with $ARGUMENTS filled in. */
export function CommandsEditor() {
  const toast = useToast();
  const [cmds, setCmds] = useState<Cmd[] | null>(null);
  const [builtin, setBuiltin] = useState<Cmd[]>([]);
  // the command in the pop-up and where it goes in the list (-1 = a new one)
  const [edit, setEdit] = useState<{ at: number; cmd: Cmd } | null>(null);
  useEffect(() => {
    void get<{ commands: Cmd[]; builtin: Cmd[] }>("/commands").then((r) => {
      setCmds(r.commands);
      setBuiltin(r.builtin);
    });
  }, []);
  if (!cmds) return <Skeleton />;
  async function store(next: Cmd[]) {
    try {
      await put("/commands", { commands: next });
      setCmds(next);
      setEdit(null);
      toast("Commands saved.", "success");
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  return (
    <Card title="Slash commands">
      <p className="muted">
        A command is a saved prompt. Type <code>/name</code> and what to look at in the message box;{" "}
        <code>$ARGUMENTS</code> in the text is replaced by what you typed after the name. Built in:{" "}
        {builtin.map((b) => `/${b.name}`).join(", ")}. A repository can add its own in{" "}
        <code>.syntaxis/commands/&lt;name&gt;.md</code>. Your own win over those with the same name.
      </p>
      {cmds.length === 0 ? (
        <p className="muted">You have no commands of your own yet.</p>
      ) : (
        <ul className="list-rows">
          {cmds.map((c, i) => (
            <li key={`${c.name}-${i}`}>
              <div className="grow">
                <code>/{c.name}</code>
                {c.description && <span className="muted"> {c.description}</span>}
              </div>
              <Button variant="ghost" onClick={() => setEdit({ at: i, cmd: { ...c } })}>
                Edit
              </Button>
              <Button
                variant="ghost"
                aria-label={`Delete /${c.name}`}
                onClick={() => void store(cmds.filter((_, j) => j !== i))}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Button
        variant="primary"
        onClick={() => setEdit({ at: -1, cmd: { name: "", description: "", template: "" } })}
      >
        Add a command
      </Button>
      {edit && (
        <Modal
          title={edit.at < 0 ? "Add a command" : `Edit /${cmds[edit.at]?.name ?? ""}`}
          onClose={() => setEdit(null)}
          actions={
            <>
              <Button onClick={() => setEdit(null)}>Cancel</Button>
              <Button
                variant="primary"
                disabled={!edit.cmd.name.trim() || !edit.cmd.template.trim()}
                onClick={() =>
                  void store(
                    edit.at < 0
                      ? [...cmds, edit.cmd]
                      : cmds.map((c, j) => (j === edit.at ? edit.cmd : c)),
                  )
                }
              >
                Save
              </Button>
            </>
          }
        >
          <div className="stack">
            <Input
              label="Name"
              value={edit.cmd.name}
              onChange={(e) => setEdit({ ...edit, cmd: { ...edit.cmd, name: e.target.value } })}
              placeholder="deploy-check"
            />
            <Input
              label="Description"
              value={edit.cmd.description}
              onChange={(e) =>
                setEdit({ ...edit, cmd: { ...edit.cmd, description: e.target.value } })
              }
            />
            <TextArea
              label="Text"
              rows={6}
              value={edit.cmd.template}
              onChange={(e) => setEdit({ ...edit, cmd: { ...edit.cmd, template: e.target.value } })}
            />
          </div>
        </Modal>
      )}
    </Card>
  );
}

const HOOK_EXAMPLE = `{
  "hooks": [
    {
      "event": "post_tool",
      "tools": ["write_file", "patch_file", "apply_patch"],
      "command": "gofmt -l ."
    }
  ]
}`;

export const HOOKS_HELP =
  "https://github.com/kolonuk/syntaxis/blob/main/docs/agent.md#writing-hooks";

interface HookItem {
  event: "pre_tool" | "post_tool";
  tools?: string[];
  command: string;
  timeout_seconds?: number;
}

/** The hooks list in the setting, or null when the text is not a valid hooks file. */
export function parseHooks(text: string): HookItem[] | null {
  if (!text.trim()) return [];
  try {
    const v = JSON.parse(text) as { hooks?: HookItem[] };
    return Array.isArray(v.hooks) ? v.hooks : null;
  } catch {
    return null;
  }
}

/** Commands that run before or after the agent's tool calls. */
export function HooksEditor() {
  const toast = useToast();
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [raw, setRaw] = useState(false);
  const [edit, setEdit] = useState<{ at: number; hook: HookItem; tools: string } | null>(null);
  useEffect(() => {
    void get<{ hooks: string }>("/hooks").then((r) => setText(r.hooks));
  }, []);
  if (text === null) return <Skeleton />;
  const list = parseHooks(text);
  async function save(next: string) {
    setError("");
    try {
      await put("/hooks", { hooks: next });
      setText(next);
      setEdit(null);
      toast("Hooks saved.", "success");
    } catch (x) {
      setError(errText(x));
    }
  }
  const write = (l: HookItem[]) => JSON.stringify({ hooks: l }, null, 2);
  return (
    <Card title="Hooks">
      <p className="muted">
        A hook runs a command in the sandbox before (<code>pre_tool</code>) or after (
        <code>post_tool</code>) the agent uses a tool. A pre hook that fails stops the tool call; a
        post hook&apos;s output is shown to the agent, for example a formatter or a linter. Hooks
        never ask for approval, so a command that would need it, or that is blocked, is not run. A
        repository can add its own in <code>.syntaxis/hooks.json</code> (you are told when a session
        uses them).
      </p>
      <p>
        <a href={HOOKS_HELP} target="_blank" rel="noreferrer">
          How to write hooks, with examples
        </a>
      </p>
      {error && <Banner tone="danger">{error}</Banner>}
      {raw || list === null ? (
        <>
          {list === null && (
            <Banner tone="warn">
              The saved hooks are not a list this screen can show, so they are shown as text.
            </Banner>
          )}
          <TextArea
            label="Hooks (JSON)"
            className="textarea code-editor"
            rows={10}
            spellCheck={false}
            value={text}
            placeholder={HOOK_EXAMPLE}
            onChange={(e) => setText(e.target.value)}
          />
          <div className="row">
            <Button variant="primary" onClick={() => void save(text)}>
              Save hooks
            </Button>
            <Button variant="ghost" onClick={() => setText(HOOK_EXAMPLE)}>
              Show an example
            </Button>
            {list !== null && (
              <Button variant="ghost" onClick={() => setRaw(false)}>
                Back to the list
              </Button>
            )}
          </div>
        </>
      ) : (
        <>
          {list.length === 0 ? (
            <p className="muted">
              You have no hooks. For example, run a formatter after every edit.
            </p>
          ) : (
            <ul className="list-rows">
              {list.map((h, i) => (
                <li key={i}>
                  <div className="grow">
                    <code>{h.command}</code>
                    <div className="muted">
                      {h.event === "pre_tool" ? "before" : "after"}{" "}
                      {h.tools && h.tools.length > 0 ? h.tools.join(", ") : "every tool"}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    onClick={() =>
                      setEdit({ at: i, hook: { ...h }, tools: (h.tools ?? []).join(", ") })
                    }
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label={`Delete hook ${h.command}`}
                    onClick={() => void save(write(list.filter((_, j) => j !== i)))}
                  >
                    Delete
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <div className="row">
            <Button
              variant="primary"
              onClick={() =>
                setEdit({ at: -1, hook: { event: "post_tool", command: "" }, tools: "" })
              }
            >
              Add a hook
            </Button>
            <Button variant="ghost" onClick={() => setRaw(true)}>
              Edit as JSON
            </Button>
          </div>
          {edit && (
            <Modal
              title={edit.at < 0 ? "Add a hook" : "Edit hook"}
              onClose={() => setEdit(null)}
              actions={
                <>
                  <Button onClick={() => setEdit(null)}>Cancel</Button>
                  <Button
                    variant="primary"
                    disabled={!edit.hook.command.trim()}
                    onClick={() => {
                      const tools = edit.tools
                        .split(",")
                        .map((t) => t.trim())
                        .filter(Boolean);
                      const h: HookItem = { ...edit.hook, command: edit.hook.command.trim() };
                      if (tools.length) h.tools = tools;
                      else delete h.tools;
                      void save(
                        write(
                          edit.at < 0 ? [...list, h] : list.map((x, j) => (j === edit.at ? h : x)),
                        ),
                      );
                    }}
                  >
                    Save
                  </Button>
                </>
              }
            >
              <div className="stack">
                <Select
                  label="Runs"
                  value={edit.hook.event}
                  onChange={(e) =>
                    setEdit({
                      ...edit,
                      hook: { ...edit.hook, event: e.target.value as HookItem["event"] },
                    })
                  }
                  options={[
                    { value: "post_tool", label: "After a tool (a formatter or linter)" },
                    { value: "pre_tool", label: "Before a tool (a failing hook stops it)" },
                  ]}
                />
                <Input
                  label="Tools (optional)"
                  hint="Comma separated, for example write_file, patch_file. Empty means every tool."
                  value={edit.tools}
                  onChange={(e) => setEdit({ ...edit, tools: e.target.value })}
                />
                <Input
                  label="Command"
                  hint="Runs in /workspace, for example gofmt -l ."
                  value={edit.hook.command}
                  onChange={(e) =>
                    setEdit({ ...edit, hook: { ...edit.hook, command: e.target.value } })
                  }
                />
              </div>
            </Modal>
          )}
        </>
      )}
    </Card>
  );
}
