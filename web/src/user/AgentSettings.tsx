// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState } from "react";
import { del, errText, get, post, put } from "../lib/api";
import { ago } from "../lib/format";
import { Banner, Button, Card, Input, Select, Skeleton, TextArea, useToast } from "../ui/ui";

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
          <Input
            label="Repository"
            hint="owner/name, as shown on the session"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
          />
        )}
        <TextArea
          label="Note"
          rows={2}
          maxLength={500}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <Button variant="primary" onClick={() => void add()} disabled={!text.trim()}>
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
  useEffect(() => {
    void get<{ commands: Cmd[]; builtin: Cmd[] }>("/commands").then((r) => {
      setCmds(r.commands);
      setBuiltin(r.builtin);
    });
  }, []);
  if (!cmds) return <Skeleton />;
  const set = (i: number, c: Partial<Cmd>) =>
    setCmds(cmds.map((x, j) => (j === i ? { ...x, ...c } : x)));
  async function save() {
    try {
      await put("/commands", { commands: cmds });
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
      <div className="stack">
        {cmds.map((c, i) => (
          <div className="stack card-inner" key={i}>
            <Input
              label="Name"
              value={c.name}
              onChange={(e) => set(i, { name: e.target.value })}
              placeholder="deploy-check"
            />
            <Input
              label="Description"
              value={c.description}
              onChange={(e) => set(i, { description: e.target.value })}
            />
            <TextArea
              label="Text"
              rows={4}
              value={c.template}
              onChange={(e) => set(i, { template: e.target.value })}
            />
            <Button variant="ghost" onClick={() => setCmds(cmds.filter((_, j) => j !== i))}>
              Remove /{c.name || "command"}
            </Button>
          </div>
        ))}
        <div className="row">
          <Button onClick={() => setCmds([...cmds, { name: "", description: "", template: "" }])}>
            Add a command
          </Button>
          <Button variant="primary" onClick={() => void save()}>
            Save commands
          </Button>
        </div>
      </div>
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

/** Commands that run before or after the agent's tool calls. */
export function HooksEditor() {
  const toast = useToast();
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void get<{ hooks: string }>("/hooks").then((r) => setText(r.hooks));
  }, []);
  if (text === null) return <Skeleton />;
  async function save() {
    setError("");
    try {
      await put("/hooks", { hooks: text });
      toast("Hooks saved.", "success");
    } catch (x) {
      setError(errText(x));
    }
  }
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
      {error && <Banner tone="danger">{error}</Banner>}
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
        <Button variant="primary" onClick={() => void save()}>
          Save hooks
        </Button>
        <Button variant="ghost" onClick={() => setText(HOOK_EXAMPLE)}>
          Show an example
        </Button>
      </div>
    </Card>
  );
}
