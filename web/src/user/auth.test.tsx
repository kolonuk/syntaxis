// SPDX-License-Identifier: GPL-2.0-or-later
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { _setFetch } from "../lib/api";
import { Login, SetPassword } from "./Auth";

afterEach(cleanup);

function respond(routes: Record<string, { status?: number; body: unknown }>) {
  const seen: { url: string; body: unknown }[] = [];
  _setFetch((async (url: string, init: RequestInit) => {
    seen.push({ url, body: init.body ? JSON.parse(String(init.body)) : undefined });
    const r = routes[url] ?? { status: 404, body: { error: "nope" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200 });
  }) as typeof fetch);
  return seen;
}

describe("Login", () => {
  it("asks for the 2FA code after the password step", async () => {
    const seen = respond({
      "/api/login": { body: { needs_mfa: true, csrf: "" } },
      "/api/login/mfa": { body: { needs_mfa: false, csrf: "c" } },
    });
    const done = vi.fn();
    render(<Login onDone={done} go={() => undefined} />);
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(screen.getByLabelText(/Authentication code/)).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText(/Authentication code/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(seen.map((s) => s.url)).toEqual(["/api/login", "/api/login/mfa"]);
  });
  it("shows a generic error and never reveals which field was wrong", async () => {
    respond({ "/api/login": { status: 401, body: { error: "invalid email or password" } } });
    render(<Login onDone={() => undefined} go={() => undefined} />);
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("invalid email or password");
  });
  it("offers password reset only on the chat login, not the admin login", () => {
    const { rerender } = render(<Login onDone={() => undefined} go={() => undefined} />);
    expect(screen.getByText(/Forgot your password/)).toBeInTheDocument();
    rerender(<Login prefix="Admin" onDone={() => undefined} go={() => undefined} />);
    expect(screen.queryByText(/Forgot your password/)).toBeNull();
  });
});

describe("SetPassword", () => {
  it("rejects mismatched passwords without calling the server", async () => {
    const seen = respond({});
    render(<SetPassword kind="invite" token="t" go={() => undefined} />);
    fireEvent.change(screen.getByLabelText("New password"), {
      target: { value: "one long passphrase" },
    });
    fireEvent.change(screen.getByLabelText("Repeat password"), { target: { value: "different" } });
    fireEvent.click(screen.getByRole("button", { name: "Save password" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("do not match");
    expect(seen).toHaveLength(0);
  });
  it("uses the invite endpoint for invites and the reset endpoint for resets", async () => {
    const seen = respond({
      "/api/invite/accept": { body: { ok: true } },
      "/api/password/reset": { body: { ok: true } },
    });
    const { unmount } = render(<SetPassword kind="invite" token="tok1" go={() => undefined} />);
    fireEvent.change(screen.getByLabelText("New password"), {
      target: { value: "a long passphrase" },
    });
    fireEvent.change(screen.getByLabelText("Repeat password"), {
      target: { value: "a long passphrase" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save password" }));
    await screen.findByText("Password saved");
    expect(seen[0]).toEqual({
      url: "/api/invite/accept",
      body: { token: "tok1", password: "a long passphrase" },
    });
    unmount();
    render(<SetPassword kind="reset" token="tok2" go={() => undefined} />);
    fireEvent.change(screen.getByLabelText("New password"), {
      target: { value: "another long passphrase" },
    });
    fireEvent.change(screen.getByLabelText("Repeat password"), {
      target: { value: "another long passphrase" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save password" }));
    await screen.findByText("Password saved");
    expect(seen[1]!.url).toBe("/api/password/reset");
  });
});
