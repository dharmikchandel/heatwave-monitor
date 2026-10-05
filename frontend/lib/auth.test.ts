import { afterEach, describe, expect, test } from "bun:test";
import {
  accountsUnavailable,
  asRead,
  callApi,
  changePassword,
  deleteAccount,
  describeError,
  fetchInbox,
  fetchSession,
  fetchUsers,
  fetchWatchlist,
  followLocation,
  INBOX_CHANGED,
  login,
  logout,
  markAllRead,
  markRead,
  passwordProblem,
  register,
  removeCity,
  safeNextPath,
  saveCity,
  setUserDisabled,
  unfollow,
  type ApiError,
} from "./auth";

interface Seen {
  url: string;
  method?: string;
  body?: string;
  credentials?: RequestCredentials;
  headers: Headers;
}

/** A fetch that records each request and answers with the scripted reply. */
function scripted(status: number, body?: unknown, headers: Record<string, string> = {}) {
  const seen: Seen[] = [];
  const fetchImpl = async (input: RequestInfo | URL, init?: RequestInit) => {
    seen.push({ url: String(input), method: init?.method, body: init?.body as string | undefined, credentials: init?.credentials, headers: new Headers(init?.headers) });
    return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
  };
  return { fetchImpl, seen };
}

const fail = (status: number, code: string, message = "", extra: Record<string, string> = {}) => scripted(status, { error: { code, message } }, extra);

describe("callApi", () => {
  test("sends the session cookie to our own origin, and a JSON body only when there is one", async () => {
    const { fetchImpl, seen } = scripted(200, { ok: 1 });
    await callApi("GET", "/x", undefined, { fetchImpl });
    await callApi("POST", "/y", { a: 1 }, { fetchImpl });
    expect(seen[0]).toMatchObject({ url: "/api/v1/x", method: "GET", credentials: "same-origin", body: undefined });
    expect(seen[0].headers.get("Content-Type")).toBeNull();
    expect(seen[1]).toMatchObject({ url: "/api/v1/y", method: "POST", body: '{"a":1}' });
    expect(seen[1].headers.get("Content-Type")).toBe("application/json");
    expect(seen[1].headers.get("Authorization")).toBeNull(); // the token is in an HttpOnly cookie; scripts never handle it
  });

  test("returns the body of a success, and nothing for a 204", async () => {
    expect(await callApi("GET", "/x", undefined, { fetchImpl: scripted(200, { n: 5 }).fetchImpl })).toEqual({ ok: true, data: { n: 5 } });
    const none = await callApi("DELETE", "/x", undefined, { fetchImpl: async () => new Response(null, { status: 204 }) });
    expect(none.ok).toBe(true);
  });

  test("turns the gateway's error shape into an ApiError, with Retry-After", async () => {
    const res = await callApi("POST", "/auth/login", {}, { fetchImpl: fail(429, "too_many_attempts", "locked", { "Retry-After": "840" }).fetchImpl });
    expect(res).toEqual({ ok: false, error: { status: 429, code: "too_many_attempts", message: "locked", retryAfterSeconds: 840 } });
  });

  test("an answer that is not our error shape is 'not_available'; an unreachable server is 'network'", async () => {
    const html = await callApi("GET", "/x", undefined, { fetchImpl: async () => new Response("<html>404</html>", { status: 404 }) });
    expect(html).toMatchObject({ ok: false, error: { status: 404, code: "not_available" } });
    const down = await callApi("GET", "/x", undefined, { fetchImpl: async () => Promise.reject(new TypeError("failed to fetch")) });
    expect(down).toMatchObject({ ok: false, error: { status: 0, code: "network" } });
  });

  test("a call that takes too long is a 'timeout'; one the caller abandons is 'aborted'", async () => {
    const never = (_i: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_res, rej) => init?.signal?.addEventListener("abort", () => rej(new DOMException("x", "AbortError"))));
    expect(await callApi("GET", "/x", undefined, { fetchImpl: never, timeoutMs: 15 })).toMatchObject({ error: { code: "timeout" } });
    const controller = new AbortController();
    const pending = callApi("GET", "/x", undefined, { fetchImpl: never, signal: controller.signal });
    controller.abort();
    expect(await pending).toMatchObject({ error: { code: "aborted" } });
  });

  test("tells a deployment without a backend from a broken one", () => {
    const e = (status: number, code: string): ApiError => ({ status, code, message: "" });
    expect(accountsUnavailable(e(503, "backend_not_configured"))).toBe(true);
    expect(accountsUnavailable(e(404, "not_available"))).toBe(true); // an HTML 404: nothing serves /api/v1
    expect(accountsUnavailable(e(502, "upstream_unavailable"))).toBe(false);
    expect(accountsUnavailable(e(0, "network"))).toBe(false);
    expect(accountsUnavailable(e(401, "unauthorized"))).toBe(false);
  });
});

