// Client for the backend API gateway, plus the mapping from its answers onto the
// types the dashboard already uses. The backend is a progressive enhancement: any
// failure here makes the app fall back to fetching Open-Meteo directly.
//
// In the browser the API lives at the same origin (/api/v1/*): proxy.ts forwards it
// to the gateway, so there is no CORS and the gateway's address never reaches the page.

import type {
  BackendAlert,
  BackendInsight,
  ClimateData,
  DailyRiskForecast,
  HeatRiskLevel,
  HeatwaveAssessment,
} from "./types";

export const API_BASE = "/api/v1";

// ---- wire types (what the gateway returns; see backend/internal/contracts) ----

interface WireCurrent {
  time: string;
  temperatureC: number;
  humidity: number;
  apparentTemperatureC: number;
  heatIndexC: number;
  windSpeed: number;
  weatherCode: number;
  directNormalIrradiance: number;
}

interface WireHourly {
  time: string[];
  temperatureC: number[];
  humidity: number[];
  apparentTemperatureC: number[];
  heatIndexC: number[];
  windSpeed: number[];
  currentIndex: number;
}

interface WireDay {
  date: string;
  forecast: boolean;
  tempMaxC: number;
  tempMinC: number;
  apparentTempMaxC: number;
  heatIndexMaxC: number;
  uvIndexMax: number;
  precipitationSumMm: number;
  estimated: boolean;
}

interface WireWeather {
  fetchedAt: string;
  location: { latitude: number; longitude: number; timezone: string };
  current: WireCurrent;
  hourly?: WireHourly | null;
  days: WireDay[];
  quality: { score: number };
}

interface WireDayProbability {
  date: string;
  horizon: number;
  probability: number;
}

interface WirePrediction {
  method: string;
  modelVersion: string;
  days: WireDayProbability[];
  peak: WireDayProbability;
}

interface WireDayRisk {
  date: string;
  level: HeatRiskLevel;
  probability: number;
}

interface WireRisk {
  now: { level: HeatRiskLevel; consecutiveHotDays: number; reason: string };
  days: WireDayRisk[];
  alertLevel: HeatRiskLevel;
  heatwaveExpected: boolean;
  rationale: string[];
}

/** The composed answer of GET /api/v1/locations/{id}/climate. */
export interface BackendClimate {
  location: { id: number } | null;
  weather: WireWeather | null;
  prediction: WirePrediction | null;
  risk: WireRisk | null;
  alerts: BackendAlert[] | null;
  status: "ok" | "partial" | "warming";
  degraded: boolean;
  sources: Record<string, string>;
  generatedAt: string;
}

// ---- helpers ----

/** Coordinates rounded to ~1 km, matching how the backend de-duplicates locations. */
export function geoKey(latitude: number, longitude: number): string {
  const round = (v: number) => Math.round(v * 100) / 100 + 0; // + 0 turns -0 into 0
  return `${round(latitude).toFixed(2)},${round(longitude).toFixed(2)}`;
}

export type BackendFailure =
  | "disabled" // no backend is configured for this deployment
  | "unavailable" // unreachable, erroring or circuit open
  | "timeout"
  | "rate_limited"
  | "unauthorized" // the backend wants a signed-in user (to add a new city)
  | "not_ready" // the backend has no data for this location yet
  | "bad_response";

export type BackendResult =
  | { ok: true; climate: BackendClimate & { weather: WireWeather }; locationId: number }
  | { ok: false; reason: BackendFailure };

