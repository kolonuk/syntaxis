// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import { ToastProvider } from "../ui/ui";
import { MyProviders } from "./MyProviders";

afterEach(cleanup);

interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** Answers the chat API from a table of "METHOD /path" to a reply, and records every call. */
function api(routes: Record<string, unknown>) {
  const calls: Call[] = [];
  _setFetch((async (url: string, init: RequestInit) => {
    const method = init.method ?? "GET";
    const path = url.replace(/^\/api/, "");
    calls.push({ method, path, body: init.body ? JSON.parse(String(init.body)) : undefined });
    const hit = routes[`${method} ${path}`];
    if (hit === undefined)
      return new Response(JSON.stringify({ error: "no such route in the test" }), { status: 404 });
    return new Response(JSON.stringify(hit), { status: 200 });
  }) as typeof fetch);
  return calls;
}

const wrap = (ui: React.ReactNode) => render(<ToastProvider>{ui}</ToastProvider>);

const provider = {
  id: 7,
  name: "hosted",
  base_url: "https://api.example.com",
  concurrency: 2,
  has_key: true,
  models: [
    { id: 1, name: "alpha", enabled: false, context: 32768 },
    { id: 2, name: "beta", enabled: true, context: 32768 },
  ],
};

describe("My model providers", () => {
  it("says so, and offers nothing, while an admin has not turned the feature on", async () => {
    api({ "GET /my/providers": { allowed: false, max: 5, providers: [] } });
    wrap(<MyProviders />);
    expect(await screen.findByText(/has not turned on providers of your own/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add a provider" })).toBeNull();
  });

  it("lists providers without any key, and adds one with a password field that is cleared", async () => {
    const calls = api({
      "GET /my/providers": { allowed: true, max: 5, providers: [provider] },
      "POST /my/providers": provider,
    });
    wrap(<MyProviders />);
    expect(await screen.findByText("hosted")).toBeTruthy();
    expect(screen.getByText("1 of 2 models on")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Add a provider" }));
    const dialog = screen.getByRole("dialog");
    const key = within(dialog).getByLabelText("API key") as HTMLInputElement;
    expect(key.type).toBe("password");
    // where to make a key
    fireEvent.click(within(dialog).getByText("Where do I get an API key?"));
    expect(within(dialog).getByRole("link", { name: "OpenAI" }).getAttribute("href")).toMatch(
      /^https:\/\//,
    );
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "second" } });
    fireEvent.change(within(dialog).getByLabelText("Address"), {
      target: { value: "https://api.example.org/v1" },
    });
    fireEvent.change(key, { target: { value: "pasted-key" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check and save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "second",
      base_url: "https://api.example.org/v1",
      api_key: "pasted-key",
    });
    // the dialog closes and the key is not kept anywhere on the page
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.innerHTML).not.toContain("pasted-key");
  });

  it("turns models on and off, and sets a window, one at a time", async () => {
    const calls = api({
      "GET /my/providers": { allowed: true, max: 5, providers: [provider] },
      "PUT /my/providers/7/models/1": provider,
      "PUT /my/providers/7/models/2": provider,
    });
    wrap(<MyProviders />);
    fireEvent.click(await screen.findByRole("button", { name: "Models" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("switch", { name: "alpha" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/my/providers/7/models/1")).toBe(true),
    );
    expect(calls.find((c) => c.path === "/my/providers/7/models/1")?.body).toEqual({
      enabled: true,
    });
    const window = within(dialog).getByLabelText("Window of beta (tokens)");
    fireEvent.change(window, { target: { value: "65536" } });
    fireEvent.blur(window);
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/my/providers/7/models/2")).toBe(true),
    );
    expect(calls.find((c) => c.path === "/my/providers/7/models/2")?.body).toEqual({
      context: 65536,
    });
  });

  it("asks before removing", async () => {
    const calls = api({
      "GET /my/providers": { allowed: true, max: 5, providers: [provider] },
      "DELETE /my/providers/7": { ok: true },
    });
    wrap(<MyProviders />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
  });
});