describe("the account endpoints", () => {
  const cases: { name: string; call: (o: { fetchImpl: ReturnType<typeof scripted>["fetchImpl"] }) => Promise<unknown>; method: string; url: string; body?: unknown }[] = [
    { name: "session", call: (o) => fetchSession(o), method: "GET", url: "/api/v1/auth/session" },
    { name: "register", call: (o) => register({ email: "a@b.co", password: "pw", displayName: "A" }, o), method: "POST", url: "/api/v1/auth/register", body: { email: "a@b.co", password: "pw", displayName: "A" } },
    { name: "login", call: (o) => login({ email: "a@b.co", password: "pw" }, o), method: "POST", url: "/api/v1/auth/login", body: { email: "a@b.co", password: "pw" } },
    { name: "logout", call: (o) => logout(o), method: "POST", url: "/api/v1/auth/logout" },
    { name: "change password", call: (o) => changePassword({ current: "a", new: "b" }, o), method: "POST", url: "/api/v1/auth/password", body: { current: "a", new: "b" } },
    { name: "delete account", call: (o) => deleteAccount("pw", o), method: "DELETE", url: "/api/v1/auth/account", body: { password: "pw" } },
    { name: "watchlist", call: (o) => fetchWatchlist(o), method: "GET", url: "/api/v1/me/watchlist" },
    {
      name: "save city",
      call: (o) => saveCity({ locationId: 7, name: "Mumbai", country: "India", latitude: 19, longitude: 72, timezone: "Asia/Kolkata" }, o),
      method: "PUT",
      url: "/api/v1/me/watchlist/7",
      body: { name: "Mumbai", country: "India", latitude: 19, longitude: 72, timezone: "Asia/Kolkata" }, // the id is in the path, not the body
    },
    { name: "remove city", call: (o) => removeCity(7, o), method: "DELETE", url: "/api/v1/me/watchlist/7" },
    { name: "follow", call: (o) => followLocation(7, "danger", o), method: "POST", url: "/api/v1/me/subscriptions", body: { locationId: 7, minLevel: "danger" } },
    { name: "unfollow", call: (o) => unfollow(3, o), method: "DELETE", url: "/api/v1/me/subscriptions/3" },
    { name: "inbox", call: (o) => fetchInbox(20, o), method: "GET", url: "/api/v1/me/notifications?limit=20" },
    { name: "mark read", call: (o) => markRead(9, o), method: "POST", url: "/api/v1/me/notifications/9/read" },
    { name: "mark all read", call: (o) => markAllRead(o), method: "POST", url: "/api/v1/me/notifications/read" },
    { name: "list users", call: (o) => fetchUsers(o), method: "GET", url: "/api/v1/admin/users" },
    { name: "disable", call: (o) => setUserDisabled(4, true, o), method: "POST", url: "/api/v1/admin/users/4/disable" },
    { name: "enable", call: (o) => setUserDisabled(4, false, o), method: "POST", url: "/api/v1/admin/users/4/enable" },
  ];
  for (const c of cases) {
    test(`${c.name}: ${c.method} ${c.url}`, async () => {
      const { fetchImpl, seen } = scripted(200, {});
      await c.call({ fetchImpl });
      expect(seen).toHaveLength(1);
      expect(seen[0].method).toBe(c.method);
      expect(seen[0].url).toBe(c.url);
      expect(seen[0].body === undefined ? undefined : JSON.parse(seen[0].body)).toEqual(c.body);
    });
  }

  test("following 'every city' sends a null location", async () => {
    const { fetchImpl, seen } = scripted(200, {});
    await followLocation(null, "extreme-danger", { fetchImpl });
    expect(JSON.parse(seen[0].body!)).toEqual({ locationId: null, minLevel: "extreme-danger" });
  });
});

describe("inbox changes are announced so the bell updates", () => {
  const original = (globalThis as { window?: unknown }).window;
  afterEach(() => {
    (globalThis as { window?: unknown }).window = original;
  });

  test("only when the change succeeded", async () => {
    const target = new EventTarget();
    (globalThis as { window?: unknown }).window = target;
    let heard = 0;
    target.addEventListener(INBOX_CHANGED, () => heard++);

    await markRead(1, { fetchImpl: scripted(204).fetchImpl });
    await markAllRead({ fetchImpl: scripted(204).fetchImpl });
    expect(heard).toBe(2);
    await markRead(1, { fetchImpl: fail(404, "not_found").fetchImpl });
    await markAllRead({ fetchImpl: fail(401, "unauthorized").fetchImpl });
    expect(heard).toBe(2);
  });
});