/** A short, user-facing explanation of why the dashboard is not using the backend. */
export function describeFallback(reason: BackendFailure | null): string {
  switch (reason) {
    case "disabled":
      return "No backend is configured; computing everything in your browser.";
    case "rate_limited":
      return "The backend is busy (rate limited); showing locally computed data.";
    case "unauthorized":
      return "Sign in to have the backend track new cities; showing locally computed data.";
    case "timeout":
      return "The backend is slow to respond; showing locally computed data.";
    case "not_ready":
      return "The backend is still preparing this location; showing locally computed data.";
    case "bad_response":
      return "The backend sent an unexpected answer; showing locally computed data.";
    default:
      return "The backend is unreachable; showing locally computed data.";
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isBackendClimate(v: unknown): v is BackendClimate {
  return isRecord(v) && typeof v.status === "string" && isRecord(v.sources) && "weather" in v;
}

// ---- mapping onto the dashboard's types ----

/** The backend's cleaned weather as the ClimateData the components render. Needs the hourly series. */
export function climateFromBackend(c: BackendClimate): ClimateData | null {
  const w = c.weather;
  if (!w || !w.hourly || w.hourly.time.length === 0) return null;
  const forecastDays = w.days.filter((d) => d.forecast);
  if (forecastDays.length === 0) return null;

  return {
    latitude: w.location.latitude,
    longitude: w.location.longitude,
    timezone: w.location.timezone,
    current: {
      time: w.current.time,
      temperature2m: w.current.temperatureC,
      relativeHumidity2m: w.current.humidity,
      apparentTemperature: w.current.apparentTemperatureC,
      weatherCode: w.current.weatherCode,
      windSpeed10m: w.current.windSpeed,
      directNormalIrradiance: w.current.directNormalIrradiance,
    },
    hourly: {
      time: w.hourly.time,
      temperature2m: w.hourly.temperatureC,
      relativeHumidity2m: w.hourly.humidity,
      apparentTemperature: w.hourly.apparentTemperatureC,
      heatIndex: w.hourly.heatIndexC,
    },
    // Today onward only: the dashboard treats daily[0] as today.
    daily: {
      time: forecastDays.map((d) => d.date),
      temperature2mMax: forecastDays.map((d) => d.tempMaxC),
      temperature2mMin: forecastDays.map((d) => d.tempMinC),
      apparentTemperatureMax: forecastDays.map((d) => d.apparentTempMaxC),
      uvIndexMax: forecastDays.map((d) => d.uvIndexMax),
      precipitationSum: forecastDays.map((d) => d.precipitationSumMm),
    },
  };
}

/**
 * The backend's risk verdict as the dashboard's types, or null when the risk service
 * contributed nothing (the caller then uses the local engine, as it does without a backend).
 */
export function riskFromBackend(
  c: BackendClimate,
): { dailyForecast: DailyRiskForecast[]; assessment: HeatwaveAssessment } | null {
  const w = c.weather;
  const r = c.risk;
  if (!w || !r) return null;

  const levelByDate = new Map(r.days.map((d) => [d.date, d.level]));
  const forecastDays = w.days.filter((d) => d.forecast);
  if (forecastDays.some((d) => !levelByDate.has(d.date))) return null; // out of step: do not guess

  return {
    dailyForecast: forecastDays.map((d) => ({
      date: d.date,
      tempMax: d.tempMaxC,
      tempMin: d.tempMinC,
      apparentTempMax: d.apparentTempMaxC,
      uvIndexMax: d.uvIndexMax,
      precipitationSum: d.precipitationSumMm,
      riskLevel: levelByDate.get(d.date) as HeatRiskLevel,
    })),
    assessment: {
      riskLevel: r.now.level,
      heatIndexC: w.current.heatIndexC,
      consecutiveDangerDays: r.now.consecutiveHotDays,
      isHeatwaveWarning: r.now.level === "danger" || r.now.level === "extreme-danger",
      // "Now: Danger — apparent temperature ..." reads better on the card without the lead-in.
      message: r.now.reason.replace(/^Now:\s*/, ""),
    },
  };
}

/** Everything the backend adds on top of the numbers. */
export function insightFromBackend(c: BackendClimate, locationId: number): BackendInsight {
  const p = c.prediction;
  return {
    locationId,
    status: c.status,
    degraded: c.degraded,
    method: p?.method ?? null,
    modelVersion: p?.method === "model" ? p.modelVersion : null,
    probabilityByDate: Object.fromEntries((p?.days ?? []).map((d) => [d.date, d.probability])),
    peak: p ? { date: p.peak.date, probability: p.peak.probability } : null,
    rationale: c.risk?.rationale ?? [],
    alertLevel: c.risk?.alertLevel ?? null,
    heatwaveExpected: c.risk?.heatwaveExpected ?? false,
    openAlerts: c.alerts ?? [],
    dataQuality: c.weather?.quality.score ?? null,
    generatedAt: c.generatedAt,
  };
}

// ---- the client ----

type Fetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>; // just the call signature (bun-types adds more to typeof fetch)
type KeyValueStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

export interface BackendClientOptions {
  baseUrl?: string;
  fetchImpl?: Fetch;
  /** Remembers which backend id belongs to which place (survives reloads). */
  storage?: KeyValueStorage | null;
  now?: () => number;
  requestTimeoutMs?: number;
  /** How often a new location's data is polled for while the pipeline produces it. */
  warmupAttempts?: number;
  warmupDelayMs?: number;
  sleep?: (ms: number) => Promise<void>;
  /** After a failure, skip the backend for this long instead of making every action wait for a timeout. */
  downCooldownMs?: number;
}

const ID_KEY_PREFIX = "heatwave-monitor:backend-id:";

/** The place to look up or register: only a name and coordinates are required. */
export interface Place {
  name: string;
  country?: string;
  admin1?: string;
  latitude: number;
  longitude: number;
  timezone?: string;
}

export interface BackendClient {
  /** Forget remembered failures, so the next call really tries the backend (the user pressed Retry). */
  reset(): void;
  fetchClimate(loc: Place, signal?: AbortSignal): Promise<BackendResult>;
}

class Failure extends Error {
  constructor(readonly reason: BackendFailure) {
    super(reason);
  }
}

/** One JSON request with a timeout. Network failures and timeouts become Failures; a caller's abort is rethrown. */
async function requestJson(doFetch: Fetch, url: string, init: RequestInit | undefined, timeoutMs: number, signal?: AbortSignal) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const onAbort = () => controller.abort();
  signal?.addEventListener("abort", onAbort);
  try {
    const res = await doFetch(url, { ...init, signal: controller.signal, headers: { Accept: "application/json", ...init?.headers } });
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      // not JSON: an HTML error page from a proxy in front of a dead backend, for instance
    }
    return { status: res.status, body };
  } catch (err) {
    if (signal?.aborted) throw err; // the caller moved on; not a backend failure
    throw new Failure(controller.signal.aborted ? "timeout" : "unavailable");
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", onAbort);
  }
}

