import { describe, expect, test } from "bun:test";
import sample from "../../testdata/climate.sample.json";
import {
  type BackendClimate,
  climateFromBackend,
  createBackendClient,
  describeFallback,
  fetchAlerts,
  geoKey,
  insightFromBackend,
  readApi,
  riskFromBackend,
} from "./backend";

// The fixture is written by the Go backend (backend/internal/contracts/fixtures_test.go) from the
// real code of every service, so these tests fail if the API's shape drifts from the frontend's types.
const climate = sample as unknown as BackendClimate;

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v));

describe("mapping the backend's weather onto the dashboard's ClimateData", () => {
  const data = climateFromBackend(climate)!;

  test("keeps the hourly series whole and aligned", () => {
    expect(data.hourly.time).toHaveLength(240);
    for (const series of [data.hourly.temperature2m, data.hourly.relativeHumidity2m, data.hourly.apparentTemperature, data.hourly.heatIndex]) {
      expect(series).toHaveLength(240);
    }
  });

  test("daily covers today onward only (the dashboard treats daily[0] as today)", () => {
    expect(data.daily.time).toHaveLength(7);
    expect(data.daily.time[0]).toBe("2026-05-01");
    expect(climate.weather!.days.filter((d) => !d.forecast)).toHaveLength(3); // history exists in the payload...
    expect(data.daily.time).not.toContain("2026-04-30"); // ...but is not shown as forecast
    for (const series of Object.values(data.daily)) if (Array.isArray(series)) expect(series).toHaveLength(7);
  });

  test("current conditions carry over with the dashboard's field names", () => {
    const c = climate.weather!.current;
    expect(data.current.time).toBe(c.time);
    expect(data.current.temperature2m).toBe(c.temperatureC);
    expect(data.current.relativeHumidity2m).toBe(c.humidity);
    expect(data.current.apparentTemperature).toBe(c.apparentTemperatureC);
    expect(data.current.windSpeed10m).toBe(c.windSpeed);
    expect(data.latitude).toBeCloseTo(19.076);
    expect(data.timezone).toBe("Asia/Kolkata");
  });

  test("returns null rather than half-rendering when the data is incomplete", () => {
    expect(climateFromBackend({ ...climate, weather: null })).toBeNull();
    const noHourly = clone(climate);
    noHourly.weather!.hourly = null;
    expect(climateFromBackend(noHourly)).toBeNull();
    const noForecast = clone(climate);
    noForecast.weather!.days.forEach((d) => (d.forecast = false));
    expect(climateFromBackend(noForecast)).toBeNull();
  });
});

describe("mapping the backend's risk verdict", () => {
  test("daily tiers come from the backend, joined to the weather by date", () => {
    const r = riskFromBackend(climate)!;
    expect(r.dailyForecast).toHaveLength(7);
    expect(r.dailyForecast.map((d) => d.riskLevel)).toEqual(climate.risk!.days.map((d) => d.level));
    // The sample is a building heatwave: it must read as one on screen.
    expect(r.dailyForecast.some((d) => d.riskLevel === "danger")).toBe(true);
    expect(r.dailyForecast.at(-1)!.riskLevel).toBe("danger");
    expect(r.dailyForecast[0].tempMax).toBe(climate.weather!.days.find((d) => d.forecast)!.tempMaxC);
  });

  test("the assessment reflects 'now', with the explanation used as the message", () => {
    const { assessment } = riskFromBackend(climate)!;
    expect(assessment.riskLevel).toBe(climate.risk!.now.level);
    expect(assessment.heatIndexC).toBe(climate.weather!.current.heatIndexC);
    expect(assessment.consecutiveDangerDays).toBe(climate.risk!.now.consecutiveHotDays);
    expect(assessment.message.startsWith("Now:")).toBe(false);
    expect(assessment.message.length).toBeGreaterThan(20);
    expect(assessment.isHeatwaveWarning).toBe(false); // extreme-caution today; the warning is later in the week
  });

  test("returns null when there is no verdict, or it is out of step with the weather", () => {
    expect(riskFromBackend({ ...climate, risk: null })).toBeNull();
    const shifted = clone(climate);
    shifted.risk!.days = shifted.risk!.days.slice(1);
    expect(riskFromBackend(shifted)).toBeNull();
  });
});

