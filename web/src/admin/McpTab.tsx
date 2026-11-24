// SPDX-License-Identifier: GPL-2.0-or-later
import { useState, type FormEvent } from "react";
import { del, errText, get, post, put } from "../lib/api";
import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  Input,
  Modal,
  Select,
  Skeleton,
  Tabs,
  useToast,
} from "../ui/ui";
import { DOCS, useLoad } from "./shared";

export interface McpPkg {
  index: number;
  /** For a container image: whether it is on the Docker host yet. */
  image_present?: boolean;
  package: {
    registryType: string;
    identifier: string;
    version: string;
    env: { name: string; required: boolean; secret: boolean; description: string }[];
  };
  review: {
    capabilities: string[];
    warnings: string[];
    needs_credentials: string[];
    pinnable: boolean;
    /** Set for a container image: Syntaxis cannot download it, so it has to be pulled on the host. */
    pull_command?: string;
  };
}
export interface McpServerEntry {
  name: string;
  title: string;
  description: string;
  repository: string;
  status: string;
  remote_only: boolean;
  packages: McpPkg[];
}
interface McpApproved {
  name: string;
  package: string;
  type: string;
  env_secrets: Record<string, string>;
  approved_by: string;
}
interface Suggestions {
  user_credentials: string[];
  suggestions: {
    title: string;
    description?: string;
    search: string;
    env_secrets: Record<string, string>;
  }[];
}

const NEW = "__new__";
const credName = /^[A-Z][A-Z0-9_]*$/;

/** What an environment variable is mapped to, in words. */
export function describeMapping(v: string): string {
  if (v.startsWith("user:")) return `each user's own ${v.slice(5)}`;
  return `shared credential ${v}`;
}

/**
 * MCP servers give the agent extra tools. This tab walks an admin through it in order: what is
 * approved now, how to add one (recommended or searched), and the credentials a server may need.
 */