/** Classify a non-success answer. */
function failureFor(status: number, body: unknown): Failure {
  const code = isRecord(body) && isRecord(body.error) ? body.error.code : undefined;
  if (code === "backend_not_configured") return new Failure("disabled");
  if (status === 429) return new Failure("rate_limited");
  if (status === 401) return new Failure("unauthorized");
  if (status === 504) return new Failure("timeout");
  if (status === 404 && !isRecord(body)) return new Failure("disabled"); // an HTML 404: nothing serves /api/v1 here
  return new Failure(status >= 500 || status === 0 ? "unavailable" : "bad_response");
}

export function createBackendClient(options: BackendClientOptions = {}): BackendClient {
  const baseUrl = options.baseUrl ?? API_BASE;
  const doFetch: Fetch = options.fetchImpl ?? ((input, init) => fetch(input, init));
  const storage = options.storage === undefined ? safeLocalStorage() : options.storage;
  const now = options.now ?? Date.now;
  const timeoutMs = options.requestTimeoutMs ?? 4000;
  const warmupAttempts = options.warmupAttempts ?? 5;
  const warmupDelayMs = options.warmupDelayMs ?? 1000;
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const downCooldownMs = options.downCooldownMs ?? 30_000;

  let downUntil = 0;
  let downReason: BackendFailure = "unavailable";

  function readId(key: string): number | null {
    try {
      const v = Number(storage?.getItem(ID_KEY_PREFIX + key));
      return Number.isInteger(v) && v > 0 ? v : null;
    } catch {
      return null;
    }
  }
  function writeId(key: string, id: number | null) {
    try {
      if (id === null) storage?.removeItem(ID_KEY_PREFIX + key);
      else storage?.setItem(ID_KEY_PREFIX + key, String(id));
    } catch {
      // storage may be unavailable (private mode, quota); the id is simply looked up again next time
    }
  }

  const request = (path: string, init: RequestInit | undefined, signal?: AbortSignal) =>
    requestJson(doFetch, baseUrl + path, init, timeoutMs, signal);

  async function findOrRegister(loc: Place, signal?: AbortSignal): Promise<number> {
    const key = geoKey(loc.latitude, loc.longitude);
    const cached = readId(key);
    if (cached !== null) return cached;

    // Reading is cheap and not write-limited, so look for an existing entry (e.g. a seeded city) first.
    const list = await request("/locations", undefined, signal);
    if (list.status !== 200) throw failureFor(list.status, list.body);
    const locations = isRecord(list.body) && Array.isArray(list.body.locations) ? list.body.locations : [];
    for (const l of locations) {
      if (isRecord(l) && typeof l.id === "number" && typeof l.latitude === "number" && typeof l.longitude === "number" &&
          geoKey(l.latitude, l.longitude) === key) {
        writeId(key, l.id);
        return l.id;
      }
    }

    const created = await request(
      "/locations",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: loc.name,
          country: loc.country ?? "",
          admin1: loc.admin1 ?? "",
          latitude: loc.latitude,
          longitude: loc.longitude,
          timezone: loc.timezone ?? "",
        }),
      },
      signal,
    );
    if ((created.status !== 200 && created.status !== 201) || !isRecord(created.body) || typeof created.body.id !== "number") {
      throw failureFor(created.status, created.body);
    }
    writeId(key, created.body.id);
    return created.body.id;
  }

  async function climateOnce(id: number, signal?: AbortSignal): Promise<{ status: number; body: unknown }> {
    return request(`/locations/${id}/climate`, undefined, signal);
  }

  async function run(loc: Place, signal?: AbortSignal): Promise<BackendResult> {
    const key = geoKey(loc.latitude, loc.longitude);
    let id = await findOrRegister(loc, signal);

    for (let attempt = 0, retriedId = false; ; attempt++) {
      const res = await climateOnce(id, signal);

      if (res.status === 404 && !retriedId) {
        // The backend forgot this id (its database was reset): look the place up again, once.
        retriedId = true;
        writeId(key, null);
        id = await findOrRegister(loc, signal);
        continue;
      }
      if (res.status !== 200) throw failureFor(res.status, res.body);
      if (!isBackendClimate(res.body)) throw new Failure("bad_response");
      const climate = res.body;

      if (climate.weather && climate.weather.hourly) {
        return { ok: true, climate: climate as BackendClimate & { weather: WireWeather }, locationId: id };
      }
      if (attempt >= warmupAttempts) {
        return { ok: false, reason: climate.degraded ? "unavailable" : "not_ready" };
      }
      await sleep(warmupDelayMs); // a new location: the pipeline is still producing its first result
    }
  }

  return {
    reset() {
      downUntil = 0;
    },
    async fetchClimate(loc, signal) {
      if (now() < downUntil) return { ok: false, reason: downReason };
      try {
        return await run(loc, signal);
      } catch (err) {
        if (signal?.aborted) throw err;
        const reason = err instanceof Failure ? err.reason : "unavailable";
        // "disabled" and outages are worth remembering; a rate limit clears sooner; not_ready and
        // unauthorized are about one city (the others still work), so they are not remembered.
        if (reason === "disabled" || reason === "unavailable" || reason === "timeout" || reason === "bad_response") {
          downUntil = now() + (reason === "disabled" ? downCooldownMs * 4 : downCooldownMs);
          downReason = reason;
        } else if (reason === "rate_limited") {
          downUntil = now() + 10_000;
          downReason = reason;
        }
        return { ok: false, reason };
      }
    },
  };
}

