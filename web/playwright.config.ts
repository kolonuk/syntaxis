// SPDX-License-Identifier: GPL-2.0-or-later
import { defineConfig } from "@playwright/test";

// End-to-end flows run against a throwaway instance started by CI:
//   syntaxis serve (dev mail mode) with a bootstrap admin and a fake LLM server.
// BASE_URL / ADMIN_URL point at it. Not run as part of `npm test`.
export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  use: { baseURL: process.env.BASE_URL ?? "http://127.0.0.1:8080", trace: "off" },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
