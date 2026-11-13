// SPDX-License-Identifier: GPL-2.0-or-later
import { useCallback, useEffect, useState } from "react";
import { get, setCsrf } from "../lib/api";

export interface Me {
  kind: string;
  name: string;
  email: string;
  totp_enabled: boolean;
  must_change_password: boolean;
  password_days_left: number;
  csrf: string;
  remind_2fa: boolean;
  banners?: string[];
}

/** Current user; null = signed out; undefined = loading. */
export function useMe(): {
  me: Me | null | undefined;
  reload: () => Promise<void>;
  clear: () => void;
} {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const reload = useCallback(async () => {
    try {
      const m = await get<Me>("/me");
      setCsrf(m.csrf);
      setMe(m);
    } catch {
      setMe(null);
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);
  return { me, reload, clear: () => setMe(null) };
}

/** Minimal client router over the History API. */
export function useRoute(): [string, (to: string) => void] {
  const [path, setPath] = useState(window.location.pathname + window.location.search);
  useEffect(() => {
    const on = () => setPath(window.location.pathname + window.location.search);
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  const go = useCallback((to: string) => {
    window.history.pushState({}, "", to);
    setPath(to);
  }, []);
  return [path, go];
}
