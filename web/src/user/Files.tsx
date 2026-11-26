// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useRef, useState } from "react";
import { del, errText, get, post, put, upload } from "../lib/api";
import { acceptFor, fileSize, MAX_UPLOAD } from "../lib/attachments";
import type { Uploads } from "./hooks";
import { ago } from "../lib/format";
import { Banner, Button, Input, Modal, Skeleton, TextArea, useToast } from "../ui/ui";

interface Entry {
  name: string;
  type: "dir" | "file" | "link" | "other";
  size: number;
  modified: string;
}
interface Listing {
  available: boolean;
  reason?: string;
  path?: string;
  entries?: Entry[];
  truncated?: boolean;
}
interface Opened {
  path: string;
  size: number;
  binary: boolean;
  truncated?: boolean;
  text?: string;
}

const ROOT = "/workspace";
export const FILES_POLL_MS = 4000;

const join = (dir: string, name: string) => (dir.endsWith("/") ? dir : dir + "/") + name;
const parent = (p: string) =>
  p === ROOT ? ROOT : p.slice(0, Math.max(p.lastIndexOf("/"), ROOT.length));
const q = (p: string) => encodeURIComponent(p);

/** Where each part of a path leads, for the breadcrumb. */
export function crumbs(path: string): { label: string; path: string }[] {
  const out = [{ label: "workspace", path: ROOT }];
  const rest = path.startsWith(ROOT + "/") ? path.slice(ROOT.length + 1) : "";
  let cur = ROOT;
  for (const part of rest.split("/").filter(Boolean)) {
    cur = join(cur, part);
    out.push({ label: part, path: cur });
  }
  return out;
}

