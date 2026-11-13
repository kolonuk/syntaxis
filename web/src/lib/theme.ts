// SPDX-License-Identifier: GPL-2.0-or-later

export type ThemePref = "system" | "light" | "dark";
const KEY = "syx.theme";

export function resolveTheme(pref: ThemePref, systemDark: boolean): "light" | "dark" {
  if (pref === "system") return systemDark ? "dark" : "light";
  return pref;
}

export function loadPref(store: Storage = localStorage): ThemePref {
  const v = store.getItem(KEY);
  return v === "light" || v === "dark" ? v : "system";
}

export function applyTheme(
  pref: ThemePref,
  doc: Document = document,
  store: Storage = localStorage,
): void {
  const dark =
    typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches;
  doc.documentElement.dataset.theme = resolveTheme(pref, dark);
  if (pref === "system") store.removeItem(KEY);
  else store.setItem(KEY, pref);
}

export function nextPref(p: ThemePref): ThemePref {
  return p === "system" ? "light" : p === "light" ? "dark" : "system";
}
