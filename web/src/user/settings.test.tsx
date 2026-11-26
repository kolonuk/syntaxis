// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import { ToastProvider } from "../ui/ui";
import { Connections } from "./Connections";
import { McpTools } from "./McpTools";
import { CommandsEditor, HooksEditor, Notes, parseHooks } from "./AgentSettings";
import { SETTINGS_GROUPS, Settings } from "./Settings";
import { ApiTokens } from "./ApiTokens";
import { Files, FILES_POLL_MS } from "./Files";

afterEach(cleanup);

type Route = unknown | ((body: unknown) => unknown);
interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** Answers the chat API from a table of "METHOD /path" to a reply, and records every call. A reply
 *  of the form {__status: n, body} sends that status. */
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
    const r = out as { __status?: number; body?: unknown };
    if (r && typeof r === "object" && r.__status)
      return new Response(JSON.stringify(r.body), { status: r.__status });
    return new Response(JSON.stringify(out), { status: 200 });
  }) as typeof fetch);
  return calls;
}

const wrap = (ui: React.ReactNode) => render(<ToastProvider>{ui}</ToastProvider>);

const kind = (id: string, title: string, fields: object[], git = false) => ({
  id,
  title,
  guide: "How to make a token.",
  git,
  fields,
});

describe("Settings navigation", () => {
  it("groups the pages and switches between them", async () => {
    api({
      "GET /usage": {
        daily: { Used: 0, Limit: 0 },
        monthly: { Used: 0, Limit: 0 },
        sandbox_seconds: { Used: 0, Limit: 0 },
      },
    });
    wrap(
      <Settings
        me={{ name: "Ann", email: "ann@example.com", totp_enabled: true } as never}
        reload={() => undefined}
        onLogout={() => undefined}
      />,
    );
    const nav = screen.getByRole("navigation", { name: "Settings" });
    for (const g of SETTINGS_GROUPS) {
      expect(within(nav).getByRole("heading", { name: g.label })).toBeTruthy();
      for (const i of g.items)
        expect(within(nav).getByRole("button", { name: i.label })).toBeTruthy();
    }
    expect(within(nav).getByRole("button", { name: "Profile" }).getAttribute("aria-current")).toBe(
      "page",
    );
    fireEvent.click(within(nav).getByRole("button", { name: "Usage" }));
    expect(await screen.findByText("Tokens today")).toBeTruthy();
    expect(within(nav).getByRole("button", { name: "Usage" }).getAttribute("aria-current")).toBe(
      "page",
    );
  });
});

describe("Connections", () => {
  const gitlab = {
    kind: kind(
      "gitlab",
      "GitLab",
      [
        {
          name: "GITLAB_HOST",
          label: "GitLab address",
          secret: false,
          required: false,
          default: "https://gitlab.com",
        },
        { name: "GITLAB_TOKEN", label: "Token", secret: true, required: true },
      ],
      true,
    ),
    connected: false,
    account: "",
    detail: "",
    checked_at: "",
    values: { GITLAB_HOST: "" },
    stored: { GITLAB_TOKEN: false },
  };

  it("does not fill in a default the user never chose, and says what it will use", async () => {
    api({ "GET /connections": [gitlab] });
    wrap(<Connections />);
    fireEvent.click(await screen.findByRole("button", { name: "Set up" }));
    const host = screen.getByLabelText(/GitLab address/) as HTMLInputElement;
    expect(host.value).toBe("");
    expect(host.placeholder).toContain("https://gitlab.com");
    expect(
      screen.getByRole("button", { name: "Hide settings" }).getAttribute("aria-expanded"),
    ).toBe("true");
  });

  it("shows a prominent result in the card, for a failure and then for a success", async () => {
    let ok = false;
    api({
      "GET /connections": [gitlab],
      "PUT /connections/gitlab": () =>
        ok ? { account: "ann" } : { __status: 400, body: { error: "the token was refused" } },
    });
    wrap(<Connections />);
    fireEvent.click(await screen.findByRole("button", { name: "Set up" }));
    fireEvent.change(screen.getByLabelText(/^Token/), { target: { value: "tok" } });
    fireEvent.click(screen.getByRole("button", { name: "Test the connection and save" }));
    const fail = (await screen.findAllByRole("alert")).find((e) =>
      e.classList.contains("result-banner"),
    )!;
    expect(fail.textContent).toContain("Not saved");
    expect(fail.textContent).toContain("the token was refused");
    ok = true;
    fireEvent.click(screen.getByRole("button", { name: "Test the connection and save" }));
    await waitFor(() =>
      expect(document.querySelector(".result-banner.ok")?.textContent).toContain(
        "connected as ann",
      ),
    );
  });
});