/** The files of the session's workspace: browse, view, edit, download, upload and delete. */
export function Files({
  sessionId,
  running,
  uploads,
}: {
  sessionId: string;
  running: boolean;
  uploads?: Uploads;
}) {
  const toast = useToast();
  const base = `/sessions/${sessionId}`;
  const [path, setPath] = useState(ROOT);
  const [list, setList] = useState<Listing | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Opened | null>(null);
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirm, setConfirm] = useState<Entry | null>(null);
  const [folderName, setFolderName] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  const load = useCallback(
    async (p: string) => {
      setError("");
      try {
        setList(await get<Listing>(`${base}/files?path=${q(p)}`));
        setPath(p);
      } catch (x) {
        setError(errText(x));
      }
    },
    [base],
  );

  useEffect(() => {
    setOpen(null);
    setList(null);
    void load(ROOT);
  }, [sessionId, load]);

  // after the agent has worked, what is on disk has changed
  useEffect(() => {
    if (!running) void load(path);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  // keep the list current: look again every few seconds while the tab is showing, quietly, so a
  // failed look does not replace what is on screen
  useEffect(() => {
    const t = setInterval(() => {
      if (document.hidden) return;
      get<Listing>(`${base}/files?path=${q(path)}`)
        .then((l) => setList(l))
        .catch(() => undefined);
    }, FILES_POLL_MS);
    return () => clearInterval(t);
  }, [base, path]);

  async function openFile(p: string) {
    try {
      const f = await get<Opened>(`${base}/files/content?path=${q(p)}`);
      setOpen(f);
      setText(f.text ?? "");
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function save() {
    if (!open) return;
    setSaving(true);
    try {
      await put(`${base}/files/content`, { path: open.path, text });
      setOpen({ ...open, text, size: new Blob([text]).size });
      toast("Saved.", "success");
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setSaving(false);
    }
  }

  async function uploadFiles(files: FileList | null) {
    if (!files?.length) return;
    setBusy(true);
    try {
      for (const f of Array.from(files)) {
        const maxUpload = uploads?.max_bytes || MAX_UPLOAD;
        if (f.size > maxUpload) {
          toast(`${f.name} is larger than ${Math.round(maxUpload / 1024 / 1024)} MB.`, "error");
          continue;
        }
        await upload(`${base}/files/upload?dir=${q(path)}&name=${q(f.name)}`, f);
      }
      await load(path);
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
      if (picker.current) picker.current.value = "";
    }
  }

  async function makeFolder() {
    const name = (folderName ?? "").trim();
    if (!name) return;
    try {
      await post(`${base}/files/mkdir`, { path: join(path, name) });
      setFolderName(null);
      await load(path);
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  async function remove(e: Entry) {
    try {
      await del(`${base}/files?path=${q(join(path, e.name))}`);
      setConfirm(null);
      if (open && open.path.startsWith(join(path, e.name))) setOpen(null);
      await load(path);
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  if (error && !list) return <Banner tone="danger">{error}</Banner>;
  if (!list) return <Skeleton />;
  if (!list.available)
    return (
      <div className="files">
        <p className="muted">{list.reason ?? "Nothing has run in this session yet."}</p>
        {uploads?.enabled ? (
          <>
            <input
              ref={picker}
              type="file"
              multiple
              hidden
              accept={acceptFor(uploads.kinds)}
              aria-label="Choose files to upload"
              onChange={(e) => void uploadFiles(e.target.files)}
            />
            <Button onClick={() => picker.current?.click()} loading={busy}>
              Upload files
            </Button>
          </>
        ) : (
          <p className="muted">File uploads are switched off by the administrator.</p>
        )}
      </div>
    );

  const dirty = open && !open.binary && text !== (open.text ?? "");

  return (
    <div className="files">
      <nav className="crumbs" aria-label="Folder">
        {crumbs(path).map((c, i, all) => (
          <span key={c.path}>
            {i === all.length - 1 ? (
              <strong>{c.label}</strong>
            ) : (
              <button className="link" onClick={() => void load(c.path)}>
                {c.label}
              </button>
            )}
            {i < all.length - 1 && " / "}
          </span>
        ))}
      </nav>
      <div className="row files-actions">
        {uploads?.enabled && (
          <>
            <input
              ref={picker}
              type="file"
              multiple
              hidden
              accept={acceptFor(uploads.kinds)}
              aria-label="Choose files to upload"
              onChange={(e) => void uploadFiles(e.target.files)}
            />
            <Button onClick={() => picker.current?.click()} loading={busy} disabled={running}>
              Upload
            </Button>
          </>
        )}
        <Button onClick={() => setFolderName("")} disabled={running}>
          New folder
        </Button>
        <a
          className="btn btn-ghost"
          href={`/api${base}/files/zip?path=${q(path)}`}
          aria-label={
            path === ROOT
              ? "Download the whole workspace as a zip"
              : "Download this folder as a zip"
          }
        >
          {path === ROOT ? "Download workspace (zip)" : "Download folder (zip)"}
        </a>
      </div>
      {running && (
        <p className="muted">The agent is working, so files cannot be changed right now.</p>
      )}
      {error && <Banner tone="danger">{error}</Banner>}
      <div className="files-grid">
        <table className="files-table">
          <caption className="sr-only">Files in {path}</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Size</th>
              <th scope="col">Changed</th>
              <th scope="col">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {path !== ROOT && (
              <tr>
                <td colSpan={4}>
                  <button className="link" onClick={() => void load(parent(path))}>
                    ↑ Up
                  </button>
                </td>
              </tr>
            )}
            {(list.entries ?? []).length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  This folder is empty.
                </td>
              </tr>
            )}
            {(list.entries ?? []).map((e) => {
              const p = join(path, e.name);
              return (
                <tr key={e.name} aria-current={open?.path === p || undefined}>
                  <td>
                    <button
                      className="link file-name"
                      onClick={() => (e.type === "dir" ? void load(p) : void openFile(p))}
                    >
                      {e.name}
                      {e.type === "dir" ? "/" : ""}
                    </button>
                  </td>
                  <td>{e.type === "dir" ? "" : fileSize(e.size)}</td>
                  <td>{e.modified ? ago(e.modified) : ""}</td>
                  <td className="files-row-actions">
                    {e.type === "file" && (
                      <a
                        className="btn btn-ghost"
                        href={`/api${base}/files/download?path=${q(p)}`}
                        aria-label={`Download ${e.name}`}
                      >
                        Download
                      </a>
                    )}
                    {e.type === "dir" && (
                      <a
                        className="btn btn-ghost"
                        href={`/api${base}/files/zip?path=${q(p)}`}
                        aria-label={`Download ${e.name} as a zip`}
                      >
                        Download zip
                      </a>
                    )}
                    <Button
                      variant="ghost"
                      onClick={() => setConfirm(e)}
                      disabled={running}
                      aria-label={`Delete ${e.name}`}
                    >
                      Delete
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {list.truncated && (
          <p className="muted">
            Only the first 2000 items are shown. Use the terminal for the rest.
          </p>
        )}
        {open && (
          <section className="file-view" aria-label={`File ${open.path}`}>
            <div className="row">
              <strong className="grow file-path">{open.path.slice(ROOT.length + 1)}</strong>
              <Button variant="ghost" onClick={() => setOpen(null)}>
                Close
              </Button>
            </div>
            {open.binary ? (
              <p className="muted">
                This file is not text ({fileSize(open.size)}).{" "}
                <a href={`/api${base}/files/download?path=${q(open.path)}`}>Download it</a> to open
                it.
              </p>
            ) : (
              <>
                {open.truncated && (
                  <Banner tone="warn">
                    This file is larger than 1 MB ({fileSize(open.size)}), so only the start is
                    shown and it cannot be saved from here.
                  </Banner>
                )}
                <TextArea
                  label="Contents"
                  className="textarea code-editor"
                  rows={18}
                  spellCheck={false}
                  value={text}
                  readOnly={!!open.truncated || running}
                  onChange={(e) => setText(e.target.value)}
                  onKeyDown={(e) => {
                    if ((e.metaKey || e.ctrlKey) && e.key === "s") {
                      e.preventDefault();
                      if (dirty && !open.truncated) void save();
                    }
                  }}
                />
                <div className="row">
                  <Button
                    variant="primary"
                    onClick={() => void save()}
                    loading={saving}
                    disabled={!dirty || !!open.truncated || running}
                  >
                    Save
                  </Button>
                  {dirty && <span className="muted">Unsaved changes</span>}
                </div>
              </>
            )}
          </section>
        )}
      </div>
      {confirm && (
        <Modal
          title={`Delete ${confirm.name}?`}
          onClose={() => setConfirm(null)}
          actions={
            <>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button variant="danger" onClick={() => void remove(confirm)}>
                Delete
              </Button>
            </>
          }
        >
          <p>
            {confirm.type === "dir"
              ? "The folder and everything in it are removed from your workspace."
              : "The file is removed from your workspace."}{" "}
            This cannot be undone.
          </p>
        </Modal>
      )}
      {folderName !== null && (
        <Modal
          title="New folder"
          onClose={() => setFolderName(null)}
          actions={
            <>
              <Button onClick={() => setFolderName(null)}>Cancel</Button>
              <Button variant="primary" onClick={() => void makeFolder()}>
                Create
              </Button>
            </>
          }
        >
          <Input
            label="Folder name"
            value={folderName}
            onChange={(e) => setFolderName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void makeFolder();
            }}
          />
        </Modal>
      )}
    </div>
  );
}
