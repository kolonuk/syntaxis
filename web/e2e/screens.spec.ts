// SPDX-License-Identifier: GPL-2.0-or-later
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ADMIN_URL, BASE_URL, savedState } from "./session";

// Every screen of the admin app and of the user's settings, signed in, in light and dark and on a
// phone: it loads without a script error, fits the width, and has no serious accessibility problem.

async function scan(page: Page, where = "") {
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  for (const v of violations)
    console.log(
      `${where}: ${v.impact}: ${v.id} (${v.nodes.length}) ${v.help} ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`,
    );
  expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
}

async function fits(page: Page, where = "") {
  const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(over, `the page is wider than the window ${where}`).toBeLessThanOrEqual(1);
}

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

const sizes = [
  { name: "desktop", width: 1280, height: 800 },
  { name: "phone", width: 390, height: 800 },
];

test.describe("admin app screens", () => {
  test.skip(!savedState("admin"), "no signed-in admin (E2E_SETUP_TOKEN or E2E_ADMIN_PASSWORD)");
  test.use({ storageState: savedState("admin") });
  for (const scheme of ["light", "dark"] as const) {
    for (const size of sizes) {
      test(`every menu item opens and is accessible (${scheme}, ${size.name})`, async ({
        page,
      }) => {
        test.setTimeout(180_000);
        await page.setViewportSize({ width: size.width, height: size.height });
        await page.emulateMedia({ colorScheme: scheme });
        const errors = watchErrors(page);
        await page.goto(ADMIN_URL + "/");
        const nav = page.getByRole("navigation", { name: "Admin sections" });
        await expect(nav).toBeVisible();
        const groups = await nav.locator("h2").allTextContents();
        expect(groups).toEqual([
          "Health & updates",
          "Users",
          "Models & tools",
          "Sessions & egress",
          "System",
        ]);
        const items = await nav.getByRole("button").allTextContents();
        expect(items.length).toBeGreaterThanOrEqual(14);
        for (const label of items) {
          await nav.getByRole("button", { name: label, exact: true }).click();
          await expect(nav.getByRole("button", { name: label, exact: true })).toHaveAttribute(
            "aria-current",
            "page",
          );
          await page.waitForLoadState("networkidle");
          await fits(page, label);
          await scan(page, label);
        }
        expect(errors).toEqual([]);
      });
    }
  }
  test("the Add provider dialog and the user list are usable", async ({ page }) => {
    await page.goto(ADMIN_URL + "/");
    const nav = page.getByRole("navigation", { name: "Admin sections" });
    await nav.getByRole("button", { name: "Providers & models", exact: true }).click();
    await page.getByRole("button", { name: "Add provider" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByLabel("Name")).toBeFocused();
    await scan(page);
    await page.getByRole("button", { name: "Cancel" }).click();
    await nav.getByRole("button", { name: "Users", exact: true }).click();
    await expect(page.getByText("e2e-user@example.com")).toBeVisible();
  });
  test("inviting a user keeps the typing in the email field", async ({ page }) => {
    await page.goto(ADMIN_URL + "/");
    await page
      .getByRole("navigation", { name: "Admin sections" })
      .getByRole("button", { name: "Users", exact: true })
      .click();
    await page
      .getByRole("button", { name: /invite/i })
      .first()
      .click();
    const email = page.getByLabel("Email");
    await email.click();
    await email.pressSequentially("someone@example.com");
    await expect(email).toHaveValue("someone@example.com");
    await expect(email).toBeFocused();
  });
});

test.describe("user app screens", () => {
  test.skip(!savedState("user"), "no signed-in user (E2E_SETUP_TOKEN or E2E_ADMIN_PASSWORD)");
  test.use({ storageState: savedState("user") });
  for (const scheme of ["light", "dark"] as const) {
    for (const size of sizes) {
      test(`the home page and every settings screen are accessible (${scheme}, ${size.name})`, async ({
        page,
      }) => {
        test.setTimeout(180_000);
        await page.setViewportSize({ width: size.width, height: size.height });
        await page.emulateMedia({ colorScheme: scheme });
        const errors = watchErrors(page);
        await page.goto(BASE_URL + "/");
        await page.waitForLoadState("networkidle");
        await fits(page);
        await scan(page);
        await page
          .getByRole("button", { name: /settings/i })
          .first()
          .click();
        const nav = page.getByRole("navigation", { name: "Settings" });
        await expect(nav).toBeVisible();
        expect(await nav.locator("h2").allTextContents()).toEqual([
          "Account",
          "Services and keys",
          "Agent",
          "Sharing",
        ]);
        for (const label of await nav.getByRole("button").allTextContents()) {
          await nav.getByRole("button", { name: label, exact: true }).click();
          await expect(nav.getByRole("button", { name: label, exact: true })).toHaveAttribute(
            "aria-current",
            "page",
          );
          await page.waitForLoadState("networkidle");
          await fits(page, label);
          await scan(page, label);
        }
        expect(errors).toEqual([]);
      });
    }
  }
  test("the new-session pop-up opens, closes with Cancel, and is accessible", async ({ page }) => {
    await page.goto(BASE_URL + "/");
    await page
      .getByRole("button", { name: /new session/i })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await scan(page);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
  });
  test("settings tools lets a user search for an MCP server to request", async ({ page }) => {
    await page.goto(BASE_URL + "/");
    await page
      .getByRole("button", { name: /settings/i })
      .first()
      .click();
    await page
      .getByRole("navigation", { name: "Settings" })
      .getByRole("button", { name: "Tools", exact: true })
      .click();
    await expect(page.getByRole("heading", { name: "Find more tools" })).toBeVisible();
  });
});
