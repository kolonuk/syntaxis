// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import App, { MODEL_NAMES_HELP, MODEL_QUANT_HELP } from "./App";

afterEach(cleanup);

function serve(routes: Record<string, unknown>) {
  _setFetch((async (url: string, init: RequestInit) => {
    const path = url.replace(/^\/api/, "").split("?")[0];
    const hit = routes[`${init.method ?? "GET"} ${path}`];
    // screens the test does not look at get an empty list
    if (hit === undefined) return new Response("[]", { status: 200 });
    return new Response(JSON.stringify(hit), { status: 200 });
  }) as typeof fetch);
}

const usage = {
  daily: { Used: 0, Limit: 0 },
  monthly: { Used: 0, Limit: 0 },
  sandbox_seconds: { Used: 0, Limit: 0 },
};

const me = {
  kind: "user",
  name: "Ann",
  email: "ann@example.com",
  totp_enabled: true,
  must_change_password: false,
  password_days_left: -1,
  csrf: "c",
  remind_2fa: false,
};

describe("the chat app's title bar and new-session dialog", () => {
  it("puts the sessions/automations switch on the left, next to the name, with a border", async () => {
    serve({ "GET /me": me, "GET /sessions": [], "GET /usage": usage });
    render(<App />);
    const header = await screen.findByRole("banner");
    const toggle = within(header).getByRole("button", { name: "Automations" });
    expect(toggle.className).toContain("view-switch");
    const kids = Array.from(header.children);
    const brand = kids.findIndex((e) => e.tagName === "H1");
    const settings = kids.findIndex((e) => e.getAttribute("aria-label") === "Settings");
    expect(kids.indexOf(toggle)).toBe(brand + 1);
    expect(kids.indexOf(toggle)).toBeLessThan(settings);
    fireEvent.click(toggle);
    expect(toggle.textContent).toBe("Sessions");
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
  });

  it("shows the repository and environment choices, and links to what model names mean", async () => {
    serve({
      "GET /me": me,
      "GET /sessions": [],
      "GET /models": [
        { id: "prov/m1", name: "m1", provider: "prov", tools: true, recommended: false },
      ],
      "GET /environments": [],
      "GET /connections": [],
      "GET /usage": usage,
      "GET /parallel/capacity": { max: 1, recommended: 1, limits: [] },
    });
    render(<App />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New session/ }))[0]!);
    const dialog = await screen.findByRole("dialog", { name: "New session" });
    // the environment is always offered, even before an admin adds any
    expect(await within(dialog).findByLabelText(/^Environment/)).toBeTruthy();
    // with no git service connected, the dialog says why there is no repository list
    expect(await within(dialog).findByText(/No git service is connected/)).toBeTruthy();
    const moe = within(dialog).getByRole("link", {
      name: /mixture of experts/,
    }) as HTMLAnchorElement;
    const quant = within(dialog).getByRole("link", { name: /quantisation/ }) as HTMLAnchorElement;
    expect(moe.href).toBe(MODEL_NAMES_HELP);
    expect(quant.href).toBe(MODEL_QUANT_HELP);
    expect(MODEL_NAMES_HELP.startsWith("https://")).toBe(true);
  });

  it("marks a model from a user's own provider as leaving this machine", async () => {
    serve({
      "GET /me": me,
      "GET /sessions": [],
      "GET /models": [
        { id: "prov/m1", name: "m1", provider: "prov", tools: true, recommended: false },
        {
          id: "@hosted/alpha",
          name: "alpha",
          provider: "@hosted",
          tools: true,
          recommended: false,
          external: true,
        },
      ],
      "GET /environments": [],
      "GET /connections": [],
      "GET /usage": usage,
      "GET /parallel/capacity": { max: 1, recommended: 1, limits: [] },
    });
    render(<App />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New session/ }))[0]!);
    const dialog = await screen.findByRole("dialog", { name: "New session" });
    const options = within(dialog)
      .getAllByRole("option")
      .map((o) => o.textContent ?? "");
    expect(options).toContain("@hosted/alpha (leaves this machine)");
    expect(options).toContain("prov/m1");
  });
});