describe("what the backend adds on top", () => {
  test("probabilities, peak, model and alerts", () => {
    const i = insightFromBackend(climate, 1);
    expect(i.locationId).toBe(1);
    expect(i.method).toBe("model");
    expect(i.modelVersion).toMatch(/^lr-/);
    expect(Object.keys(i.probabilityByDate)).toHaveLength(7);
    expect(i.probabilityByDate["2026-05-03"]).toBeGreaterThan(0.9);
    expect(i.peak!.probability).toBe(Math.max(...Object.values(i.probabilityByDate)));
    expect(i.alertLevel).toBe("extreme-danger");
    expect(i.heatwaveExpected).toBe(true);
    expect(i.openAlerts).toHaveLength(1);
    expect(i.openAlerts[0].headline).toContain("Mumbai");
    expect(i.dataQuality).toBe(1);
    expect(i.rationale.length).toBeGreaterThan(1);
    expect(i.degraded).toBe(false);
  });

  test("tolerates missing parts (a service was down)", () => {
    const i = insightFromBackend({ ...climate, prediction: null, risk: null, alerts: null, degraded: true }, 1);
    expect(i.method).toBeNull();
    expect(i.modelVersion).toBeNull();
    expect(i.probabilityByDate).toEqual({});
    expect(i.peak).toBeNull();
    expect(i.alertLevel).toBeNull();
    expect(i.openAlerts).toEqual([]);
    expect(i.degraded).toBe(true);
  });

  test("a rule-based prediction does not claim a model version", () => {
    const rules = clone(climate);
    rules.prediction!.method = "rules";
    expect(insightFromBackend(rules, 1).modelVersion).toBeNull();
  });
});

test("geoKey rounds like the backend does", () => {
  expect(geoKey(19.076, 72.8777)).toBe("19.08,72.88");
  expect(geoKey(19.0761, 72.8779)).toBe(geoKey(19.076, 72.8777)); // nearby points are the same place
  expect(geoKey(-0.001, 0.001)).toBe("0.00,0.00");
  expect(geoKey(-33.8688, 151.2093)).toBe("-33.87,151.21");
});

test("every failure has its own explanation", () => {
  const reasons = ["disabled", "unavailable", "timeout", "rate_limited", "unauthorized", "not_ready", "bad_response"] as const;
  const texts = new Set(reasons.map((r) => describeFallback(r)));
  expect(texts.size).toBe(reasons.length);
  expect(describeFallback(null)).toContain("unreachable");
});

// ---- the client, against a scripted fake API ----

type Reply = { status: number; body?: unknown; raw?: string } | (() => Promise<Response>) | Error;
type Handler = Reply | Reply[];

class FakeApi {
  calls: { method: string; path: string; body?: string }[] = [];
  constructor(private routes: Record<string, Handler>) {}

  set(route: string, handler: Handler) {
    this.routes[route] = handler;
  }

  count(route: string) {
    return this.calls.filter((c) => `${c.method} ${c.path}` === route).length;
  }

  fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const path = String(input).replace("/api/v1", "");
    const method = init?.method ?? "GET";
    this.calls.push({ method, path, body: init?.body as string | undefined });
    const key = Object.keys(this.routes).find((k) => k === `${method} ${path}` || (k.endsWith("*") && `${method} ${path}`.startsWith(k.slice(0, -1))));
    if (!key) return new Response("<html>404</html>", { status: 404 });
    let handler = this.routes[key];
    if (Array.isArray(handler)) handler = handler.length > 1 ? handler.shift()! : handler[0];
    if (handler instanceof Error) throw handler;
    if (typeof handler === "function") return handler();
    return new Response(handler.raw ?? JSON.stringify(handler.body ?? {}), { status: handler.status, headers: { "Content-Type": "application/json" } });
  };
}

const mumbai = { name: "Mumbai", country: "India", admin1: "Maharashtra", latitude: 19.076, longitude: 72.8777, timezone: "Asia/Kolkata" };
const listWithMumbai = { status: 200, body: { locations: [{ id: 7, name: "Mumbai", latitude: 19.076, longitude: 72.8777 }] } };
const okClimate = { status: 200, body: climate };

function memoryStorage(initial: Record<string, string> = {}) {
  const m = new Map(Object.entries(initial));
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), map: m };
}

