/* SPDX-License-Identifier: GPL-2.0-or-later */
// Service worker: app-shell cache + offline fallback + push. It NEVER caches
// /api responses (they contain user data). Cache names start with "syx-user-"
// so the app can delete them on logout.
const VERSION = "v1";
const SHELL = "syx-user-shell-" + VERSION;
const ASSETS = "syx-user-assets-" + VERSION;
const SHELL_URLS = ["/", "/offline.html", "/manifest.webmanifest", "/icon.svg", "/icon-192.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(SHELL).then((c) => c.addAll(SHELL_URLS)));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      const keep = new Set([SHELL, ASSETS]);
      for (const k of await caches.keys()) if (k.startsWith("syx-user-") && !keep.has(k)) await caches.delete(k);
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/preview/")) return; // never cache user data

  if (req.mode === "navigate") {
    event.respondWith(
      fetch(req).catch(async () => (await caches.match("/")) || (await caches.match("/offline.html"))),
    );
    return;
  }
  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(
      caches.open(ASSETS).then(async (cache) => {
        const hit = await cache.match(req);
        if (hit) return hit;
        const res = await fetch(req);
        if (res.ok) cache.put(req, res.clone());
        return res;
      }),
    );
    return;
  }
  event.respondWith(caches.match(req).then((hit) => hit || fetch(req)));
});

// Background Sync (where supported): tell open pages to flush their outbox.
self.addEventListener("sync", (event) => {
  if (event.tag === "syx-outbox") {
    event.waitUntil(self.clients.matchAll().then((cs) => cs.forEach((c) => c.postMessage({ type: "FLUSH_OUTBOX" }))));
  }
});

self.addEventListener("push", (event) => {
  let data = { title: "Syntaxis", body: "" };
  try {
    data = { ...data, ...event.data.json() };
  } catch {
    /* plain text or empty */
  }
  event.waitUntil(self.registration.showNotification(data.title, { body: data.body, icon: "/icon-192.png", badge: "/icon-192.png", tag: "syntaxis" }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: "window" }).then((cs) => (cs[0] ? cs[0].focus() : self.clients.openWindow("/"))),
  );
});
