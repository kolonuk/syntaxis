// SPDX-License-Identifier: GPL-2.0-or-later
import { createHmac, randomBytes } from "node:crypto";
import { mkdirSync } from "node:fs";
import { request, type APIRequestContext } from "@playwright/test";

export const BASE_URL = process.env.BASE_URL ?? "http://127.0.0.1:8080";
export const ADMIN_URL = process.env.ADMIN_URL ?? "http://127.0.0.1:8081";
export const AUTH_DIR = "e2e/.auth";
export const ADMIN_STATE = `${AUTH_DIR}/admin.json`;
export const USER_STATE = `${AUTH_DIR}/user.json`;

/** A time-based one-time code (RFC 6238: SHA-1, 30 seconds, 6 digits) for a base32 secret. */
export function totp(secret: string, at = Date.now()): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const ch of secret.toUpperCase().replace(/=+$/, ""))
    bits += alphabet.indexOf(ch).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const mac = createHmac("sha1", key).update(counter).digest();
  const off = mac[mac.length - 1]! & 0x0f;
  const n = mac.readUInt32BE(off) & 0x7fffffff;
  return String(n % 1_000_000).padStart(6, "0");
}

let lastWindow = 0;
/** A code is accepted once, so wait for the next window when this one was already used. */
async function freshCode(secret: string): Promise<string> {
  let w = Math.floor(Date.now() / 30_000);
  if (w <= lastWindow) {
    await new Promise((r) => setTimeout(r, 30_000 - (Date.now() % 30_000) + 500));
    w = Math.floor(Date.now() / 30_000);
  }
  lastWindow = w;
  return totp(secret);
}

let csrfToken = "";

async function api(
  ctx: APIRequestContext,
  base: string,
  method: string,
  path: string,
  data?: unknown,
) {
  const res = await ctx.fetch(base + "/api" + path, {
    method,
    data,
    headers: { Origin: base, ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}) },
  });
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok()) throw new Error(`${method} ${path}: HTTP ${res.status()} ${text.slice(0, 200)}`);
  if (typeof body.csrf === "string") csrfToken = body.csrf;
  return body as Record<string, any>;
}

/** Signs in, setting up two-factor authentication the first time, and returns the browser state. */
async function signIn(base: string, email: string, password: string, statePath: string) {
  const ctx = await request.newContext({ baseURL: base });
  try {
    csrfToken = "";
    const login = await api(ctx, base, "POST", "/login", { email, password });
    if (login.needs_mfa) throw new Error("two-factor is already set up for " + email);
    const begin = await api(ctx, base, "POST", "/2fa/begin");
    await api(ctx, base, "POST", "/2fa/confirm", { code: await freshCode(begin.secret) });
    await api(ctx, base, "GET", "/me");
    await ctx.storageState({ path: statePath });
  } finally {
    await ctx.dispose();
  }
}

/**
 * Makes a signed-in admin and a signed-in user for the screen tests. The admin comes from the
 * installer's single-use link (E2E_SETUP_TOKEN) or from a known password (E2E_ADMIN_PASSWORD, for an
 * instance started with a bootstrap admin). Without either the screen tests are skipped.
 */
export async function prepareSessions(): Promise<boolean> {
  const email = process.env.E2E_ADMIN_EMAIL ?? "admin@example.com";
  const token = process.env.E2E_SETUP_TOKEN;
  let password = process.env.E2E_ADMIN_PASSWORD ?? "";
  if (!token && !password) return false;
  mkdirSync(AUTH_DIR, { recursive: true });
  if (token) {
    password = `Ee2e-${randomBytes(9).toString("base64url")}-1!`;
    const ctx = await request.newContext({ baseURL: ADMIN_URL });
    await api(ctx, ADMIN_URL, "POST", "/setup", { token, password });
    await ctx.dispose();
  }
  await signIn(ADMIN_URL, email, password, ADMIN_STATE);

  // a user, invited by the admin and signed in
  const admin = await request.newContext({ baseURL: ADMIN_URL, storageState: ADMIN_STATE });
  csrfToken = "";
  await api(admin, ADMIN_URL, "GET", "/me");
  const invite = await api(admin, ADMIN_URL, "POST", "/users", {
    email: "e2e-user@example.com",
    name: "E2E User",
  });
  await admin.dispose();
  const userToken = new URL(invite.invite_url as string).searchParams.get("token") ?? "";
  const userPassword = `Ue2e-${randomBytes(9).toString("base64url")}-2!`;
  const anon = await request.newContext({ baseURL: BASE_URL });
  csrfToken = "";
  await api(anon, BASE_URL, "POST", "/invite/accept", { token: userToken, password: userPassword });
  await anon.dispose();
  await signIn(BASE_URL, "e2e-user@example.com", userPassword, USER_STATE);
  return true;
}