function client(api: FakeApi, extra: Parameters<typeof createBackendClient>[0] = {}) {
  let t = 1_000_000;
  const clock = { advance: (ms: number) => (t += ms) };
  const sleeps: number[] = [];
  const c = createBackendClient({
    fetchImpl: api.fetch,
    storage: memoryStorage(),
    now: () => t,
    sleep: async (ms) => void sleeps.push(ms),
    warmupDelayMs: 7,
    ...extra,
  });
  return { c, clock, sleeps };
}

describe("backend client: finding the location", () => {
  test("uses an existing city without creating anything, and remembers its id", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": okClimate });
    const storage = memoryStorage();
    const { c } = client(api, { storage });

    const r = await c.fetchClimate(mumbai);
    expect(r.ok && r.locationId).toBe(7);
    expect(api.count("POST /locations")).toBe(0);
    expect(storage.map.get("heatwave-monitor:backend-id:19.08,72.88")).toBe("7");

    await c.fetchClimate(mumbai); // the second time the id is known: no listing at all
    expect(api.count("GET /locations")).toBe(1);
    expect(api.count("GET /locations/7/climate")).toBe(2);
  });

  test("registers an unknown place, sending what the backend needs", async () => {
    const api = new FakeApi({
      "GET /locations": { status: 200, body: { locations: [] } },
      "POST /locations": { status: 201, body: { id: 9 } },
      "GET /locations/9/climate": okClimate,
    });
    const { c } = client(api);
    const r = await c.fetchClimate({ name: "Surat", latitude: 21.1702, longitude: 72.8311 });
    expect(r.ok && r.locationId).toBe(9);
    const post = api.calls.find((x) => x.method === "POST")!;
    expect(JSON.parse(post.body!)).toEqual({ name: "Surat", country: "", admin1: "", latitude: 21.1702, longitude: 72.8311, timezone: "" });
  });

  test("a stale remembered id (backend database was reset) is looked up again once", async () => {
    const api = new FakeApi({
      "GET /locations/99/climate": { status: 404, body: { error: { code: "not_found" } } },
      "GET /locations": listWithMumbai,
      "GET /locations/7/climate": okClimate,
    });
    const storage = memoryStorage({ "heatwave-monitor:backend-id:19.08,72.88": "99" });
    const { c } = client(api, { storage });
    const r = await c.fetchClimate(mumbai);
    expect(r.ok && r.locationId).toBe(7);
    expect(storage.map.get("heatwave-monitor:backend-id:19.08,72.88")).toBe("7");
  });

  test("keeps working when browser storage is unavailable", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": okClimate });
    const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); }, removeItem: () => { throw new Error("denied"); } };
    const { c } = client(api, { storage: broken });
    expect((await c.fetchClimate(mumbai)).ok).toBe(true);
  });
});

describe("backend client: a new location warming up", () => {
  const warming = { status: 200, body: { ...climate, weather: null, status: "warming", sources: { ...climate.sources, processing: "no_data" } } };

  test("polls until the first result arrives", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": [warming, warming, okClimate] });
    const { c, sleeps } = client(api);
    const r = await c.fetchClimate(mumbai);
    expect(r.ok).toBe(true);
    expect(api.count("GET /locations/7/climate")).toBe(3);
    expect(sleeps).toEqual([7, 7]);
  });

  test("gives up after the allowed attempts, without punishing the backend", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": warming });
    const { c } = client(api, { warmupAttempts: 2 });
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "not_ready" });
    expect(api.count("GET /locations/7/climate")).toBe(3);
    await c.fetchClimate(mumbai); // not_ready is per location: the next try goes straight through
    expect(api.count("GET /locations/7/climate")).toBe(6);
  });

  test("weather missing because a service is failing reads as unavailable, not 'preparing'", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": { status: 200, body: { ...climate, weather: null, status: "partial", degraded: true } } });
    const { c } = client(api, { warmupAttempts: 1 });
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unavailable" });
  });
});

