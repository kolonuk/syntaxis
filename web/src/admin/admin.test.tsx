// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import { ToastProvider } from "../ui/ui";
import { AdminsTab, adminState, type AdminAccount } from "./AdminsTab";
import { AuditTab } from "./AuditTab";
import { ConfigTab, type ConfigItem } from "./ConfigTab";
import { describeMapping, McpTab } from "./McpTab";
import { UpdatesTab, type UpdateInfo } from "./UpdatesTab";
import { navGroups, UsersTab } from "./App";
import { BenchmarksTab, modelFeatures, ProvidersTab } from "./ProvidersTab";
import { UserSettingsDialog } from "./UserSettings";

afterEach(cleanup);

type Route = unknown | ((body: unknown) => unknown);
interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** Answers the admin API from a table of "METHOD /path" to a reply, and records every call. */
function api(routes: Record<string, Route>) {
  const calls: Call[] = [];
  _setFetch((async (url: string, init: RequestInit) => {
    const method = init.method ?? "GET";
    const path = url.replace(/^\/api/, "");
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, path, body });
    const hit = routes[`${method} ${path}`] ?? routes[`${method} ${path.split("?")[0]}`];
    if (hit === undefined)
      return new Response(JSON.stringify({ error: "no such route in the test" }), { status: 404 });
    const out = typeof hit === "function" ? (hit as (b: unknown) => unknown)(body) : hit;
    return new Response(JSON.stringify(out), { status: 200 });
  }) as typeof fetch);
  return calls;
}

const wrap = (ui: React.ReactNode) => render(<ToastProvider>{ui}</ToastProvider>);

describe("Audit tab", () => {
  const base = {
    entries: [
      {
        seq: 2,
        time: "2026-01-01T00:00:00Z",
        actor: "admin:1",
        action: "setting.change",
        target: "smtp_host",
        detail: "x",
      },
    ],
    chain_ok: true,
    first_bad: -1,
    head: { count: 2, seq: 2, hash: "ab".repeat(32) },
    total: 2,
    genesis: "0".repeat(64),
  };
  it("says what verified means and gives the proof to check", async () => {
    api({ "GET /audit": base });
    wrap(<AuditTab />);
    expect(await screen.findByText(/Verified\./)).toBeInTheDocument();
    expect(screen.getByText(/all\s+2 entries/)).toBeInTheDocument();
    expect(screen.getByText(/cannot be edited quietly/)).toBeInTheDocument();
    expect(screen.getByLabelText("Fingerprint of the latest entry")).toHaveTextContent(
      "ab".repeat(32),
    );
    expect(screen.getByRole("link", { name: "Download the log" })).toHaveAttribute(
      "href",
      "/api/audit/export",
    );
    expect(screen.getByLabelText("Command to verify the downloaded log")).toHaveTextContent(
      "syntaxis audit verify",
    );
    expect(screen.getByRole("link", { name: "How the audit log works" })).toHaveAttribute(
      "href",
      expect.stringContaining("security.md#audit-log"),
    );
  });
  it("says plainly when the chain is broken and where", async () => {
    api({ "GET /audit": { ...base, chain_ok: false, first_bad: 4 } });
    wrap(<AuditTab />);
    expect(await screen.findByText(/The audit log has been changed/)).toBeInTheDocument();
    expect(screen.getByText(/Entry 5 does not match/)).toBeInTheDocument();
  });
});

