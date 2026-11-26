// SPDX-License-Identifier: GPL-2.0-or-later
import { useState, type FormEvent } from "react";
import { del, errText, get, post } from "../lib/api";
import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  Input,
  Modal,
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
  /** Each server variable and the name of the credential every user supplies for it. */
  env_secrets: Record<string, string>;
  /** Variables that used a shared credential before each user had their own. */
  shared_before?: string[];
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

const credName = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** What an environment variable takes, in words: always the running user's own credential. */
export function describeMapping(v: string): string {
  return `each user's own ${v.replace(/^user:/, "")}`;
}

/**
 * MCP servers give the agent extra tools. This tab walks an admin through it in order: what is
 * approved now, how to add one (recommended or searched), and the credentials a server may need.
 */
export function McpTab() {
  const toast = useToast();
  const approved = useLoad<McpApproved[]>("/mcp/approved");
  const sugg = useLoad<Suggestions>("/mcp/suggestions");
  const requests =
    useLoad<{ id: string; user?: string; name: string; title?: string; reason?: string }[]>(
      "/mcp/requests",
    );
  const [how, setHow] = useState("recommended");
  const [q, setQ] = useState("");
  const [results, setResults] = useState<McpServerEntry[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [review, setReview] = useState<{ server: McpServerEntry; pkg: McpPkg } | null>(null);
  const [map, setMap] = useState<Record<string, string>>({});

  const close = () => {
    setReview(null);
    setMap({});
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
      setMap({ ...s.env_secrets });
      setReview({ server: hit, pkg });
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  async function reviewRequest(name: string) {
    setBusy(true);
    try {
      const r = await get<{ servers: McpServerEntry[] }>(
        `/mcp/search?q=${encodeURIComponent(name)}`,
      );
      const hit = r.servers.find((x) => x.name === name);
      const pkg = hit?.packages?.[0];
      if (!hit || !pkg) {
        toast("That server is not in the registry right now.", "error");
        return;
      }
      setMap({});
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
      const mapping: Record<string, string> = {};
      for (const ev of review.pkg.package.env ?? []) {
        const name = (map[ev.name] ?? (ev.required ? ev.name : "")).trim();
        if (!name) continue;
        if (!credName.test(name)) throw new Error(`"${name}" is not a valid credential name`);
        mapping[ev.name] = name;
      }
      // a variable mapped from a suggestion that the package does not list keeps its mapping
      for (const [env, name] of Object.entries(map))
        if (!(env in mapping) && name) mapping[env] = name;
      await post("/mcp/approve", {
        name: review.server.name,
        package: review.pkg.index,
        env_secrets: mapping,
      });
      toast(
        `${review.server.name} is approved. Users switch it on under Settings → Tools, with their own credentials.`,
        "success",
      );
      close();
      approved.reload();
      requests.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  const imageMissing = review?.pkg.image_present === false;
  const missingRequired = (review?.pkg.package.env ?? []).some(
    (ev) => ev.required && !(map[ev.name] ?? ev.name).trim(),
  );

  return (
    <div className="stack">
      <Card title="Give the agent more tools">
        <p>
          An MCP server adds tools the agent can call: read a GitHub repository&apos;s issues, query
          a database, search documentation. Each one is a third-party program, so nothing runs until
          you approve it. An approved server starts, in its own gVisor sandbox, only when a session
          uses it, and only with the credentials of the user who runs it. You never give a server a
          credential of your own. Anything it asks to change still asks the user first.
        </p>
        <ol>
          <li>
            <strong>Add a server</strong> below: a recommended one, or search the public registry.
          </li>
          <li>
            <strong>Review and approve</strong> it. You see what it can do and which credentials
            each user will need to supply.
          </li>
          <li>
            <strong>Users switch it on</strong> under Settings → Tools in the chat app, after adding
            their own credentials there or in Connections. Users can also ask you for a server from
            the same screen.
          </li>
        </ol>
        <p className="muted">
          <a href={`${DOCS}/security.md`} target="_blank" rel="noreferrer">
            How MCP servers are contained
          </a>
        </p>
      </Card>

      {requests.data && requests.data.length > 0 && (
        <Card title={`Requests from users (${requests.data.length})`}>
          <ul>
            {requests.data.map((r) => (
              <li key={r.id} className="row">
                <span className="grow">
                  <strong>{r.title || r.name}</strong>{" "}
                  <span className="muted">
                    {r.name}
                    {r.user ? ` · ${r.user}` : ""}
                    {r.reason ? ` · ${r.reason}` : ""}
                  </span>
                </span>
                <Button disabled={busy} onClick={() => void reviewRequest(r.name)}>
                  Review
                </Button>
                <Button
                  variant="danger"
                  onClick={async () => {
                    try {
                      await del(`/mcp/requests/${encodeURIComponent(r.id)}`);
                      requests.reload();
                    } catch (x) {
                      toast(errText(x), "error");
                    }
                  }}
                >
                  Dismiss
                </Button>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {(approved.data ?? []).some((a) => (a.shared_before ?? []).length > 0) && (
        <Banner tone="warn">
          <p>
            These servers used to take a credential shared by everyone. A shared credential is no
            longer sent to a server: each user now supplies their own, and the server stays off for
            a user until they do.
          </p>
          <ul>
            {(approved.data ?? [])
              .filter((a) => (a.shared_before ?? []).length > 0)
              .map((a) => (
                <li key={a.name}>
                  <strong>{a.name}</strong>: {(a.shared_before ?? []).join(", ")}
                </li>
              ))}
          </ul>
          <Button
            onClick={async () => {
              try {
                await del("/mcp/shared-notice");
                approved.reload();
              } catch (x) {
                toast(errText(x), "error");
              }
            }}
          >
            Dismiss
          </Button>
        </Banner>
      )}

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
              First-party servers that run as a local program, set up so each user brings their own
              account (Settings → Connections in the chat app). GitLab and Cloudflare publish only
              hosted servers (run by the publisher, not as a package), which Syntaxis does not run:
              it only runs servers inside its own sandboxes.
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
                  <>
                    <Badge tone="warn">hosted only: cannot run in a sandbox</Badge>
                    <p className="muted">
                      The publisher runs this server on its own infrastructure and publishes no
                      package to run here. Syntaxis only runs servers inside its own sandboxes, so
                      that nothing the agent sends goes to a third party's server. Use a different
                      server that publishes a package, or connect the service in Settings →
                      Connections.
                    </p>
                  </>
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
                This server is a container image. Syntaxis cannot download images: the Docker
                connection it is given can only create hardened sandboxes, so that a compromised
                Syntaxis cannot pull and run any image on your host.{" "}
                {imageMissing
                  ? "It is not on the Docker host yet. Run this on the host, then check again:"
                  : review.pkg.image_present
                    ? "It is on the Docker host. To fetch it again:"
                    : "Make sure it is on the Docker host:"}
              </p>
              <pre className="output" tabIndex={0} aria-label="Command to pull the image">
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
          {(review.pkg.package.env ?? []).length > 0 && (
            <>
              <h3>Credentials each user supplies</h3>
              <p className="muted">
                Every user gives the server their own value, from one of their connections or a
                secret they add in Settings → Tools. You only choose the name it has in their vault.
                Nothing here is shared, and you never see a value.
              </p>
            </>
          )}
          <datalist id="user-credential-names">
            {(sugg.data?.user_credentials ?? []).map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>
          {(review.pkg.package.env ?? []).map((ev) => (
            <div key={ev.name}>
              <Input
                label={`${ev.name}${ev.required ? " (required)" : ""}: name of the user's credential`}
                list="user-credential-names"
                value={map[ev.name] ?? (ev.required ? ev.name : "")}
                placeholder={ev.required ? ev.name : "Leave empty to leave it out"}
                onChange={(e) => setMap({ ...map, [ev.name]: e.target.value })}
              />
              {ev.description && <p className="muted">{ev.description}</p>}
            </div>
          ))}
        </Modal>
      )}
    </div>
  );
}
