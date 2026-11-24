// SPDX-License-Identifier: GPL-2.0-or-later
import { Badge, Banner, Button, Card, Skeleton, useToast } from "../ui/ui";
import { copyText } from "../lib/fences";
import { DOCS, Table, useLoad } from "./shared";

interface AuditData {
  entries: Record<string, unknown>[];
  chain_ok: boolean;
  first_bad: number;
  head: { count: number; seq: number; hash: string };
  total: number;
  genesis: string;
}

/**
 * The audit log is a chain: every entry carries a fingerprint (SHA-256) of its own content and of
 * the entry before it. This tab says what "verified" means, shows the evidence, and how to check it
 * somewhere that does not have to trust this server.
 */
export function AuditTab() {
  const toast = useToast();
  const { data, err } = useLoad<AuditData>("/audit");
  if (err) return <Banner tone="danger">{err}</Banner>;
  if (!data) return <Skeleton />;
  const shown = data.entries.length;
  return (
    <div className="stack">
      <Banner tone={data.chain_ok ? "info" : "danger"}>
        {data.chain_ok ? (
          <>
            <strong>Verified.</strong> Syntaxis just re-computed the fingerprint of all {data.total}{" "}
            entries, from the first one, and every one matched.
          </>
        ) : (
          <>
            <strong>The audit log has been changed.</strong> Entry {data.first_bad + 1} does not
            match its fingerprint, so something at or before it was edited, removed or inserted.
            Restore from a backup and find out who has write access to the database.
          </>
        )}
      </Banner>
      <Card title="What this means">
        <p>
          Every entry stores a fingerprint that covers its own content{" "}
          <em>and the previous entry's fingerprint</em>. Change, delete or insert an entry anywhere
          and every fingerprint after it stops matching, so the log cannot be edited quietly.
          &quot;Verified&quot; means all of them matched.
        </p>
        <p className="muted">
          It does not stop someone with write access to the database from rewriting the whole log
          from one point onward with fresh fingerprints, or from cutting entries off the end. To
          catch that, keep the latest fingerprint below somewhere else (a note, a ticket, another
          machine). If a later check shows a different fingerprint at the same entry number, the log
          was rewritten.{" "}
          <a href={`${DOCS}/security.md#audit-log`} target="_blank" rel="noreferrer">
            How the audit log works
          </a>
          .
        </p>
      </Card>
      <Card title="Proof you can check yourself">
        <p>
          Latest entry: <strong>#{data.head.seq}</strong> of {data.head.count}{" "}
          <Badge tone={data.chain_ok ? "success" : "danger"}>
            {data.chain_ok ? "chain intact" : "chain broken"}
          </Badge>
        </p>
        <p>
          Fingerprint: <code aria-label="Fingerprint of the latest entry">{data.head.hash}</code>{" "}
          <Button
            onClick={async () =>
              toast((await copyText(data.head.hash)) ? "Copied." : "Could not copy.", "info")
            }
          >
            Copy
          </Button>
        </p>
        <p className="muted">
          To check the whole log on another machine, without trusting this server&apos;s answer:
          download it, then run the check. It recomputes every fingerprint and prints the length and
          the latest fingerprint to compare with the one above.
        </p>
        <p>
          <a className="btn" href="/api/audit/export" download="syntaxis-audit.jsonl">
            Download the log
          </a>
        </p>
        <pre className="output" aria-label="Command to verify the downloaded log">
          docker compose exec -T syntaxis syntaxis audit verify - &lt; syntaxis-audit.jsonl
        </pre>
      </Card>
      <p className="muted">
        {shown < data.total
          ? `The newest ${shown} of ${data.total} entries. The download has all of them.`
          : `All ${data.total} entries.`}
      </p>
      <Table
        rows={[...data.entries].reverse()}
        cols={["seq", "time", "actor", "action", "target", "detail"]}
      />
    </div>
  );
}
