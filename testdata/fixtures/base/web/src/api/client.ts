// Thin HTTP client for the portal API. Every request goes through fetchJSON so
// auth headers, base URL and error mapping live in one place.

import { formatQuery } from "../utils/format";

export const API_BASE = "/api/v1";

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export interface User {
  id: string;
  email: string;
  displayName: string;
  teamId: string | null;
  createdAt: string;
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  query?: Record<string, string | number | boolean | undefined>;
  body?: unknown;
  signal?: AbortSignal;
}

function authHeader(): Record<string, string> {
  const token = window.localStorage.getItem("portal.token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export async function fetchJSON<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const url = API_BASE + path + formatQuery(opts.query ?? {});
  const res = await fetch(url, {
    method: opts.method ?? "GET",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      ...authHeader(),
    },
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    signal: opts.signal,
  });
  if (!res.ok) {
    let code = "unknown";
    let message = res.statusText;
    try {
      const payload = (await res.json()) as { code?: string; message?: string };
      code = payload.code ?? code;
      message = payload.message ?? message;
    } catch {
      // body was not JSON; keep the status text
    }
    throw new ApiError(res.status, code, message);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

export async function getUser(id: string): Promise<User> {
  return fetchJSON<User>(`/users/${encodeURIComponent(id)}`);
}

export async function getCurrentUser(): Promise<User> {
  return fetchJSON<User>("/users/me");
}

export async function updateUser(id: string, patch: Partial<Pick<User, "displayName" | "teamId">>): Promise<User> {
  return fetchJSON<User>(`/users/${encodeURIComponent(id)}`, { method: "PATCH", body: patch });
}