describe("Admins tab", () => {
  const acct = (o: Partial<AdminAccount>): AdminAccount => ({
    id: 1,
    email: "a@example.com",
    name: "A",
    totp_enabled: true,
    disabled: false,
    pending: false,
    you: false,
    created_at: "2026-01-01T00:00:00Z",
    ...o,
  });
  it("names each state in words", () => {
    expect(adminState(acct({})).label).toBe("2FA on");
    expect(adminState(acct({ totp_enabled: false })).label).toBe("no 2FA");
    expect(adminState(acct({ pending: true, totp_enabled: false })).label).toMatch(/invited/);
    expect(adminState(acct({ disabled: true })).label).toBe("disabled");
  });
  it("lists accounts, protects your own, and hands over a single-use link", async () => {
    const calls = api({
      "GET /admins": [
        acct({ id: 1, name: "Me", email: "me@example.com", you: true }),
        acct({ id: 2, name: "Second", email: "two@example.com", totp_enabled: false }),
      ],
      "POST /admins": { id: 3, setup_path: "/#setup=TOKEN123", expires_hours: 24 },
      "POST /admins/2/reset": { id: 2, setup_path: "/#setup=RESET456", expires_hours: 24 },
    });
    wrap(<AdminsTab />);
    expect(await screen.findByText("me@example.com")).toBeInTheDocument();
    expect(screen.getByText(/1 admin account has a password but no 2FA/)).toBeInTheDocument();
    const rows = screen.getAllByRole("row");
    const mine = rows.find((r) => within(r).queryByText("me@example.com"))!;
    expect(within(mine).queryByRole("button")).toBeNull(); // you cannot act on yourself
    // add an admin
    fireEvent.click(screen.getByRole("button", { name: "Add an admin" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New Person" } });
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "new@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Create setup link" }));
    expect(await screen.findByLabelText("Setup link")).toHaveTextContent(
      `${window.location.origin}/#setup=TOKEN123`,
    );
    expect(calls.find((c) => c.method === "POST" && c.path === "/admins")?.body).toEqual({
      name: "New Person",
      email: "new@example.com",
    });
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    // reset another admin needs a confirmation first
    fireEvent.click(screen.getByRole("button", { name: "Reset (new link)" }));
    expect(calls.some((c) => c.path === "/admins/2/reset")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Reset and make a link" }));
    expect(await screen.findByLabelText("Setup link")).toHaveTextContent("/#setup=RESET456");
  });
  it("can be cancelled out of", async () => {
    api({ "GET /admins": [acct({ id: 2, name: "Second" })] });
    wrap(<AdminsTab />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("Configuration tab", () => {
  const item = (o: Partial<ConfigItem>): ConfigItem => ({
    key: "k",
    group: "email",
    label: "Label",
    kind: "text",
    live: true,
    locked: false,
    value: "",
    source: "default",
    env_set: false,
    has_value: false,
    ...o,
  });
  const data = (restart: string[] = []) => ({
    restart_pending: restart,
    groups: [
      {
        id: "email",
        label: "Email",
        help: "Where mail goes.",
        items: [
          item({
            key: "smtp_host",
            label: "Mail server",
            value: "mail.docker.example",
            source: "docker",
            env: "SYNTAXIS_SMTP_HOST",
            env_set: true,
            has_value: true,
          }),
          item({
            key: "smtp_from",
            label: "From address",
            value: "me@example.com",
            source: "database",
            env: "SYNTAXIS_SMTP_FROM",
            env_set: true,
            env_value: "syntaxis@localhost",
            has_value: true,
          }),
          item({
            key: "smtp_password",
            label: "Password",
            kind: "secret",
            source: "database",
            has_value: true,
          }),
        ],
      },
      {
        id: "sandboxes",
        label: "Sessions and sandboxes",
        help: "",
        items: [
          item({
            key: "idle_minutes",
            group: "sandboxes",
            label: "Stop idle sandboxes after (minutes)",
            kind: "number",
            value: "30",
            live: false,
          }),
        ],
      },
      {
        id: "deployment",
        label: "Set by your deployment",
        help: "",
        items: [
          item({
            key: "listen",
            group: "deployment",
            label: "Chat app listen address",
            value: "0.0.0.0:8080",
            locked: true,
            env: "SYNTAXIS_LISTEN",
            source: "docker",
          }),
          item({
            key: "master_key",
            group: "deployment",
            label: "Master key",
            kind: "secret",
            locked: true,
            env: "SYNTAXIS_MASTER_KEY",
            has_value: true,
            source: "docker",
          }),
        ],
      },
    ],
  });
  it("shows where every value comes from and what needs a restart", async () => {
    api({ "GET /config": data(["idle_minutes"]) });
    wrap(<ConfigTab />);
    expect(await screen.findByText(/take effect after Syntaxis restarts/)).toHaveTextContent(
      "idle_minutes",
    );
    expect(screen.getByText("from Docker (SYNTAXIS_SMTP_HOST)")).toBeInTheDocument();
    expect(screen.getAllByText("saved here").length).toBe(2);
    expect(screen.getByText("needs a restart")).toBeInTheDocument();
    // the deployment group is read only and never shows the key
    expect(screen.getByLabelText("Chat app listen address value")).toHaveTextContent(
      "0.0.0.0:8080",
    );
    expect(screen.getByLabelText("Master key value")).toHaveTextContent("set");
    expect(screen.queryByLabelText("Master key")).toBeNull();
  });
  it("saves only what was changed, and never shows the saved password", async () => {
    const calls = api({ "GET /config": data(), "PUT /config": { ok: true, restart_pending: [] } });
    wrap(<ConfigTab />);
    const host = await screen.findByLabelText("Mail server");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("Password")).toHaveAttribute(
      "placeholder",
      expect.stringContaining("Set."),
    );
    fireEvent.change(host, { target: { value: "mail.new.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Save email" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ smtp_host: "mail.new.example" });
  });
  it("sends a new password when one is typed", async () => {
    const calls = api({ "GET /config": data(), "PUT /config": { ok: true, restart_pending: [] } });
    wrap(<ConfigTab />);
    fireEvent.change(await screen.findByLabelText("Password"), { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save email" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ smtp_password: "s3cret" });
  });
  it("puts the Docker value back, and tests the mail server", async () => {
    const calls = api({
      "GET /config": data(),
      "DELETE /config/smtp_from": { ok: true, restart_pending: [] },
      "POST /mail/test": { ok: true, to: "me@example.com", sent: true },
    });
    wrap(<ConfigTab />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Use the Docker value again (syntaxis@localhost)",
      }),
    );
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Send test email" }));
    await waitFor(() => expect(calls.some((c) => c.path === "/mail/test")).toBe(true));
  });
  it("does not save when nothing changed", async () => {
    api({ "GET /config": data() });
    wrap(<ConfigTab />);
    expect(await screen.findByRole("button", { name: "Save email" })).toBeDisabled();
  });
  it("has a tab for each group, shows one at a time and keeps what was typed in the others", async () => {
    api({ "GET /config": data() });
    wrap(<ConfigTab />);
    expect(
      await screen.findByRole("tablist", { name: "Configuration groups" }),
    ).toBeInTheDocument();
    const mail = await screen.findByLabelText("From address");
    fireEvent.change(mail, { target: { value: "other@example.com" } });
    fireEvent.click(screen.getByRole("tab", { name: "Sessions and sandboxes" }));
    expect(screen.getByLabelText("Stop idle sandboxes after (minutes)")).toBeVisible();
    expect(screen.getByLabelText("From address")).not.toBeVisible(); // the Email group is hidden now
    fireEvent.click(screen.getByRole("tab", { name: "Email" }));
    expect(screen.getByLabelText("From address")).toHaveValue("other@example.com");
  });
});

describe("Updates tab", () => {
  const rel = (tag: string, pre = false) => ({
    tag,
    name: tag,
    published: "2026-01-01T00:00:00Z",
    url: `https://example.com/${tag}`,
    prerelease: pre,
  });
  const info = (o: Partial<UpdateInfo> = {}): UpdateInfo => ({
    current: { version: "v0.2.0", commit: "abc", date: "" },
    mode: "release",
    stream: "stable",
    streams: [
      { id: "stable", current: true, latest: rel("v0.2.1"), newer: true, running: false },
      {
        id: "preview",
        current: false,
        latest: rel("v0.3.0-rc.1", true),
        newer: true,
        running: false,
      },
      {
        id: "edge",
        current: false,
        edge: { sha: "deadbeefcafe", date: "", subject: "fix a thing", newer: true },
      },
    ],
    releases: [rel("v0.3.0-rc.1", true), rel("v0.2.1"), rel("v0.2.0")],
    agent: { running: true, manual: "sudo sh scripts/update.sh" },
    status: {},
    ...o,
  });
  it("lists the three streams, marks the one followed and updates within it", async () => {
    const calls = api({
      "GET /update": info(),
      "POST /update": { target: "v0.2.1", stream: "stable" },
    });
    wrap(<UpdatesTab />);
    expect(await screen.findByRole("heading", { name: /Stable/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Preview/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Edge/ })).toBeInTheDocument();
    expect(screen.getAllByText("following").length).toBe(1);
    expect(screen.getByText("following Stable")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update to v0.2.1" }));
    fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ target: "stable" });
  });
  it("switches stream by name and warns when that would go backwards", async () => {
    const calls = api({
      "GET /update": info({
        current: { version: "v0.3.0-rc.1", commit: "", date: "" },
        stream: "preview",
        streams: [
          { id: "stable", current: false, latest: rel("v0.2.1"), newer: false, running: false },
          {
            id: "preview",
            current: true,
            latest: rel("v0.3.0-rc.1", true),
            newer: false,
            running: true,
          },
          { id: "edge", current: false },
        ],
      }),
      "POST /update": { target: "v0.2.1", stream: "stable" },
    });
    wrap(<UpdatesTab />);
    fireEvent.click(await screen.findByRole("button", { name: "Switch to Stable (v0.2.1)" }));
    expect(screen.getByText(/older than v0.3.0-rc.1/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ target: "stable" });
  });
  it("pins an exact version, and can be cancelled", async () => {
    const calls = api({ "GET /update": info(), "POST /update": {} });
    wrap(<UpdatesTab />);
    fireEvent.change(await screen.findByLabelText("Version"), { target: { value: "v0.3.0-rc.1" } });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
  it("says so when the update service never reported back, instead of showing a result it does not know", async () => {
    api({
      "GET /update": info({
        status: {
          state: "unknown",
          target: "v0.2.1",
          message: "The update service finished without telling Syntaxis how it went.",
        },
      }),
    });
    wrap(<UpdatesTab />);
    expect(await screen.findByText(/may or may not have happened/)).toHaveTextContent("v0.2.1");
    expect(screen.getByText(/finished without telling Syntaxis/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update to v0.2.1" })).toBeEnabled(); // not stuck as "running"
  });
  it("explains what to do when there is no update service, and offers nothing it cannot do", async () => {
    api({
      "GET /update": info({ agent: { running: false, manual: "sudo sh scripts/update.sh" } }),
    });
    wrap(<UpdatesTab />);
    expect(await screen.findByText(/deployed by another tool/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update to v0.2.1" })).toBeDisabled();
    expect(screen.queryByText(/Updates made here are temporary/)).toBeNull(); // not a container
  });
  it("warns at the top that an update is temporary when Syntaxis is in a container with no update service", async () => {
    api({
      "GET /update": info({
        agent: { running: false, manual: "sudo sh scripts/update.sh" },
        container: true,
      }),
    });
    wrap(<UpdatesTab />);
    expect(await screen.findByText(/Updates made here are temporary/)).toBeInTheDocument();
    expect(screen.getByText(/Compose file or Kubernetes manifest/)).toBeInTheDocument();
  });
  it("does not call updates temporary when the update service is running", async () => {
    api({ "GET /update": info({ container: true }) });
    wrap(<UpdatesTab />);
    await screen.findByText(/Running now/);
    expect(screen.queryByText(/Updates made here are temporary/)).toBeNull();
  });
});

describe("MCP servers tab", () => {
  const entry = {
    name: "io.github.example/docs",
    title: "Docs",
    description: "Search docs",
    repository: "https://example.com/r",
    status: "active",
    remote_only: false,
    packages: [
      {
        index: 0,
        package: {
          registryType: "npm",
          identifier: "docs-mcp",
          version: "1.2.3",
          env: [{ name: "DOCS_TOKEN", required: true, secret: true, description: "API token" }],
        },
        review: {
          capabilities: ["runs npm package docs-mcp@1.2.3"],
          warnings: [],
          needs_credentials: ["DOCS_TOKEN"],
          pinnable: true,
        },
      },
    ],
  };
  const routes = (approved: unknown[] = []) => ({
    "GET /mcp/approved": approved,
    "GET /secrets": ["SHARED_KEY"],
    "GET /mcp/suggestions": {
      user_credentials: ["GITHUB_TOKEN"],
      suggestions: [
        { title: "GitHub (official)", search: "io.github.example/docs", env_secrets: {} },
      ],
    },
    "GET /mcp/search": { servers: [entry] },
    "POST /mcp/approve": { ok: true },
    "PUT /secrets/DOCS_KEY": { ok: true },
  });
  it("explains the three steps and starts with what is approved", async () => {
    api(routes());
    wrap(<McpTab />);
    expect(await screen.findByText("Nothing is approved yet")).toBeInTheDocument();
    expect(screen.getByText(/Add a server/, { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText(/Users switch it on/)).toBeInTheDocument();
    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings.indexOf("Give the agent more tools")).toBeLessThan(
      headings.findIndex((h) => h?.startsWith("Approved servers")),
    );
    expect(headings.findIndex((h) => h?.startsWith("Approved servers"))).toBeLessThan(
      headings.indexOf("Add a server"),
    );
    expect(headings.indexOf("Add a server")).toBeLessThan(headings.indexOf("Shared credentials"));
  });
  it("lists what users requested and opens the review for it", async () => {
    api({
      ...routes(),
      "GET /mcp/requests": [
        {
          id: "r1",
          user: "user@example.com",
          name: "io.github.example/docs",
          title: "Docs",
          reason: "for the wiki",
        },
      ],
    });
    wrap(<McpTab />);
    expect(await screen.findByText(/Requests from users \(1\)/)).toBeInTheDocument();
    expect(screen.getByText(/user@example.com/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    expect(await screen.findByText(/runs npm package docs-mcp@1.2.3/)).toBeInTheDocument();
  });
  it("describes what an approved server was given", async () => {
    api(
      routes([
        {
          name: "gh",
          package: "x@1",
          type: "npm",
          env_secrets: { T: "user:GITHUB_TOKEN" },
          approved_by: "admin:1",
        },
      ]),
    );
    wrap(<McpTab />);
    expect(
      await screen.findByText(/credentials: each user's own GITHUB_TOKEN/),
    ).toBeInTheDocument();
    expect(describeMapping("SHARED")).toBe("shared credential SHARED");
  });
  it("searches, reviews, makes a credential on the spot and approves", async () => {
    const calls = api(routes());
    wrap(<McpTab />);
    fireEvent.click(await screen.findByRole("tab", { name: "Search the registry" }));
    fireEvent.change(screen.getByLabelText("Search"), { target: { value: "docs" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: /Review npm: docs-mcp@1.2.3/ }));
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeDisabled(); // a required credential is not chosen yet
    expect(screen.getByText("API token")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/DOCS_TOKEN/), { target: { value: "__new__" } });
    const dialog = within(screen.getByRole("dialog"));
    fireEvent.change(dialog.getByLabelText("Credential name"), { target: { value: "docs_key" } });
    fireEvent.change(dialog.getByLabelText("Value"), { target: { value: "the-secret" } });
    expect(approve).toBeEnabled();
    fireEvent.click(approve);
    await waitFor(() => expect(calls.some((c) => c.path === "/mcp/approve")).toBe(true));
    // the credential is saved first, then mapped by name; the value is not sent with the approval
    const order = calls.filter((c) => c.method !== "GET").map((c) => c.method + " " + c.path);
    expect(order).toEqual(["PUT /secrets/DOCS_KEY", "POST /mcp/approve"]);
    expect(calls.find((c) => c.path === "/mcp/approve")?.body).toEqual({
      name: "io.github.example/docs",
      package: 0,
      env_secrets: { DOCS_TOKEN: "DOCS_KEY" },
    });
  });
  it("tells the admin to pull a container image first, and approves once it is there", async () => {
    let present = false;
    const oci = {
      ...entry,
      packages: [
        {
          index: 0,
          image_present: false,
          package: {
            registryType: "oci",
            identifier: "ghcr.io/example/app:1.2.3",
            version: "",
            env: [],
          },
          review: {
            capabilities: ["runs as a container image"],
            warnings: [],
            needs_credentials: [],
            pinnable: true,
            pull_command: "docker pull ghcr.io/example/app:1.2.3",
          },
        },
      ],
    };
    const calls = api({
      ...routes(),
      "GET /mcp/search": () => ({
        servers: [{ ...oci, packages: [{ ...oci.packages[0], image_present: present }] }],
      }),
    });
    wrap(<McpTab />);
    fireEvent.click(await screen.findByRole("tab", { name: "Search the registry" }));
    fireEvent.change(screen.getByLabelText("Search"), { target: { value: "docs" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: /Review oci/ }));
    expect(screen.getByLabelText("Command to pull the image")).toHaveTextContent(
      "docker pull ghcr.io/example/app:1.2.3",
    );
    expect(screen.getByText(/not on the Docker host yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    present = true;
    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(calls.some((c) => c.path === "/mcp/approve")).toBe(true));
  });
  it("offers each user's own credential first, and can be cancelled", async () => {
    const calls = api(routes());
    wrap(<McpTab />);
    fireEvent.click(await screen.findByRole("button", { name: "Review and approve" }));
    const select = await screen.findByLabelText(/DOCS_TOKEN/);
    expect(
      within(select).getByText("Each user's own GITHUB_TOKEN (recommended)"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(calls.some((c) => c.path === "/mcp/approve")).toBe(false);
  });
});

describe("Admin menu", () => {
  it("groups related screens and reaches every screen once", () => {
    const groups = navGroups(false);
    const ids = groups.flatMap((g) => g.tabs.map((t) => t.id));
    expect(new Set(ids).size).toBe(ids.length);
    for (const want of [
      "health",
      "updates",
      "users",
      "groups",
      "admins",
      "providers",
      "bench",
      "envs",
      "mcp",
      "sessions",
      "egress",
      "settings",
      "config",
      "audit",
      "kill",
    ])
      expect(ids).toContain(want);
    const of = (id: string) => groups.find((g) => g.tabs.some((t) => t.id === id))!.id;
    expect(of("users")).toBe(of("groups"));
    expect(of("providers")).toBe(of("envs"));
    expect(of("providers")).toBe(of("mcp"));
    expect(of("health")).toBe(of("updates"));
    expect(of("sessions")).toBe(of("egress"));
  });
  it("marks the health and updates group when a release is available", () => {
    expect(navGroups(true)[0]!.label).toContain("●");
    expect(navGroups(false)[0]!.label).not.toContain("●");
  });
});

describe("Providers and models", () => {
  const prov = {
    id: 1,
    name: "local",
    kind: "ollama",
    base_url: "http://llm.internal",
    concurrency: 1,
    enabled: true,
    has_key: false,
    stream_probe: {},
    helper_model: "",
    helper_assert: false,
  };
  const model = (o: object) => ({
    id: 5,
    provider_id: 1,
    name: "tiny-model",
    enabled: true,
    tools: 1,
    recommended: false,
    context_override: 0,
    tool_probe: {},
    ...o,
  });
  it("shows what a model can do as labels beside its name, with no separate probe column", async () => {
    api({
      "GET /providers": [prov],
      "GET /models": [model({ tool_probe: { ok: false, detail: "no tool call came back" } })],
      "GET /benchmarks": [],
    });
    wrap(<ProvidersTab />);
    expect((await screen.findAllByText("tiny-model")).length).toBeGreaterThan(0);
    expect(screen.getByText("tools")).toBeInTheDocument();
    expect(screen.getByText("does not call tools")).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Live probe" })).toBeNull();
  });
  it("labels a model that cannot use tools and a provider whose stream is buffered", () => {
    const f = modelFeatures(model({ tools: 0 }) as never, {
      stream_probe: { ok: false, detail: "buffered" },
    });
    expect(f.map((x) => x.text)).toEqual(["no tools", "not probed", "stream is buffered"]);
  });
  it("keeps only the switch in the Recommended column", async () => {
    api({ "GET /providers": [prov], "GET /models": [model({})], "GET /benchmarks": [] });
    wrap(<ProvidersTab />);
    const sw = await screen.findByRole("switch", { name: "Recommend tiny-model for agents" });
    expect(sw.parentElement!.querySelector(".sr-only")).not.toBeNull(); // the words are for screen readers only
  });
  it("links to where each hosted provider's API key is made, in the add-provider dialog", async () => {
    api({ "GET /providers": [prov], "GET /models": [], "GET /benchmarks": [] });
    wrap(<ProvidersTab />);
    fireEvent.click(await screen.findByRole("button", { name: /Add provider/ }));
    for (const name of ["Claude (Anthropic)", "OpenAI", "Google Gemini"]) {
      const a = screen.getByRole("link", { name });
      expect(a.getAttribute("href")).toMatch(/^https:\/\//);
    }
  });
});

describe("Benchmarks tab", () => {
  const prov = [
    {
      id: 5,
      provider_id: 1,
      name: "tiny-model",
      enabled: true,
      tools: 1,
      recommended: false,
      context_override: 0,
      tool_probe: {},
    },
  ];
  it("says a benchmark is running, so a run that takes minutes does not look like nothing", async () => {
    api({
      "GET /benchmarks": [],
      "GET /models": prov,
      "GET /benchmarks/runs": [{ model_id: 5, state: "running", started: "2026-11-26T10:00:00Z" }],
    });
    wrap(<BenchmarksTab />);
    expect(await screen.findByText(/Benchmark of tiny-model is running/)).toBeInTheDocument();
    expect(screen.queryByText(/No benchmark has been run/)).toBeNull();
  });
  it("shows why a benchmark did not run", async () => {
    api({
      "GET /benchmarks": [],
      "GET /models": prov,
      "GET /benchmarks/runs": [
        {
          model_id: 5,
          state: "failed",
          started: "2026-11-26T10:00:00Z",
          message: "could not start a sandbox",
        },
      ],
    });
    wrap(<BenchmarksTab />);
    expect(await screen.findByText(/did not run: could not start a sandbox/)).toBeInTheDocument();
  });
  it("shows the empty message when nothing has run", async () => {
    api({ "GET /benchmarks": [], "GET /models": prov, "GET /benchmarks/runs": [] });
    wrap(<BenchmarksTab />);
    expect(await screen.findByText(/No benchmark has been run/)).toBeInTheDocument();
  });
});

describe("Users tab", () => {
  const user = {
    id: 3,
    email: "alex@example.com",
    name: "Alex",
    disabled: false,
    invited: false,
    totp_enabled: true,
    quota_daily_tokens: 0,
    quota_monthly_tokens: 0,
    egress_mode: "",
    allowed_models: null,
    level: "power",
    group_id: 2,
  };
  it("shows each user's level and group", async () => {
    api({
      "GET /users": [user],
      "GET /groups": [{ id: 2, name: "Engineers", kind: "group", policy: {} }],
    });
    wrap(<UsersTab />);
    expect(await screen.findByText("Alex")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Level" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Group" })).toBeInTheDocument();
    expect(await screen.findByText("Engineers")).toBeInTheDocument();
    expect(screen.getByText("power")).toBeInTheDocument();
  });
  it("keeps the email field focused while typing in the invite dialog", async () => {
    api({ "GET /users": [], "GET /groups": [] });
    wrap(<UsersTab />);
    fireEvent.click(await screen.findByRole("button", { name: "Invite user" }));
    const email = screen.getByLabelText("Email");
    email.focus();
    fireEvent.change(email, { target: { value: "a" } });
    fireEvent.change(email, { target: { value: "al" } });
    expect(document.activeElement).toBe(email);
  });
});

describe("User settings dialog", () => {
  it("opens for a user who has set nothing up, even if the server sends null for a list", async () => {
    api({
      "GET /users/3/settings": {
        user: { id: 3, email: "a@example.com", name: "Alex" },
        notes: null,
        commands: null,
        hooks: "",
        trusted_repo_hooks: 0,
        connections: null,
        mcp: null,
        automations: null,
        tokens: null,
        secret_names: null,
      },
    });
    wrap(<UserSettingsDialog user={{ id: 3, name: "Alex" }} onClose={() => undefined} />);
    expect(await screen.findByText("Notes")).toBeInTheDocument();
    expect(screen.getAllByText("None.").length).toBeGreaterThan(2);
  });
});

describe("MCP hosted-only explanation", () => {
  it("is plain about why a hosted server cannot be used", async () => {
    api({
      "GET /mcp/suggestions": { suggestions: [] },
      "GET /mcp/approved": [],
      "GET /mcp/credentials": [],
      "GET /mcp/search": {
        servers: [
          {
            name: "x/hosted",
            title: "Hosted",
            description: "d",
            version: "1",
            repository: "",
            status: "active",
            remote_only: true,
            packages: [],
          },
        ],
      },
    });
    wrap(<McpTab />);
    fireEvent.click(await screen.findByRole("tab", { name: "Search the registry" }));
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(
      await screen.findByText(/runs this server on its own infrastructure/i),
    ).toBeInTheDocument();
  });
});
