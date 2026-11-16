// SPDX-License-Identifier: GPL-2.0-or-later
import { isValidElement, type ReactElement, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import rehypeHighlight from "rehype-highlight";
import remarkGfm from "remark-gfm";
import { copyText } from "../lib/fences";
import { Icon } from "./ui";
import { useState } from "react";

function textOf(n: ReactNode): string {
  if (typeof n === "string" || typeof n === "number") return String(n);
  if (Array.isArray(n)) return n.map(textOf).join("");
  if (isValidElement(n))
    return textOf((n as ReactElement<{ children?: ReactNode }>).props.children);
  return "";
}

function Block({ children }: { children?: ReactNode }) {
  const [done, setDone] = useState(false);
  const code = isValidElement(children)
    ? (children as ReactElement<{ className?: string; children?: ReactNode }>)
    : null;
  const lang = /language-([\w+#.-]+)/.exec(code?.props.className ?? "")?.[1] ?? "";
  const text = textOf(code?.props.children ?? children).replace(/\n$/, "");
  return (
    <div className="codeblock">
      <div className="codeblock-bar">
        <span className="muted">{lang || "code"}</span>
        <button
          type="button"
          className="btn btn-ghost codeblock-copy"
          aria-label={done ? "Copied" : "Copy code"}
          onClick={async () => {
            if (await copyText(text)) {
              setDone(true);
              setTimeout(() => setDone(false), 1500);
            }
          }}
        >
          <Icon name={done ? "check" : "copy"} />
          <span>{done ? "Copied" : "Copy"}</span>
        </button>
      </div>
      <pre tabIndex={0}>{children}</pre>
    </div>
  );
}

const components: Components = {
  pre: ({ children }) => <Block>{children}</Block>,
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer nofollow">
      {children}
    </a>
  ),
  // pictures from a model's reply could be used to leak data to another site: show the text instead
  img: ({ alt }) => <span className="muted">[image: {alt ?? ""}]</span>,
  table: ({ children }) => (
    <div className="table-wrap" tabIndex={0} role="region" aria-label="Table">
      <table>{children}</table>
    </div>
  ),
};

/**
 * A model's reply rendered as Markdown: tables, task lists, syntax-highlighted code with a copy button.
 * Raw HTML is never rendered, links open in a new tab, and images are not loaded.
 */
export default function Markdown({ text }: { text: string }) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeHighlight, { detect: false, ignoreMissing: true }]]}
        components={components}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