describe("backend client: failures fall back and are remembered", () => {
  test("an outage is reported once, then skipped for the cooldown, then retried", async () => {
    const api = new FakeApi({ "GET /locations": { status: 503, body: { error: { code: "unavailable" } } } });
    const { c, clock } = client(api, { downCooldownMs: 30_000 });

    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unavailable" });
    expect(api.calls).toHaveLength(1);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unavailable" });
    expect(api.calls).toHaveLength(1); // no second attempt: switching cities must not wait on a dead backend each time

    clock.advance(31_000);
    api.set("GET /locations", listWithMumbai);
    api.set("GET /locations/7/climate", okClimate);
    expect((await c.fetchClimate(mumbai)).ok).toBe(true);
  });

  test("a network error is an outage", async () => {
    const api = new FakeApi({ "GET /locations": new TypeError("Failed to fetch") });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unavailable" });
  });

  test("a slow backend times out", async () => {
    const hang = () => new Promise<Response>(() => {});
    const api = new FakeApi({ "GET /locations": async () => {
      // never answers on its own; resolves only through the abort signal handled by the client
      return hang();
    } });
    const { c } = client(api, { requestTimeoutMs: 20, fetchImpl: (_i, init) => new Promise((_res, rej) => init?.signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError")))) });
    const started = Date.now();
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "timeout" });
    expect(Date.now() - started).toBeLessThan(500);
  });

  test("a rate limit is reported and clears sooner than an outage", async () => {
    const api = new FakeApi({
      "GET /locations": { status: 200, body: { locations: [] } },
      "POST /locations": { status: 429, body: { error: { code: "rate_limited" } } },
    });
    const { c, clock } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "rate_limited" });
    const before = api.calls.length;
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "rate_limited" });
    expect(api.calls.length).toBe(before);
    clock.advance(11_000);
    await c.fetchClimate(mumbai);
    expect(api.calls.length).toBeGreaterThan(before);
  });

  test("adding a new city needs an account: reported as such, and not remembered, so seeded cities keep using the backend", async () => {
    const api = new FakeApi({
      "GET /locations": { status: 200, body: { locations: [{ id: 3, name: "Delhi", latitude: 28.61, longitude: 77.21 }] } },
      "POST /locations": { status: 401, body: { error: { code: "unauthorized", message: "sign in to do that" } } },
      "GET /locations/3/climate": okClimate,
    });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unauthorized" });
    const before = api.calls.length;
    const delhi = { name: "Delhi", country: "India", latitude: 28.61, longitude: 77.21 };
    const res = await c.fetchClimate(delhi); // a city the backend already has still works straight away
    expect(res.ok).toBe(true);
    expect(api.calls.length).toBeGreaterThan(before);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "unauthorized" });
    expect(api.count("POST /locations")).toBe(2); // asked again each time: signing in must take effect at once
  });

  test("an HTML 404 means nothing serves /api/v1 here (a plain static deployment): disabled, and not retried for a long while", async () => {
    const api = new FakeApi({}); // every route answers with Next's HTML 404 page
    const { c, clock } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "disabled" });
    clock.advance(60_000);
    await c.fetchClimate(mumbai);
    expect(api.calls).toHaveLength(1); // still skipped a minute later
    clock.advance(70_000);
    await c.fetchClimate(mumbai);
    expect(api.calls).toHaveLength(2);
  });

  test("the proxy's 'not configured' answer is also 'disabled'", async () => {
    const api = new FakeApi({ "GET /locations": { status: 503, body: { error: { code: "backend_not_configured" } } } });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "disabled" });
  });

  test("a gateway timeout is a timeout", async () => {
    const api = new FakeApi({ "GET /locations": { status: 504, body: { error: { code: "upstream_timeout" } } } });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "timeout" });
  });

  test("an answer that is not a climate response is rejected", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": { status: 200, body: { hello: "world" } } });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "bad_response" });
  });

  test("an unexpected client error status is a bad response", async () => {
    const api = new FakeApi({ "GET /locations": listWithMumbai, "GET /locations/7/climate": { status: 400, body: { error: { code: "invalid_id" } } } });
    const { c } = client(api);
    expect(await c.fetchClimate(mumbai)).toEqual({ ok: false, reason: "bad_response" });
  });

  test("a caller that moves on (aborts) is not blamed on the backend", async () => {
    const controller = new AbortController();
    const api = new FakeApi({ "GET /locations": async () => { controller.abort(); throw new DOMException("aborted", "AbortError"); } });
    const { c } = client(api);
    await expect(c.fetchClimate(mumbai, controller.signal)).rejects.toBeDefined();
    api.set("GET /locations", listWithMumbai);
    api.set("GET /locations/7/climate", okClimate);
    expect((await c.fetchClimate(mumbai)).ok).toBe(true); // no cooldown was set
  });
});

