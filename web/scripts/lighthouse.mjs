// SPDX-License-Identifier: GPL-2.0-or-later
// Runs Lighthouse against a running instance and fails below the scores this app keeps.
// Usage: node scripts/lighthouse.mjs [url]   (CHROME_PATH may name the browser to use)
import { execFileSync } from "node:child_process";
import { readFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const url = process.argv[2] ?? process.env.BASE_URL ?? "http://127.0.0.1:8080/";
const min = { accessibility: 0.95, "best-practices": 0.9, seo: 0.8, performance: 0.8 };
const out = join(mkdtempSync(join(tmpdir(), "lh-")), "report.json");
execFileSync(
  "npx",
  [
    "--yes",
    "lighthouse@12",
    url,
    "--quiet",
    "--output=json",
    `--output-path=${out}`,
    `--only-categories=${Object.keys(min).join(",")}`,
    "--chrome-flags=--headless=new --no-sandbox",
  ],
  { stdio: "inherit" },
);
const report = JSON.parse(readFileSync(out, "utf8"));
let bad = false;
for (const [id, floor] of Object.entries(min)) {
  const score = report.categories[id]?.score ?? 0;
  const low = score < floor;
  bad ||= low;
  console.log(
    `${low ? "LOW " : "ok  "} ${id}: ${Math.round(score * 100)} (needs ${Math.round(floor * 100)})`,
  );
  if (low) {
    for (const ref of report.categories[id].auditRefs) {
      const a = report.audits[ref.id];
      if (a.score !== null && a.score < 0.9 && ref.weight > 0) console.log(`       - ${a.title}`);
    }
  }
}
process.exit(bad ? 1 : 0);
