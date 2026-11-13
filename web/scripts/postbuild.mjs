// SPDX-License-Identifier: GPL-2.0-or-later
// Verifies the build produced both apps and the PWA files, and keeps the
// embed placeholder so `go build` works from a clean checkout.
import { existsSync, writeFileSync } from "node:fs";
const dist = new URL("../../internal/webui/dist/", import.meta.url).pathname;
for (const f of ["index.html", "admin/index.html", "sw.js", "manifest.webmanifest", "offline.html"]) {
  if (!existsSync(dist + f)) {
    console.error("missing build output: " + f);
    process.exit(1);
  }
}
writeFileSync(dist + ".gitkeep", "");
console.log("web build ok");