describe("Notes", () => {
  it("chooses the repository from the connected git services instead of typing it", async () => {
    const calls = api({
      "GET /memory": [],
      "GET /connections": [
        {
          kind: kind(
            "github",
            "GitHub",
            [{ name: "GITHUB_TOKEN", label: "Token", secret: true, required: true }],
            true,
          ),
          connected: true,
          account: "ann",
          detail: "",
          checked_at: "",
          values: {},
          stored: { GITHUB_TOKEN: true },
        },
      ],
      "GET /connections/github/repos": [
        {
          full_name: "ann/site",
          description: "my site",
          private: true,
          clone_url: "u",
          default_branch: "main",
        },
      ],
      "POST /memory": { id: 1 },
    });
    wrap(<Notes />);
    fireEvent.change(await screen.findByLabelText("About"), { target: { value: "repo" } });
    const repo = (await screen.findByLabelText("Repository")) as HTMLSelectElement;
    expect(repo.tagName).toBe("SELECT");
    expect(screen.getByLabelText("Git provider").tagName).toBe("SELECT");
    await screen.findByRole("option", { name: /ann\/site \(private\)/ });
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: "run make test" } });
    const add = screen.getByRole("button", { name: "Add note" }) as HTMLButtonElement;
    expect(add.disabled).toBe(true); // a repository note needs a repository
    fireEvent.change(repo, { target: { value: "ann/site" } });
    await waitFor(() => expect(add.disabled).toBe(false));
    fireEvent.click(add);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/memory")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      scope: "repo",
      repo: "ann/site",
      content: "run make test",
    });
  });
});