test("reset() makes the next call really try the backend (the user pressed Retry)", async () => {
  const api = new FakeApi({ "GET /locations": { status: 503, body: { error: { code: "unavailable" } } } });
  const { c } = client(api);
  await c.fetchClimate(mumbai);
  await c.fetchClimate(mumbai);
  expect(api.calls).toHaveLength(1); // remembered as down
  c.reset();
  await c.fetchClimate(mumbai);
  expect(api.calls).toHaveLength(2);
});

// ---- readApi: the plain reader used by the alerts and status pages ----


describe("readApi", () => {
  const reply = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

  test("returns the parsed body", async () => {
    const r = await readApi<{ a: number }>("/x", { fetchImpl: reply(200, { a: 1 }) });
    expect(r).toEqual({ ok: true, data: { a: 1 } });
  });

  test("asks the right URL, as JSON", async () => {
    let seen = null as { url: string; accept: string | null } | null; // assigned inside the fake fetch
    await readApi("/alerts?status=open", {
      fetchImpl: async (input, init) => {
        seen = { url: String(input), accept: new Headers(init?.headers).get("Accept") };
        return new Response("{}", { status: 200 });
      },
    });
    expect(seen).toEqual({ url: "/api/v1/alerts?status=open", accept: "application/json" });
  });

  test("classifies failures the same way the climate client does", async () => {
    const reason = async (status: number, body: unknown) => (await readApi("/x", { fetchImpl: reply(status, body) }) as { reason: string }).reason;
    expect(await reason(429, { error: { code: "rate_limited" } })).toBe("rate_limited");
    expect(await reason(401, { error: { code: "unauthorized" } })).toBe("unauthorized");
    expect(await reason(504, { error: { code: "upstream_timeout" } })).toBe("timeout");
    expect(await reason(503, { error: { code: "backend_not_configured" } })).toBe("disabled");
    expect(await reason(503, { error: { code: "unavailable" } })).toBe("unavailable");
    expect(await reason(400, { error: { code: "invalid_id" } })).toBe("bad_response");
    const html = await readApi("/x", { fetchImpl: async () => new Response("<html>404</html>", { status: 404 }) });
    expect(html).toEqual({ ok: false, reason: "disabled" });
  });

  test("rejects an answer of the wrong shape or a 200 that is not JSON", async () => {
    const isList = (b: unknown): b is { alerts: unknown[] } => typeof b === "object" && b !== null && Array.isArray((b as { alerts?: unknown }).alerts);
    expect(await readApi("/x", { fetchImpl: reply(200, { nope: 1 }), check: isList })).toEqual({ ok: false, reason: "bad_response" });
    expect(await readApi("/x", { fetchImpl: async () => new Response("not json", { status: 200 }) })).toEqual({ ok: false, reason: "bad_response" });
  });

  test("network errors and timeouts are failures, not exceptions", async () => {
    expect(await readApi("/x", { fetchImpl: async () => { throw new TypeError("Failed to fetch"); } })).toEqual({ ok: false, reason: "unavailable" });
    const hang = (_i: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_res, rej) => init?.signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError"))));
    expect(await readApi("/x", { fetchImpl: hang, timeoutMs: 20 })).toEqual({ ok: false, reason: "timeout" });
  });

  test("a caller who navigates away aborts the request (it rejects, like fetch)", async () => {
    const controller = new AbortController();
    const hang = (_i: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_res, rej) => init?.signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError"))));
    const pending = readApi("/x", { fetchImpl: hang, signal: controller.signal, timeoutMs: 5000 });
    controller.abort();
    await expect(pending).rejects.toBeDefined();
  });

  test("has no failure memory: a polling page must notice the backend coming back", async () => {
    let calls = 0;
    const flaky = async () => {
      calls++;
      return calls < 3 ? new Response("{}", { status: 503 }) : new Response(JSON.stringify({ alerts: [] }), { status: 200 });
    };
    expect((await readApi("/x", { fetchImpl: flaky })).ok).toBe(false);
    expect((await readApi("/x", { fetchImpl: flaky })).ok).toBe(false);
    expect((await readApi("/x", { fetchImpl: flaky })).ok).toBe(true);
    expect(calls).toBe(3);
  });

  test("fetchAlerts validates the list shape", async () => {
    expect(await fetchAlerts("open", { fetchImpl: reply(200, { alerts: [] }) })).toEqual({ ok: true, data: { alerts: [] } });
    expect(await fetchAlerts("open", { fetchImpl: reply(200, { alerts: "nope" }) })).toEqual({ ok: false, reason: "bad_response" });
  });
});
