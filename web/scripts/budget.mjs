// SPDX-License-Identifier: GPL-2.0-or-later
// Fails when the first-load JavaScript or CSS of the built app grows past a budget. The heavy
// parts (diff view, markdown, terminal) are lazy-loaded and are budgeted separately.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const dir = new URL("../../internal/webui/dist/", import.meta.url).pathname;
const budgetKB = { "index-initial-js": 130, "total-js": 450, css: 20 };

function walk(d) {
  return readdirSync(d).flatMap((f) => {
    const p = join(d, f);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}
const files = walk(dir);
const gz = (p) => gzipSync(readFileSync(p)).length / 1024;
const js = files.filter((f) => f.endsWith(".js"));
const css = files.filter((f) => f.endsWith(".css"));
const sum = (fs) => fs.reduce((n, f) => n + gz(f), 0);
// the entry chunks are the ones the HTML loads first
const html = readFileSync(join(dir, "index.html"), "utf8");
const entry = js.filter((f) => html.includes(f.slice(dir.length)));
const rows = [
  ["first-load JS (gzip)", sum(entry), budgetKB["index-initial-js"]],
  ["all JS (gzip)", sum(js), budgetKB["total-js"]],
  ["CSS (gzip)", sum(css), budgetKB.css],
];
let bad = false;
for (const [name, kb, max] of rows) {
  const over = kb > max;
  bad ||= over;
  console.log(`${over ? "OVER " : "ok   "} ${name}: ${kb.toFixed(1)} KB of ${max} KB`);
}
process.exit(bad ? 1 : 0);
