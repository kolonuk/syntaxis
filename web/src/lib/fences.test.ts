// SPDX-License-Identifier: GPL-2.0-or-later
import { describe, expect, it } from "vitest";
import { splitFences } from "./fences";

describe("splitFences", () => {
  it("returns plain text untouched", () => {
    expect(splitFences("hello\nworld")).toEqual([{ kind: "text", text: "hello\nworld" }]);
  });
  it("extracts a fenced block with its language", () => {
    const p = splitFences("Run this:\n```sh\nls -la\n```\nDone.");
    expect(p).toEqual([
      { kind: "text", text: "Run this:\n" },
      { kind: "code", lang: "sh", text: "ls -la" },
      { kind: "text", text: "\nDone." },
    ]);
  });
  it("handles several blocks and no language", () => {
    const p = splitFences("```\na\n```\n```py\nb\n```");
    expect(p.filter((x) => x.kind === "code")).toHaveLength(2);
  });
  it("treats an unterminated fence (streaming) as code", () => {
    const p = splitFences("Try:\n```sh\nnpm inst");
    expect(p[1]).toEqual({ kind: "code", lang: "sh", text: "npm inst" });
  });
  it("keeps multi-line code intact", () => {
    const p = splitFences("```go\nfunc main() {\n\n}\n```");
    expect(p[0]).toEqual({ kind: "code", lang: "go", text: "func main() {\n\n}" });
  });
});
