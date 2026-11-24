// SPDX-License-Identifier: GPL-2.0-or-later
// Service worker registration and the update prompt. There are no push notifications.

export function registerPWA(): void {
  if (!("serviceWorker" in navigator) || !window.isSecureContext) return;
  window.addEventListener("load", () => {
    navigator.serviceWorker
      .register("/sw.js")
      .then((reg) => {
        reg.addEventListener("updatefound", () => {
          const w = reg.installing;
          w?.addEventListener("statechange", () => {
            if (w.state === "installed" && navigator.serviceWorker.controller) showUpdate(reg);
          });
        });
      })
      .catch(() => undefined);
  });
  // Background Sync is a progressive enhancement; the in-app queue flushes on "online" regardless.
}

function showUpdate(reg: ServiceWorkerRegistration): void {
  const bar = document.createElement("div");
  bar.setAttribute("role", "status");
  bar.style.cssText =
    "position:fixed;left:12px;right:12px;bottom:12px;padding:12px;background:var(--surface);border:1px solid var(--border);border-radius:10px;z-index:100;display:flex;gap:12px;align-items:center";
  const t = document.createElement("span");
  t.textContent = "A new version of Syntaxis is available.";
  const b = document.createElement("button");
  b.textContent = "Reload";
  b.className = "btn btn-primary";
  b.onclick = () => {
    reg.waiting?.postMessage({ type: "SKIP_WAITING" });
    navigator.serviceWorker.addEventListener("controllerchange", () => window.location.reload(), {
      once: true,
    });
  };
  bar.append(t, b);
  document.body.append(bar);
}
