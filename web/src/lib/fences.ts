// SPDX-License-Identifier: GPL-2.0-or-later

export type Part = { kind: "text"; text: string } | { kind: "code"; lang: string; text: string };

/**
 * Splits a message into prose and fenced code blocks. An unterminated fence
 * (a reply that is still streaming) is treated as code so it does not flicker.
 */
export function splitFences(src: string): Part[] {
  const parts: Part[] = [];
  const re = /^```[ \t]*([\w+#.-]*)[^\n]*\n([\s\S]*?)(?:\n?```[ \t]*$|$(?![\s\S]))/gm;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src)) !== null) {
    if (m.index > last) parts.push({ kind: "text", text: src.slice(last, m.index) });
    parts.push({ kind: "code", lang: m[1] ?? "", text: m[2] ?? "" });
    last = m.index + m[0].length;
    if (m[0].length === 0) re.lastIndex++;
  }
  if (last < src.length) parts.push({ kind: "text", text: src.slice(last) });
  return parts.filter((p) => p.kind === "code" || p.text.trim() !== "");
}

/** Copies text; falls back for plain-HTTP origins where the Clipboard API is unavailable. */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to the legacy path */
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}
