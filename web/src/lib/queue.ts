// SPDX-License-Identifier: GPL-2.0-or-later
// Offline queue: messages typed while offline are kept (per user) and sent on
// reconnect. Stored in localStorage under a per-user key and cleared on logout.

export interface Queued {
  id: string;
  sessionId: string;
  text: string;
  at: number;
}

const key = (user: string) => `syx.outbox.${user}`;

export function loadQueue(user: string, store: Storage = localStorage): Queued[] {
  try {
    const v = JSON.parse(store.getItem(key(user)) ?? "[]");
    return Array.isArray(v) ? (v as Queued[]) : [];
  } catch {
    return [];
  }
}

export function saveQueue(user: string, q: Queued[], store: Storage = localStorage): void {
  if (q.length === 0) store.removeItem(key(user));
  else store.setItem(key(user), JSON.stringify(q));
}

export function enqueue(
  user: string,
  item: Omit<Queued, "id" | "at">,
  store: Storage = localStorage,
): Queued {
  const q = loadQueue(user, store);
  const entry: Queued = { ...item, id: Math.random().toString(36).slice(2), at: Date.now() };
  q.push(entry);
  saveQueue(user, q, store);
  return entry;
}

/** Sends queued items in order; stops at the first failure. Returns how many were sent. */
export async function flush(
  user: string,
  send: (q: Queued) => Promise<void>,
  store: Storage = localStorage,
): Promise<number> {
  const q = loadQueue(user, store);
  let sent = 0;
  while (q.length) {
    const head = q[0]!;
    try {
      await send(head);
    } catch {
      break;
    }
    q.shift();
    sent++;
    saveQueue(user, q, store);
  }
  return sent;
}

/** Removes every stored outbox and per-user cache (called on logout). */
export function clearUserData(store: Storage = localStorage): void {
  for (let i = store.length - 1; i >= 0; i--) {
    const k = store.key(i);
    if (k && k.startsWith("syx.outbox.")) store.removeItem(k);
  }
}