function safeLocalStorage(): KeyValueStorage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** The app-wide client. */
export const backend = createBackendClient();

// ---- reading other endpoints (alerts, status, model) ----

export type ApiRead<T> = { ok: true; data: T } | { ok: false; reason: BackendFailure };

/**
 * GET an endpoint and return its JSON. Unlike the climate client this has no failure
 * memory: pages that poll (status, alerts) must keep asking so they notice recovery.
 * An aborted request rejects, like fetch does.
 */
export async function readApi<T>(
  path: string,
  options: { signal?: AbortSignal; fetchImpl?: Fetch; baseUrl?: string; timeoutMs?: number; check?: (body: unknown) => body is T } = {},
): Promise<ApiRead<T>> {
  const doFetch: Fetch = options.fetchImpl ?? ((input, init) => fetch(input, init));
  try {
    const { status, body } = await requestJson(doFetch, (options.baseUrl ?? API_BASE) + path, undefined, options.timeoutMs ?? 5000, options.signal);
    if (status !== 200) return { ok: false, reason: failureFor(status, body).reason };
    if (body === null || (options.check && !options.check(body))) return { ok: false, reason: "bad_response" };
    return { ok: true, data: body as T };
  } catch (err) {
    if (err instanceof Failure) return { ok: false, reason: err.reason };
    throw err;
  }
}