describe("Commands and tokens as lists with a pop-up", () => {
  it("edits a command in a dialog and saves the whole list", async () => {
    const calls = api({
      "GET /commands": {
        commands: [{ name: "review", description: "look", template: "Review $ARGUMENTS" }],
        builtin: [],
      },
      "PUT /commands": {},
    });
    wrap(<CommandsEditor />);
    expect(await screen.findByText("/review")).toBeTruthy();
    expect(screen.queryByLabelText("Text")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Text"), {
      target: { value: "Review hard $ARGUMENTS" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      commands: [{ name: "review", description: "look", template: "Review hard $ARGUMENTS" }],
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("creates a token in a dialog and shows it once", async () => {
    api({
      "GET /tokens": [],
      "POST /tokens": {
        id: 1,
        name: "ci",
        prefix: "syx_ab",
        scope: "read",
        created_at: "2026-01-01T00:00:00Z",
        revoked: false,
        token: "syx_secret_value",
      },
    });
    wrap(<ApiTokens />);
    fireEvent.click(await screen.findByRole("button", { name: "Create a token" }));
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "ci" } });
    fireEvent.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("syx_secret_value")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("Hooks", () => {
  it("reads a hooks setting, and links to the help", async () => {
    expect(parseHooks("")).toEqual([]);
    expect(parseHooks("not json")).toBeNull();
    expect(parseHooks('{"hooks":[{"event":"post_tool","command":"gofmt -l ."}]}')).toHaveLength(1);
    api({
      "GET /hooks": {
        hooks: '{"hooks":[{"event":"post_tool","tools":["write_file"],"command":"gofmt -l ."}]}',
      },
    });
    wrap(<HooksEditor />);
    expect(await screen.findByText("gofmt -l .")).toBeTruthy();
    const help = screen.getByRole("link", { name: /How to write hooks/ }) as HTMLAnchorElement;
    expect(help.href).toContain("docs/agent.md#writing-hooks");
  });

  it("adds a hook from a dialog and saves the JSON", async () => {
    const calls = api({ "GET /hooks": { hooks: "" }, "PUT /hooks": {} });
    wrap(<HooksEditor />);
    fireEvent.click(await screen.findByRole("button", { name: "Add a hook" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Command"), {
      target: { value: "npm run lint" },
    });
    fireEvent.change(within(dialog).getByLabelText(/^Tools/), {
      target: { value: "write_file, patch_file" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const sent = JSON.parse(
      (calls.find((c) => c.method === "PUT")?.body as { hooks: string }).hooks,
    );
    expect(sent).toEqual({
      hooks: [{ event: "post_tool", command: "npm run lint", tools: ["write_file", "patch_file"] }],
    });
  });
});

describe("MCP tools: search and request", () => {
  it("lets any user search the catalogue, request a server and withdraw the request", async () => {
    let requests: object[] = [];
    const calls = api({
      "GET /mcp": [],
      "GET /mcp/requests": () => requests,
      "GET /mcp/search": {
        servers: [
          {
            name: "io.example/notes",
            title: "Notes",
            description: "keeps notes",
            approved: false,
            requested: false,
          },
        ],
        next: "",
      },
      "POST /mcp/requests": () => {
        requests = [{ id: "r1", name: "io.example/notes", title: "Notes", reason: "meetings" }];
        return requests[0];
      },
      "DELETE /mcp/requests/r1": () => {
        requests = [];
        return { ok: true };
      },
    });
    wrap(<McpTools />);
    fireEvent.change(await screen.findByLabelText(/^Search/), { target: { value: "notes" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: "Request" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/Why do you want it/), {
      target: { value: "meetings" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Send request" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/mcp/requests")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "io.example/notes",
      title: "Notes",
      reason: "meetings",
    });
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
  });
});

describe("Files", () => {
  const listing = (names: string[]) => ({
    available: true,
    path: "/workspace",
    entries: names
      .map((n) => ({
        name: n,
        type: n.endsWith("/") ? "dir" : "file",
        size: 3,
        modified: "2026-01-01T00:00:00Z",
      }))
      .map((e) => ({ ...e, name: e.name.replace("/", "") })),
  });

  it("has no refresh button, offers zip downloads, and keeps the list current", async () => {
    let names = ["src/"];
    api({ "GET /sessions/s1/files": () => listing(names) });
    expect(FILES_POLL_MS).toBeGreaterThan(0);
    wrap(<Files sessionId="s1" running={false} />);
    await screen.findByText("src/");
    expect(screen.queryByRole("button", { name: /refresh/i })).toBeNull();
    const all = screen.getByRole("link", {
      name: "Download the whole workspace as a zip",
    }) as HTMLAnchorElement;
    expect(all.getAttribute("href")).toBe("/api/sessions/s1/files/zip?path=%2Fworkspace");
    const dir = screen.getByRole("link", { name: "Download src as a zip" }) as HTMLAnchorElement;
    expect(dir.getAttribute("href")).toBe("/api/sessions/s1/files/zip?path=%2Fworkspace%2Fsrc");
    // a file has no folder zip
    names = ["src/", "a.txt"];
    await screen.findByText("a.txt", {}, { timeout: FILES_POLL_MS + 3000 });
    expect(screen.queryByRole("link", { name: "Download a.txt as a zip" })).toBeNull();
  }, 15000);
});

describe("MCP tools: your own credentials", () => {
  it("shows what each server needs from you, saves it write-only, and never shows a value", async () => {
    let have = false;
    const calls = api({
      "GET /mcp": () => [
        {
          name: "io.example/notes",
          package: "notes-mcp@1.0.0",
          enabled: false,
          missing: have ? [] : ["NOTES_TOKEN"],
          ready: have,
          credentials: [{ name: "NOTES_TOKEN", env: "NOTES_TOKEN", set: have }],
        },
      ],
      "GET /mcp/requests": [],
      "PUT /secrets/NOTES_TOKEN": () => {
        have = true;
        return { ok: true };
      },
    });
    wrap(<McpTools />);
    expect(await screen.findByText("needed")).toBeTruthy();
    expect(screen.getByText(/needs NOTES_TOKEN/)).toBeTruthy();
    const field = screen.getByLabelText("Your NOTES_TOKEN") as HTMLInputElement;
    expect(field.type).toBe("password");
    fireEvent.change(field, { target: { value: "my-own-value" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("yours is set")).toBeTruthy());
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ value: "my-own-value" });
    // the value is cleared from the box and not shown anywhere
    expect(screen.queryByDisplayValue("my-own-value")).toBeNull();
    expect(screen.queryByText("my-own-value")).toBeNull();
  });
  it("tells the user why a server will not switch on until the credentials are theirs", async () => {
    api({
      "GET /mcp": [
        {
          name: "io.example/notes",
          package: "notes-mcp@1.0.0",
          enabled: false,
          missing: ["NOTES_TOKEN"],
          ready: false,
          credentials: [{ name: "NOTES_TOKEN", env: "NOTES_TOKEN", set: false }],
        },
      ],
      "GET /mcp/requests": [],
      "PUT /mcp": {
        __status: 409,
        body: { error: "add your own NOTES_TOKEN first (Settings, Tools, or Connections)" },
      },
    });
    wrap(<McpTools />);
    fireEvent.click(await screen.findByRole("switch", { name: "On" }));
    expect(await screen.findByText(/add your own NOTES_TOKEN first/)).toBeTruthy();
    expect(screen.getByText(/will not turn on/)).toBeTruthy();
  });
});
