// SPDX-License-Identifier: GPL-2.0-or-later
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { get, post, errText } from "../lib/api";
import { Banner, Button, Skeleton, StatusPill, useToast } from "../ui/ui";

const DiffView = lazy(() => import("../ui/DiffView"));

export interface Capacity {
  recommended: number;
  max: number;
  limits: string[] | null;
  model_loaded?: boolean;
}

interface Attempt {
  id: string;
  title: string;
  state: string;
}
interface Changes {
  available: boolean;
  text?: string;
  reason?: string;
}

function AttemptCard({
  a,
  onOpen,
  onKeep,
}: {
  a: Attempt;
  onOpen: () => void;
  onKeep: () => void;
}) {
  const [ch, setCh] = useState<Changes | null>(null);
  const settled = a.state !== "running";
  useEffect(() => {
    if (!settled) return;
    let live = true;
    get<Changes>(`/sessions/${a.id}/diff`)
      .then((c) => live && setCh(c))
      .catch(() => live && setCh({ available: false, reason: "Could not read the changes." }));
    return () => {
      live = false;
    };
  }, [a.id, settled]);
  return (
    <section className="card" aria-label={a.title} style={{ minWidth: 280, flex: "1 1 320px" }}>
      <header style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <strong style={{ flex: 1 }}>{a.title}</strong>
        <StatusPill state={a.state} />
      </header>
      {!settled ? (
        <Skeleton lines={3} />
      ) : ch === null ? (
        <Skeleton lines={3} />
      ) : ch.available ? (
        <Suspense fallback={<Skeleton lines={3} />}>
          <DiffView text={ch.text ?? ""} />
        </Suspense>
      ) : (
        <p>{ch.reason}</p>
      )}
      <footer style={{ display: "flex", gap: 8, marginTop: 8 }}>
        <Button onClick={onOpen}>Open</Button>
        <Button variant="primary" onClick={onKeep} disabled={!settled}>
          Keep this one
        </Button>
      </footer>
    </section>
  );
}

/** The attempts of one batch side by side, so the best can be kept and the rest removed. */
export function Compare({
  batch,
  onOpen,
  onDone,
}: {
  batch: string;
  onOpen: (id: string) => void;
  onDone: (kept: string) => void;
}) {
  const toast = useToast();
  const [items, setItems] = useState<Attempt[] | null>(null);
  const load = useCallback(async () => {
    try {
      setItems((await get<{ sessions: Attempt[] }>(`/parallel/${batch}`)).sessions);
    } catch (x) {
      toast(errText(x), "error");
    }
  }, [batch, toast]);
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), 4000);
    return () => clearInterval(t);
  }, [load]);
  async function keep(id: string) {
    try {
      await post(`/parallel/${batch}/keep`, { session: id });
      onDone(id);
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (!items) return <Skeleton lines={4} />;
  return (
    <div style={{ padding: 16 }}>
      <h2>Compare attempts</h2>
      <Banner tone="info">
        Each attempt worked in its own copy. Keeping one removes the others.
      </Banner>
      <div style={{ display: "flex", gap: 12, flexWrap: "wrap", marginTop: 12 }}>
        {items.map((a) => (
          <AttemptCard
            key={a.id}
            a={a}
            onOpen={() => onOpen(a.id)}
            onKeep={() => void keep(a.id)}
          />
        ))}
      </div>
    </div>
  );
}