describe("describeError", () => {
  const e = (code: string, extra: Partial<ApiError> = {}): ApiError => ({ status: 400, code, message: "", ...extra });

  test("known codes get their own plain-language message", () => {
    const codes = ["invalid_credentials", "email_taken", "account_disabled", "too_many_attempts", "rate_limited", "unauthorized", "cross_site_request", "network", "timeout", "backend_not_configured"];
    const texts = new Set(codes.map((c) => describeError(e(c))));
    expect(texts.size).toBe(codes.length);
    for (const t of texts) expect(t.length).toBeGreaterThan(15);
  });

  test("sign-in failures never say which half was wrong", () => {
    expect(describeError(e("invalid_credentials"))).toBe("That email and password do not match an account.");
  });

  test("a lock-out says how long, in whole minutes, singular or plural", () => {
    expect(describeError(e("too_many_attempts", { retryAfterSeconds: 840 }))).toContain("14 minutes");
    expect(describeError(e("too_many_attempts", { retryAfterSeconds: 841 }))).toContain("15 minutes"); // never promise a shorter wait than it is
    expect(describeError(e("too_many_attempts", { retryAfterSeconds: 30 }))).toContain("1 minute.");
    expect(describeError(e("too_many_attempts"))).toContain("later");
  });

  test("the server's own validation wording is shown, with a fallback", () => {
    expect(describeError(e("validation_failed", { message: "password must be at least 10 characters" }))).toBe("password must be at least 10 characters");
    expect(describeError(e("validation_failed"))).toBe("That could not be saved.");
    expect(describeError(e("conflict", { message: "watchlist is full" }))).toBe("watchlist is full");
  });

  test("an unexpected server error is not echoed raw", () => {
    expect(describeError({ status: 500, code: "internal_error", message: "sql: boom" })).not.toContain("sql");
    expect(describeError({ status: 502, code: "upstream_unavailable", message: "the user service is unavailable" })).toContain("server had a problem");
  });
});

describe("asRead", () => {
  const err = (status: number, code: string): ReturnType<typeof asRead> => asRead({ ok: false, error: { status, code, message: "" } });
  test("maps account results onto the polling hook's failure reasons", () => {
    expect(asRead({ ok: true, data: 5 })).toEqual({ ok: true, data: 5 });
    expect(err(401, "unauthorized")).toEqual({ ok: false, reason: "unauthorized" });
    expect(err(429, "rate_limited")).toEqual({ ok: false, reason: "rate_limited" });
    expect(err(503, "backend_not_configured")).toEqual({ ok: false, reason: "disabled" });
    expect(err(0, "timeout")).toEqual({ ok: false, reason: "timeout" });
    expect(err(504, "upstream_timeout")).toEqual({ ok: false, reason: "timeout" });
    expect(err(0, "network")).toEqual({ ok: false, reason: "unavailable" });
    expect(err(502, "upstream_unavailable")).toEqual({ ok: false, reason: "unavailable" });
    expect(err(400, "validation_failed")).toEqual({ ok: false, reason: "bad_response" });
  });
});

describe("passwordProblem", () => {
  test("mirrors the server's length rules, counting characters not bytes", () => {
    expect(passwordProblem("short")).toContain("at least 10");
    expect(passwordProblem("123456789")).not.toBeNull();
    expect(passwordProblem("1234567890")).toBeNull();
    expect(passwordProblem("é".repeat(10))).toBeNull();
    expect(passwordProblem("😀".repeat(5))).toContain("at least 10"); // five characters, though ten UTF-16 units
    expect(passwordProblem("😀".repeat(100))).toBeNull(); // 100 characters, though 200 UTF-16 units
    expect(passwordProblem("x".repeat(128))).toBeNull();
    expect(passwordProblem("x".repeat(129))).toContain("at most 128");
  });
});

describe("safeNextPath", () => {
  test("keeps paths on this site", () => {
    expect(safeNextPath("/alerts")).toBe("/alerts");
    expect(safeNextPath("/account?tab=cities#top")).toBe("/account?tab=cities#top");
    expect(safeNextPath("/")).toBe("/");
  });

  test("never lets a link send someone to another site after signing in", () => {
    for (const bad of ["//evil.example", "/\\evil.example", "https://evil.example", "http://evil.example/x", "javascript:alert(1)", "evil.example", "", "/ok\nSet-Cookie: x", "/ok\u0000", null, undefined]) {
      expect(safeNextPath(bad)).toBe("/");
    }
  });
});
