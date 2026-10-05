// Client for accounts: sign-in, the watchlist, in-app alert subscriptions and the inbox.
//
// Everything goes to the same origin (/api/v1/*, forwarded to the gateway by proxy.ts), so the
// browser's session cookie travels with each call. That cookie is HttpOnly: nothing here, or
// anywhere in the page, can read the session token.

import { API_BASE, type ApiRead, type BackendFailure } from "./backend";
import type { HeatRiskLevel } from "./types";

type Fetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

// ---- wire types ----

export interface AccountUser {
  id: number;
  email: string;
  displayName: string;
  role: "user" | "admin";
  disabled: boolean;
  createdAt: string;
  lastLoginAt?: string;
}

export interface WatchedCity {
  locationId: number;
  name: string;
  country: string;
  admin1?: string;
  latitude: number;
  longitude: number;
  timezone: string;
  addedAt: string;
}

/** What a user wants in-app alerts for: one city, or every city when locationId is null. */
export interface MySubscription {
  id: number;
  locationId: number | null;
  minLevel: HeatRiskLevel;
  channel: "inapp";
  active: boolean;
  createdAt: string;
}

export interface InboxItem {
  id: number;
  kind: "opened" | "escalated" | "resolved";
  level: HeatRiskLevel;
  createdAt: string;
  readAt?: string;
  alertId: number;
  locationId: number;
  locationName: string;
  headline: string;
  summary: string;
}

export interface Inbox {
  unread: number;
  notifications: InboxItem[];
}

// ---- results ----

export interface ApiError {
  /** HTTP status; 0 when the server could not be reached. */
  status: number;
  /** The gateway's error code (e.g. "invalid_credentials"), or "network", "timeout", "not_available". */
  code: string;
  message: string;
  retryAfterSeconds?: number;
}

export type Result<T> = { ok: true; data: T } | { ok: false; error: ApiError };

