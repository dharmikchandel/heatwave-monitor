import { calculateHeatIndex } from "./heatwaveEngine";
import type { ClimateData, GeoLocation } from "./types";

const GEOCODING_URL = "https://geocoding-api.open-meteo.com/v1/search";
const FORECAST_URL = "https://api.open-meteo.com/v1/forecast";

const CACHE_PREFIX = "heatwave-monitor:";
const CLIMATE_CACHE_TTL_MS = 10 * 60 * 1000; // 10 minutes — matches Open-Meteo's update cadence

/** Thrown for non-2xx API responses, carrying the HTTP status so callers can
 * distinguish "rate limited" / "service down" from a generic network failure. */
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/** Open-Meteo can return `null` for individual samples it has no data for
 * (forecast-horizon edges, model gaps). Coerces those — and any other
 * non-finite value — to a safe fallback so downstream math never sees NaN. */
function sanitizeNumberArray(values: unknown[], fallback = 0): number[] {
  return values.map((v) => (typeof v === "number" && Number.isFinite(v) ? v : fallback));
}

function sanitizeNumber(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

interface OpenMeteoGeocodingResult {
  id: number;
  name: string;
  country: string;
  admin1?: string;
  latitude: number;
  longitude: number;
  timezone: string;
}

interface OpenMeteoForecastResponse {
  latitude: number;
  longitude: number;
  timezone: string;
  current: {
    time: string;
    temperature_2m: number;
    relative_humidity_2m: number;
    apparent_temperature: number;
    weather_code: number;
    wind_speed_10m: number;
    direct_normal_irradiance: number;
  };
  hourly: {
    time: string[];
    temperature_2m: number[];
    relative_humidity_2m: number[];
    apparent_temperature: number[];
  };
  daily: {
    time: string[];
    temperature_2m_max: number[];
    temperature_2m_min: number[];
    apparent_temperature_max: number[];
    uv_index_max: number[];
    precipitation_sum: number[];
  };
}

export async function searchLocations(query: string, signal?: AbortSignal): Promise<GeoLocation[]> {
  const trimmed = query.trim();
  if (trimmed.length < 2) return [];

  const url = new URL(GEOCODING_URL);
  url.searchParams.set("name", trimmed);
  url.searchParams.set("count", "5");
  url.searchParams.set("language", "en");
  url.searchParams.set("format", "json");

  const res = await fetch(url.toString(), { signal });
  if (!res.ok) throw new ApiError(`Geocoding request failed: ${res.status}`, res.status);

  const data: { results?: OpenMeteoGeocodingResult[] } = await res.json();
  return (data.results ?? []).map((r) => ({
    id: r.id,
    name: r.name,
    country: r.country,
    admin1: r.admin1,
    latitude: r.latitude,
    longitude: r.longitude,
    timezone: r.timezone,
  }));
}

export async function fetchClimateData(
  latitude: number,
  longitude: number,
  signal?: AbortSignal,
): Promise<ClimateData> {
  const url = new URL(FORECAST_URL);
  url.searchParams.set("latitude", latitude.toString());
  url.searchParams.set("longitude", longitude.toString());
  url.searchParams.set(
    "current",
    "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m,direct_normal_irradiance",
  );
  url.searchParams.set("hourly", "temperature_2m,relative_humidity_2m,apparent_temperature");
  url.searchParams.set(
    "daily",
    "temperature_2m_max,temperature_2m_min,apparent_temperature_max,uv_index_max,precipitation_sum",
  );
  url.searchParams.set("timezone", "auto");
  url.searchParams.set("forecast_days", "7");

  const res = await fetch(url.toString(), { signal });
  if (!res.ok) throw new ApiError(`Forecast request failed: ${res.status}`, res.status);

  const data: OpenMeteoForecastResponse = await res.json();

  const hourlyTemp = sanitizeNumberArray(data.hourly.temperature_2m);
  const hourlyHumidity = sanitizeNumberArray(data.hourly.relative_humidity_2m);
  const hourlyApparent = sanitizeNumberArray(data.hourly.apparent_temperature);
  const heatIndex = hourlyTemp.map((t, i) => calculateHeatIndex(t, hourlyHumidity[i]));

  return {
    latitude: data.latitude,
    longitude: data.longitude,
    timezone: data.timezone,
    current: {
      time: data.current.time,
      temperature2m: sanitizeNumber(data.current.temperature_2m),
      relativeHumidity2m: sanitizeNumber(data.current.relative_humidity_2m),
      apparentTemperature: sanitizeNumber(data.current.apparent_temperature),
      weatherCode: sanitizeNumber(data.current.weather_code),
      windSpeed10m: sanitizeNumber(data.current.wind_speed_10m),
      directNormalIrradiance: sanitizeNumber(data.current.direct_normal_irradiance),
    },
    hourly: {
      time: data.hourly.time,
      temperature2m: hourlyTemp,
      relativeHumidity2m: hourlyHumidity,
      apparentTemperature: hourlyApparent,
      heatIndex,
    },
    daily: {
      time: data.daily.time,
      temperature2mMax: sanitizeNumberArray(data.daily.temperature_2m_max),
      temperature2mMin: sanitizeNumberArray(data.daily.temperature_2m_min),
      apparentTemperatureMax: sanitizeNumberArray(data.daily.apparent_temperature_max),
      uvIndexMax: sanitizeNumberArray(data.daily.uv_index_max),
      precipitationSum: sanitizeNumberArray(data.daily.precipitation_sum),
    },
  };
}

/** Simple TTL cache over localStorage, used to avoid refetching on quick reloads. */
export function readCache<T>(key: string, maxAgeMs = CLIMATE_CACHE_TTL_MS): { value: T; timestamp: number } | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(CACHE_PREFIX + key);
    if (!raw) return null;
    const parsed: { value: T; timestamp: number } = JSON.parse(raw);
    if (Date.now() - parsed.timestamp > maxAgeMs) return null;
    return parsed;
  } catch {
    return null;
  }
}

export function writeCache<T>(key: string, value: T): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(CACHE_PREFIX + key, JSON.stringify({ value, timestamp: Date.now() }));
  } catch {
    // localStorage may be unavailable (private browsing, quota) — caching is a
    // best-effort optimization, so silently skip it rather than fail the app.
  }
}

export function climateCacheKey(latitude: number, longitude: number): string {
  return `climate:${latitude.toFixed(2)}:${longitude.toFixed(2)}`;
}
