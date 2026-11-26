// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState } from "react";
import { get } from "../lib/api";
import { Banner, Input, Select, Skeleton, Toggle } from "../ui/ui";
import { useConnections } from "./Connections";

export interface RepoChoice {
  kind: string;
  full_name?: string;
  clone_url?: string;
  default_branch?: string;
  create?: { name: string; description: string; private: boolean };
}
interface Repo {
  full_name: string;
  description: string;
  private: boolean;
  clone_url: string;
  default_branch: string;
}

/** Optional: start the session from one of the user's repositories, or from a new one. */
export function RepoPicker({
  onChange,
  allowCreate = true,
  legend = "Repository (optional)",
  noneLabel = "No repository: an empty workspace",
  autoSelect = false,
}: {
  onChange: (r: RepoChoice | null) => void;
  allowCreate?: boolean;
  legend?: string;
  noneLabel?: string;
  /** Choose the service at once when only one is connected, so its repositories are listed. */
  autoSelect?: boolean;
}) {
  const { data } = useConnections();
  const [kind, setKind] = useState("");
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [q, setQ] = useState("");
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [err, setErr] = useState("");
  const [picked, setPicked] = useState("");
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [priv, setPriv] = useState(true);
  const git = (data ?? []).filter((c) => c.kind.git && c.connected);

  useEffect(() => {
    if (autoSelect && !kind && git.length === 1) setKind(git[0]!.kind.id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoSelect, data]);

  useEffect(() => {
    if (!kind || mode !== "existing") return;
    setRepos(null);
    setErr("");
    const t = setTimeout(() => {
      get<Repo[]>(`/connections/${kind}/repos?q=${encodeURIComponent(q)}`)
        .then(setRepos)
        .catch((e) => setErr(e instanceof Error ? e.message : "Could not load repositories"));
    }, 250);
    return () => clearTimeout(t);
  }, [kind, mode, q]);

  useEffect(() => {
    if (!kind) return onChange(null);
    if (mode === "new") {
      onChange(name ? { kind, create: { name, description: desc, private: priv } } : null);
      return;
    }
    const r = repos?.find((x) => x.full_name === picked);
    onChange(
      r
        ? { kind, full_name: r.full_name, clone_url: r.clone_url, default_branch: r.default_branch }
        : null,
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, mode, picked, name, desc, priv, repos]);

  if (!data) return <Skeleton lines={1} />;
  if (git.length === 0)
    return (
      <Banner>
        No git service is connected, so there is no list of repositories to choose from. Connect
        GitHub, GitLab or Gitea in Settings, Connections to pick one here.
      </Banner>
    );
  return (
    <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
      <legend style={{ fontWeight: 600, fontSize: "var(--fs-sm)", marginBottom: 8 }}>
        {legend}
      </legend>
      <Select
        label="Service"
        value={kind}
        onChange={(e) => {
          setKind(e.target.value);
          setPicked("");
        }}
        options={[
          { value: "", label: noneLabel },
          ...git.map((c) => ({ value: c.kind.id, label: `${c.kind.title} (${c.account})` })),
        ]}
      />
      {kind && (
        <>
          <Select
            label="Repository"
            value={mode}
            onChange={(e) => setMode(e.target.value as "existing" | "new")}
            options={[
              { value: "existing", label: "Use one of mine" },
              ...(allowCreate ? [{ value: "new", label: "Create a new one" }] : []),
            ]}
          />
          {mode === "existing" ? (
            <>
              <Input
                label="Search"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="name or description"
              />
              {err && <Banner tone="warn">{err}</Banner>}
              {repos === null && !err && <Skeleton lines={2} />}
              <div role="listbox" aria-label="Repositories" className="repo-list">
                {(repos ?? []).map((r) => (
                  <button
                    key={r.full_name}
                    type="button"
                    role="option"
                    aria-selected={picked === r.full_name}
                    className="choice"
                    aria-pressed={picked === r.full_name}
                    onClick={() => setPicked(r.full_name)}
                  >
                    <strong>{r.full_name}</strong>
                    <small>
                      {r.private ? "private" : "public"}
                      {r.description ? ` · ${r.description}` : ""}
                    </small>
                  </button>
                ))}
                {repos && repos.length === 0 && <p className="muted">No repositories found.</p>}
              </div>
            </>
          ) : (
            <>
              <Input
                label="Name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="my-new-project"
              />
              <Input
                label="Description (optional)"
                value={desc}
                onChange={(e) => setDesc(e.target.value)}
              />
              <Toggle label="Private" checked={priv} onChange={setPriv} />
            </>
          )}
          <p className="muted">
            The agent works in a clone on its own branch and can open a pull request. Your other
            branches are not touched.
          </p>
        </>
      )}
    </fieldset>
  );
}
