// SPDX-License-Identifier: GPL-2.0-or-later
import { describe, expect, it } from "vitest";
import { commentsMessage, parseDiff } from "./diff";

const sample = `### branch syntaxis/abc
### status
 M a.go
?? b.txt
### diff
diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 keep
-old
+new
+more
diff --git a/dir/c.ts b/dir/c.ts
--- a/dir/c.ts
+++ b/dir/c.ts
@@ -1 +1 @@
-x
+y
`;

describe("parseDiff", () => {
  it("reads the branch, status and files", () => {
    const d = parseDiff(sample);
    expect(d.branch).toBe("syntaxis/abc");
    expect(d.status).toEqual([" M a.go", "?? b.txt"]);
    expect(d.files.map((f) => f.name)).toEqual(["a.go", "dir/c.ts"]);
    expect(d.files[0]).toMatchObject({ added: 2, removed: 1 });
    expect(d.files[1]).toMatchObject({ added: 1, removed: 1 });
  });
  it("copes with nothing changed", () => {
    expect(parseDiff("### branch main\n### status\n### diff\n").files).toEqual([]);
  });
});

describe("commentsMessage", () => {
  it("lists only the files that have a comment", () => {
    const m = commentsMessage({ "a.go": " rename this ", "b.go": "  " });
    expect(m).toContain("- a.go: rename this");
    expect(m).not.toContain("b.go");
  });
  it("is empty without comments", () => {
    expect(commentsMessage({ "a.go": "" })).toBe("");
  });
});