export type AlertStatus = "open" | "resolved";

/** An alert as the alerts API serves it. */
export interface AlertRecord extends BackendAlert {
  details: {
    observationId: number;
    heatwaveExpected: boolean;
    warningDays: string[];
    rationale: string[];
    method: string;
    modelVersion: string;
    peak: { date: string; probability: number; level: HeatRiskLevel };
  };
  updatedAt: string;
  resolvedAt?: string;
  acknowledgedAt?: string;
  reopenCount: number;
}

export interface AlertTimelineEntry {
  at: string;
  kind: "opened" | "escalated" | "deescalated" | "resolved" | "reopened";
  level: HeatRiskLevel;
  note?: string;
}

export interface AlertNotification {
  id: number;
  subscriptionId: number;
  kind: "opened" | "escalated" | "resolved";
  level: HeatRiskLevel;
  channel: "webhook" | "log" | "inapp";
  status: "pending" | "sent" | "failed" | "cancelled";
  attempts: number;
  createdAt: string;
  sentAt?: string;
  lastError?: string;
}

export interface AlertDetail extends AlertRecord {
  history: AlertTimelineEntry[];
  notifications: AlertNotification[];
}

export interface ServiceStatus {
  name: "weather" | "processing" | "prediction" | "risk" | "alert" | "user";
  /** "ok", "unavailable" (reachable but not ready) or "down". */
  status: "ok" | "unavailable" | "down";
  ready: boolean;
  latencyMs: number;
  checks?: Record<string, string>;
  error?: string;
  /** The gateway's circuit breaker for this service. */
  circuit: "closed" | "open" | "half-open";
}

export interface SystemStatus {
  status: "ok" | "degraded" | "down";
  services: ServiceStatus[];
  checkedAt: string;
}

export interface ModelInfo {
  loaded: boolean;
  version?: string;
  trainedAt?: string;
  horizons?: number;
  training?: { cities: string[]; years: string; samples: number; baseRate: number; target: string };
  evaluation?: {
    holdoutYears: string;
    perHorizon: { horizon: number; auc: number; brier: number; brierClimatology: number }[];
    leaveOneCityOut?: { cities: { city: string; auc: number | null }[] };
  };
}

const isAlertList = (b: unknown): b is { alerts: AlertRecord[] } => isRecord(b) && Array.isArray(b.alerts);
const isAlertDetail = (b: unknown): b is AlertDetail => isRecord(b) && typeof b.id === "number" && Array.isArray(b.history) && Array.isArray(b.notifications);
const isSystemStatus = (b: unknown): b is SystemStatus => isRecord(b) && typeof b.status === "string" && Array.isArray(b.services);
const isModelInfo = (b: unknown): b is ModelInfo => isRecord(b) && typeof b.loaded === "boolean";

type ReadOptions = Parameters<typeof readApi>[1];

export const fetchAlerts = (status: AlertStatus | "all", options?: ReadOptions) =>
  readApi<{ alerts: AlertRecord[] }>(`/alerts?status=${status}&limit=100`, { ...options, check: isAlertList });
export const fetchAlertDetail = (id: number, options?: ReadOptions) => readApi<AlertDetail>(`/alerts/${id}`, { ...options, check: isAlertDetail });
export const fetchSystemStatus = (options?: ReadOptions) => readApi<SystemStatus>("/status", { ...options, check: isSystemStatus });
export const fetchModelInfo = (options?: ReadOptions) => readApi<ModelInfo>("/model", { ...options, check: isModelInfo });