export function McpTab() {
  const toast = useToast();
  const approved = useLoad<McpApproved[]>("/mcp/approved");
  const secrets = useLoad<string[]>("/secrets");
  const sugg = useLoad<Suggestions>("/mcp/suggestions");
  const [how, setHow] = useState("recommended");
  const [q, setQ] = useState("");
  const [results, setResults] = useState<McpServerEntry[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [review, setReview] = useState<{ server: McpServerEntry; pkg: McpPkg } | null>(null);
  const [map, setMap] = useState<Record<string, string>>({});
  const [fresh, setFresh] = useState<Record<string, { name: string; value: string }>>({});
  const [newName, setNewName] = useState("");
  const [newValue, setNewValue] = useState("");

  const close = () => {
    setReview(null);
    setMap({});
    setFresh({});
  };
  async function search(e?: FormEvent, term = q) {
    e?.preventDefault();
    setBusy(true);
    try {
      setResults(
        (await get<{ servers: McpServerEntry[] }>(`/mcp/search?q=${encodeURIComponent(term)}`))
          .servers,
      );
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  async function find(s: Suggestions["suggestions"][number]) {
    setBusy(true);
    try {
      const r = await get<{ servers: McpServerEntry[] }>(
        `/mcp/search?q=${encodeURIComponent(s.search)}`,
      );
      const hit = r.servers.find((x) => x.name === s.search);
      const pkg = hit?.packages?.[0];
      if (!hit || !pkg) {
        toast("That server is not in the registry right now.", "error");
        return;
      }
      setMap(s.env_secrets);
      setReview({ server: hit, pkg });
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  /** Asks again whether the image is on the Docker host, after the admin has pulled it. */
  async function recheckImage() {
    if (!review) return;
    try {
      const r = await get<{ servers: McpServerEntry[] }>(
        `/mcp/search?q=${encodeURIComponent(review.server.name)}`,
      );
      const pkg = r.servers.find((x) => x.name === review.server.name)?.packages?.[
        review.pkg.index
      ];
      if (pkg) setReview({ server: review.server, pkg });
      toast(
        pkg?.image_present
          ? "The image is on the Docker host."
          : "The image is not on the Docker host yet.",
        pkg?.image_present ? "success" : "info",
      );
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  async function approve() {
    if (!review) return;
    try {
      const mapping = { ...map };
      // a credential made here is saved first, then mapped
      for (const [env, f] of Object.entries(fresh)) {
        if (mapping[env] !== NEW) continue;
        if (!credName.test(f.name) || f.name.startsWith("SYNTAXIS_"))
          throw new Error(`"${f.name}" is not a valid credential name`);
        await put(`/secrets/${f.name}`, { value: f.value });
        mapping[env] = f.name;
      }
      for (const k of Object.keys(mapping))
        if (mapping[k] === NEW || mapping[k] === "") delete mapping[k];
      await post("/mcp/approve", {
        name: review.server.name,
        package: review.pkg.index,
        env_secrets: mapping,
      });
      toast(
        `${review.server.name} is approved. Users can now switch it on under Settings → Tools.`,
        "success",
      );
      close();
      approved.reload();
      secrets.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  const imageMissing = review?.pkg.image_present === false;
  const missingRequired = (review?.pkg.package.env ?? []).some(
    (ev) =>
      ev.required &&
      (!map[ev.name] || (map[ev.name] === NEW && !(fresh[ev.name]?.name && fresh[ev.name]?.value))),
  );

  return (
    <div className="stack">
      <Card title="Give the agent more tools">
        <p>
          An MCP server adds tools the agent can call: read a GitHub repository&apos;s issues, query
          a database, search documentation. Each one is a third-party program, so nothing runs until
          you approve it, and an approved server only starts, in its own gVisor sandbox with just
          the credentials you give it, when a session uses it. Anything it asks to change still asks
          the user first.
        </p>
        <ol>
          <li>
            <strong>Add a server</strong> below: a recommended one, or search the public registry.
          </li>
          <li>
            <strong>Review and approve</strong> it. You see what it can do and choose which
            credentials it gets.
          </li>
          <li>
            <strong>Users switch it on</strong> under Settings → Tools in the chat app. Admins can
            also do it for one user from Users → Settings.
          </li>
        </ol>
        <p className="muted">
          <a href={`${DOCS}/security.md`} target="_blank" rel="noreferrer">
            How MCP servers are contained
          </a>
        </p>
      </Card>

      <Card title={`Approved servers${approved.data ? ` (${approved.data.length})` : ""}`}>
        {!approved.data ? (
          <Skeleton lines={2} />
        ) : approved.data.length === 0 ? (
          <EmptyState
            title="Nothing is approved yet"
            hint="Start with Add a server below. Users see only the servers you approve."
          />
        ) : (
          <ul>
            {approved.data.map((a) => (
              <li key={a.name} className="row">
                <span className="grow">
                  <strong>{a.name}</strong> <span className="muted">{a.package}</span>
                  {Object.keys(a.env_secrets).length > 0 && (
                    <span className="muted">
                      {" "}
                      · credentials:{" "}
                      {[...new Set(Object.values(a.env_secrets).map(describeMapping))].join(", ")}
                    </span>
                  )}
                </span>
                <Button
                  variant="danger"
                  onClick={async () => {
                    try {
                      await del(`/mcp/approved?name=${encodeURIComponent(a.name)}`);
                      approved.reload();
                    } catch (x) {
                      toast(errText(x), "error");
                    }
                  }}
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title="Add a server">
        <Tabs
          value={how}
          onChange={setHow}
          tabs={[
            { id: "recommended", label: "Recommended" },
            { id: "search", label: "Search the registry" },
          ]}
        />
        {how === "recommended" ? (
          <>
            <p className="muted">
              First-party servers that run as a local program, set up so each user connects with
              their own account (Settings → Connections in the chat app). GitLab and Cloudflare
              publish only hosted servers, which Syntaxis does not run.
            </p>
            {!sugg.data ? (
              <Skeleton lines={2} />
            ) : (
              sugg.data.suggestions.map((s) => (
                <div key={s.search} className="row">
                  <span className="grow">
                    <strong>{s.title}</strong>
                    {s.description && (
                      <>
                        <br />
                        {s.description}
                      </>
                    )}
                    <br />
                    <span className="muted">{s.search}</span>
                  </span>
                  <Button loading={busy} onClick={() => void find(s)}>
                    Review and approve
                  </Button>
                </div>
              ))
            )}
          </>
        ) : (
          <>
            <p className="muted">
              The public MCP registry lists community servers. Only ones that run as a program can
              be used here; hosted ones are marked. You review each before approving.
            </p>
            <form className="row" onSubmit={(e) => void search(e)}>
              <div className="grow">
                <Input
                  label="Search"
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  placeholder="for example: github, filesystem, postgres"
                />
              </div>
              <Button variant="primary" type="submit" loading={busy}>
                Search
              </Button>
            </form>
            {results && results.length === 0 && (
              <p className="muted">No servers found. Try a shorter word.</p>
            )}
            {results?.map((r) => (
              <div key={r.name + r.title} className="step" style={{ padding: 12 }}>
                <strong>{r.title || r.name}</strong> <span className="muted">{r.name}</span>{" "}
                {r.status && r.status !== "active" && <Badge tone="warn">{r.status}</Badge>}
                <p>{r.description}</p>
                {r.remote_only ? (
                  <Badge tone="warn">hosted only: cannot run in a sandbox</Badge>
                ) : (
                  (r.packages ?? []).map((p) => (
                    <Button key={p.index} onClick={() => setReview({ server: r, pkg: p })}>
                      Review {p.package.registryType}: {p.package.identifier}@{p.package.version}
                    </Button>
                  ))
                )}
              </div>
            ))}
          </>
        )}
      </Card>

      <Card title="Shared credentials">
        <p className="muted">
          Only needed when a server asks for a key and you do not want each user to bring their own.
          A shared credential is used by everyone who switches that server on. Values are kept
          encrypted and never shown again. You can also create one while approving a server.
        </p>
        {(secrets.data ?? []).length === 0 && <p className="muted">None yet.</p>}
        <ul>
          {(secrets.data ?? []).map((n) => (
            <li key={n} className="row">
              <code className="grow">{n}</code>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await del(`/secrets/${n}`);
                    secrets.reload();
                  } catch (x) {
                    toast(errText(x), "error");
                  }
                }}
              >
                Delete
              </Button>
            </li>
          ))}
        </ul>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            try {
              await put(`/secrets/${newName}`, { value: newValue });
              setNewName("");
              setNewValue("");
              secrets.reload();
            } catch (x) {
              toast(errText(x), "error");
            }
          }}
        >
          <Input
            label="Name"
            placeholder="DOCS_API_KEY"
            hint="Capital letters, digits and underscores, for example DOCS_API_KEY."
            value={newName}
            onChange={(e) => setNewName(e.target.value.toUpperCase())}
            required
          />
          <Input
            label="Value"
            type="password"
            autoComplete="off"
            placeholder="the key or token itself"
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            required
          />
          <Button type="submit">Save credential</Button>
        </form>
      </Card>

      {review && (
        <Modal
          title={`Approve ${review.server.name}?`}
          onClose={close}
          actions={
            <>
              <Button onClick={close}>Cancel</Button>
              <Button
                variant="primary"
                onClick={() => void approve()}
                disabled={!review.pkg.review.pinnable || missingRequired || imageMissing}
              >
                Approve
              </Button>
            </>
          }
        >
          <p className="muted">
            {review.pkg.package.registryType} package {review.pkg.package.identifier}@
            {review.pkg.package.version}. {review.server.repository}
          </p>
          <p>When approved, this server can:</p>
          <ul>
            {(review.pkg.review.capabilities ?? []).map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
          {(review.pkg.review.warnings ?? []).map((w) => (
            <Banner key={w} tone="warn">
              {w}
            </Banner>
          ))}
          {review.pkg.review.pull_command && (
            <Banner tone={imageMissing ? "warn" : "info"}>
              <p>
                This server is a container image, and Syntaxis cannot download images.{" "}
                {imageMissing
                  ? "It is not on the Docker host yet. Run this on the host, then check again:"
                  : review.pkg.image_present
                    ? "It is on the Docker host. To fetch it again:"
                    : "Make sure it is on the Docker host:"}
              </p>
              <pre className="output" aria-label="Command to pull the image">
                {review.pkg.review.pull_command}
              </pre>
              <Button onClick={() => void recheckImage()}>Check again</Button>
            </Banner>
          )}
          {!review.pkg.review.pinnable && (
            <Banner tone="danger">
              This package has no exact version, so what would run could change without you knowing.
              It cannot be approved.
            </Banner>
          )}
          {(review.pkg.package.env ?? []).length > 0 && <h3>Credentials it takes</h3>}
          {(review.pkg.package.env ?? []).map((ev) => (
            <div key={ev.name}>
              <Select
                label={`${ev.name}${ev.required ? " (required)" : ""}`}
                value={map[ev.name] ?? ""}
                onChange={(e) => {
                  setMap({ ...map, [ev.name]: e.target.value });
                  if (e.target.value === NEW && !fresh[ev.name])
                    setFresh({ ...fresh, [ev.name]: { name: ev.name, value: "" } });
                }}
                options={[
                  {
                    value: "",
                    label: ev.required ? "Choose where it comes from…" : "Leave it out",
                  },
                  ...(sugg.data?.user_credentials ?? []).map((n) => ({
                    value: "user:" + n,
                    label: `Each user's own ${n} (recommended)`,
                  })),
                  ...(secrets.data ?? []).map((n) => ({ value: n, label: `Shared: ${n}` })),
                  { value: NEW, label: "A new shared credential…" },
                ]}
              />
              {ev.description && <p className="muted">{ev.description}</p>}
              {map[ev.name] === NEW && (
                <div className="row">
                  <Input
                    label="Credential name"
                    value={fresh[ev.name]?.name ?? ""}
                    placeholder="DOCS_API_KEY"
                    onChange={(e) =>
                      setFresh({
                        ...fresh,
                        [ev.name]: {
                          name: e.target.value.toUpperCase(),
                          value: fresh[ev.name]?.value ?? "",
                        },
                      })
                    }
                  />
                  <Input
                    label="Value"
                    type="password"
                    autoComplete="off"
                    value={fresh[ev.name]?.value ?? ""}
                    onChange={(e) =>
                      setFresh({
                        ...fresh,
                        [ev.name]: { name: fresh[ev.name]?.name ?? ev.name, value: e.target.value },
                      })
                    }
                  />
                </div>
              )}
            </div>
          ))}
        </Modal>
      )}
    </div>
  );
}
