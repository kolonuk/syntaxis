// SPDX-License-Identifier: GPL-2.0-or-later

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

let csrfToken = "";
export const setCsrf = (t: string) => {
  csrfToken = t;
};
export const getCsrf = () => csrfToken;

type Fetch = typeof fetch;
let fetchImpl: Fetch = (...a) => fetch(...a);
/** Test hook. */
export const _setFetch = (f: Fetch) => {
  fetchImpl = f;
};

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (csrfToken && method !== "GET") headers["X-CSRF-Token"] = csrfToken;
  const res = await fetchImpl("/api" + path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg =
      data && typeof data === "object" && "error" in data
        ? String((data as { error: unknown }).error)
        : `Request failed (${res.status})`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const get = <T>(p: string) => api<T>("GET", p);
export const post = <T>(p: string, b?: unknown) => api<T>("POST", p, b ?? {});
export const put = <T>(p: string, b?: unknown) => api<T>("PUT", p, b ?? {});
export const patch = <T>(p: string, b?: unknown) => api<T>("PATCH", p, b ?? {});
export const del = <T>(p: string) => api<T>("DELETE", p);

/** Friendly text for an unknown error. */
export function errText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.message === "password_expired") return "Your password has expired. Please change it.";
    if (e.message === "2fa_required") return "Two-factor authentication must be set up first.";
    return e.message;
  }
  return e instanceof Error ? e.message : "Something went wrong";
}
