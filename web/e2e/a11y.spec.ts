// SPDX-License-Identifier: GPL-2.0-or-later
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const ADMIN_URL = process.env.ADMIN_URL ?? "http://127.0.0.1:8081";

// Serious and critical accessibility problems fail the build; the rest are reported in the log.
async function scan(page: import("@playwright/test").Page) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  for (const v of violations) console.log(`${v.impact}: ${v.id} (${v.nodes.length}) ${v.help}`);
  expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
}

for (const scheme of ["light", "dark"] as const) {
  test(`the sign-in page has no serious accessibility problems (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
    await scan(page);
  });

  test(`the admin sign-in page has no serious accessibility problems (${scheme})`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto(ADMIN_URL + "/");
    await expect(page.getByRole("heading", { name: "Admin sign in" })).toBeVisible();
    await scan(page);
  });
}
