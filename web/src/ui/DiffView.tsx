// SPDX-License-Identifier: GPL-2.0-or-later
import { useMemo } from "react";
import { Diff, Hunk, parseDiff } from "react-diff-view";
import "react-diff-view/style/index.css";

/** One file's unified diff (the part from "diff --git" on), drawn with the app's own colours. */
export default function DiffView({ text }: { text: string }) {
  const files = useMemo(() => {
    try {
      return parseDiff(text, { nearbySequences: "zip" });
    } catch {
      return [];
    }
  }, [text]);
  if (files.length === 0) return <pre className="diff">{text}</pre>;
  return (
    <div className="diffview">
      {files.map((f, i) => (
        <Diff key={i} viewType="unified" diffType={f.type} hunks={f.hunks}>
          {(hunks) => hunks.map((h) => <Hunk key={h.content} hunk={h} />)}
        </Diff>
      ))}
    </div>
  );
}
