// SPDX-License-Identifier: GPL-2.0-or-later

/** A file attached to a message. The server puts the list at the end of the message text. */
export interface Attachment {
  path: string;
  kind: "text" | "pdf" | "image";
  size: number;
}

const DIR = "/workspace/.attachments/";

/** Splits a stored message into what the person typed and the files attached to it. */
export function parseAttachments(content: string): { text: string; items: Attachment[] } {
  const trimmed = content.replace(/[\n ]+$/, "");
  if (!trimmed.endsWith("</attachments>")) return { text: content, items: [] };
  const open = trimmed.lastIndexOf("<attachments>");
  if (open < 0) return { text: content, items: [] };
  const body = trimmed.slice(
    open + "<attachments>".length,
    trimmed.length - "</attachments>".length,
  );
  const items: Attachment[] = [];
  for (const line of body.split("\n")) {
    const f = line.trim().split(" | ");
    if (f.length !== 3) continue;
    const path = f[0]!;
    // only the attachment folder counts, as on the server
    if (!path.startsWith(DIR) || path.includes("..")) continue;
    const kind = f[1];
    if (kind !== "text" && kind !== "pdf" && kind !== "image") continue;
    items.push({ path, kind, size: Number(f[2]) || 0 });
  }
  return { text: trimmed.slice(0, open).replace(/\n+$/, ""), items };
}

export const attachmentName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

export function fileSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** The picker filter for the kinds an admin allows (the server checks the real content). */
export function acceptFor(kinds: string[]): string {
  const by: Record<string, string> = {
    images: ".png,.jpg,.jpeg,.gif,.webp,.bmp,.tif,.tiff",
    pdf: ".pdf",
    documents: ".docx,.xlsx,.pptx,.odt,.ods,.odp",
    text: ".txt,.md,.csv,.tsv,.json,.log",
  };
  return kinds
    .map((k) => by[k])
    .filter(Boolean)
    .join(",");
}

export const MAX_UPLOAD = 10 * 1024 * 1024; // used only when the server does not say
export const MAX_ATTACHMENTS = 8;