export interface CallOptions {
  fetchImpl?: Fetch;
  signal?: AbortSignal;
  timeoutMs?: number;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** One JSON call. Never throws: failures come back as an ApiError (an aborted call is reported as "aborted"). */
export async function callApi<T>(method: string, path: string, body?: unknown, options: CallOptions = {}): Promise<Result<T>> {
  const doFetch: Fetch = options.fetchImpl ?? ((input, init) => fetch(input, init));
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), options.timeoutMs ?? 10_000);
  const onAbort = () => controller.abort();
  options.signal?.addEventListener("abort", onAbort);
  try {
    const res = await doFetch(API_BASE + path, {
      method,
      credentials: "same-origin",
      signal: controller.signal,
      headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let parsed: unknown = null;
    try {
      parsed = await res.json();
    } catch {
      // 204, or not JSON (an HTML error page from a proxy)
    }
    if (res.status >= 200 && res.status < 300) return { ok: true, data: parsed as T };

    const err = isRecord(parsed) && isRecord(parsed.error) ? parsed.error : null;
    const retry = Number(res.headers.get("Retry-After"));
    return {
      ok: false,
      error: {
        status: res.status,
        code: typeof err?.code === "string" ? err.code : "not_available",
        message: typeof err?.message === "string" ? err.message : "",
        retryAfterSeconds: Number.isFinite(retry) && retry > 0 ? retry : undefined,
      },
    };
  } catch {
    if (options.signal?.aborted) return { ok: false, error: { status: 0, code: "aborted", message: "" } };
    return controller.signal.aborted
      ? { ok: false, error: { status: 0, code: "timeout", message: "" } }
      : { ok: false, error: { status: 0, code: "network", message: "" } };
  } finally {
    clearTimeout(timer);
    options.signal?.removeEventListener("abort", onAbort);
  }
}

/** True when this deployment has no backend, so there are no accounts to offer. */
export function accountsUnavailable(error: ApiError): boolean {
  return error.code === "backend_not_configured" || (error.code === "not_available" && error.status === 404);
}

/** An account call's result in the shape the polling hook wants (see usePolling). */
export function asRead<T>(res: Result<T>): ApiRead<T> {
  if (res.ok) return { ok: true, data: res.data };
  const { status, code } = res.error;
  let reason: BackendFailure = "unavailable";
  if (accountsUnavailable(res.error)) reason = "disabled";
  else if (status === 401) reason = "unauthorized";
  else if (status === 429) reason = "rate_limited";
  else if (code === "timeout" || status === 504) reason = "timeout";
  else if (status > 0 && status < 500) reason = "bad_response";
  return { ok: false, reason };
}

/** What to tell the person, in plain words. */
export function describeError(error: ApiError): string {
  switch (error.code) {
    case "invalid_credentials":
      return "That email and password do not match an account.";
    case "email_taken":
      return "An account with this email already exists. Try signing in instead.";
    case "account_disabled":
      return "This account has been disabled. Please contact an administrator.";
    case "too_many_attempts": {
      const minutes = error.retryAfterSeconds ? Math.max(1, Math.ceil(error.retryAfterSeconds / 60)) : null;
      return `Too many failed attempts. ${minutes ? `Try again in about ${minutes} minute${minutes === 1 ? "" : "s"}.` : "Try again later."}`;
    }
    case "rate_limited":
      return "You are doing that too often. Wait a moment and try again.";
    case "unauthorized":
      return "Your session has ended. Please sign in again.";
    case "cross_site_request":
      return "That request was blocked because it came from another site.";
    case "validation_failed":
    case "conflict":
    case "subscription_limit":
      return error.message || "That could not be saved.";
    case "network":
      return "Could not reach the server. Check your connection and try again.";
    case "timeout":
      return "The server took too long to answer. Please try again.";
    case "backend_not_configured":
    case "not_available":
      return "Accounts are not available on this deployment.";
    default:
      return error.status >= 500 ? "The server had a problem. Please try again shortly." : error.message || "Something went wrong. Please try again.";
  }
}

// ---- password and redirect helpers ----

export const MIN_PASSWORD_LENGTH = 10;
export const MAX_PASSWORD_LENGTH = 128;

/** An instant hint for the form; the server makes the real decision (it also refuses common passwords). */
export function passwordProblem(password: string): string | null {
  const length = [...password].length;
  if (length < MIN_PASSWORD_LENGTH) return `Use at least ${MIN_PASSWORD_LENGTH} characters.`;
  if (length > MAX_PASSWORD_LENGTH) return `Use at most ${MAX_PASSWORD_LENGTH} characters.`;
  return null;
}

/** Where to go after signing in: only a path on this site (never another origin) from ?next=. */
export function safeNextPath(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\") || /[\u0000-\u001f]/.test(raw)) return "/";
  return raw;
}

// ---- session ----

export const fetchSession = (options?: CallOptions) => callApi<{ user: AccountUser | null }>("GET", "/auth/session", undefined, options);

export const register = (input: { email: string; password: string; displayName: string }, options?: CallOptions) =>
  callApi<{ user: AccountUser }>("POST", "/auth/register", input, options);

export const login = (input: { email: string; password: string }, options?: CallOptions) =>
  callApi<{ user: AccountUser }>("POST", "/auth/login", input, options);

export const logout = (options?: CallOptions) => callApi<void>("POST", "/auth/logout", undefined, options);

export const changePassword = (input: { current: string; new: string }, options?: CallOptions) =>
  callApi<{ user: AccountUser }>("POST", "/auth/password", input, options);

export const deleteAccount = (password: string, options?: CallOptions) => callApi<void>("DELETE", "/auth/account", { password }, options);

// ---- watchlist and alerts ----

export const fetchWatchlist = (options?: CallOptions) => callApi<{ cities: WatchedCity[] }>("GET", "/me/watchlist", undefined, options);

export const saveCity = (city: Omit<WatchedCity, "addedAt">, options?: CallOptions) => {
  const { locationId, ...rest } = city;
  return callApi<void>("PUT", `/me/watchlist/${locationId}`, rest, options);
};

export const removeCity = (locationId: number, options?: CallOptions) => callApi<void>("DELETE", `/me/watchlist/${locationId}`, undefined, options);

export const fetchSubscriptions = (options?: CallOptions) =>
  callApi<{ subscriptions: MySubscription[] }>("GET", "/me/subscriptions", undefined, options);

export const followLocation = (locationId: number | null, minLevel: HeatRiskLevel, options?: CallOptions) =>
  callApi<MySubscription>("POST", "/me/subscriptions", { locationId, minLevel }, options);

export const unfollow = (subscriptionId: number, options?: CallOptions) => callApi<void>("DELETE", `/me/subscriptions/${subscriptionId}`, undefined, options);

// ---- inbox ----

export const fetchInbox = (limit = 30, options?: CallOptions) => callApi<Inbox>("GET", `/me/notifications?limit=${limit}`, undefined, options);

/** Fired on the window when the inbox changes, so the bell in the header updates at once. */
export const INBOX_CHANGED = "heatwave:inbox-changed";

async function announce<T>(res: Promise<Result<T>>): Promise<Result<T>> {
  const done = await res;
  if (done.ok && typeof window !== "undefined") window.dispatchEvent(new Event(INBOX_CHANGED));
  return done;
}

export const markRead = (id: number, options?: CallOptions) => announce(callApi<void>("POST", `/me/notifications/${id}/read`, undefined, options));

export const markAllRead = (options?: CallOptions) => announce(callApi<void>("POST", "/me/notifications/read", undefined, options));

// ---- administration ----

export const fetchUsers = (options?: CallOptions) => callApi<{ users: AccountUser[] }>("GET", "/admin/users", undefined, options);

export const setUserDisabled = (id: number, disabled: boolean, options?: CallOptions) =>
  callApi<AccountUser>("POST", `/admin/users/${id}/${disabled ? "disable" : "enable"}`, undefined, options);
