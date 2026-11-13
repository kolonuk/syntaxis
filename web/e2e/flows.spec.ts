// SPDX-License-Identifier: GPL-2.0-or-later
import { expect, test } from "@playwright/test";

const ADMIN_URL = process.env.ADMIN_URL ?? "http://127.0.0.1:8081";

test("the chat app shows a sign-in form, has no sign-up, and is installable", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(page.getByText(/sign up|register|create account/i)).toHaveCount(0);
  const manifest = await page.evaluate(async () => (await fetch("/manifest.webmanifest")).json());
  expect(manifest.display).toBe("standalone");
  expect(manifest.icons.some((i: { purpose: string }) => i.purpose === "maskable")).toBe(true);
  await page.waitForFunction(() => navigator.serviceWorker.ready.then(() => true));
});

test("offline: the app shell still loads from the service worker", async ({ page, context }) => {
  await page.goto("/");
  await page.evaluate(() => navigator.serviceWorker.ready);
  await context.setOffline(true);
  await page.reload();
  await expect(page.locator("body")).toContainText(/Sign in|offline/i);
});

test("the admin app is a separate login on its own address", async ({ page }) => {
  await page.goto(ADMIN_URL + "/");
  await expect(page.getByRole("heading", { name: "Admin sign in" })).toBeVisible();
  await expect(page.getByText(/forgot your password/i)).toHaveCount(0);
});
