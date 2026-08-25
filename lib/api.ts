import { calculateHeatIndex } from "./heatwaveEngine";
import type { ClimateData, GeoLocation } from "./types";

const GEOCODING_URL = "https://geocoding-api.open-meteo.com/v1/search";
const FORECAST_URL = "https://api.open-meteo.com/v1/forecast";

const CACHE_PREFIX = "heatwave-monitor:";
const CLIMATE_CACHE_TTL_MS = 10 * 60 * 1000; // 10 minutes — matches Open-Meteo's update cadence

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
  if (!res.ok) throw new Error(`Geocoding request failed: ${res.status}`);

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
  if (!res.ok) throw new Error(`Forecast request failed: ${res.status}`);

  const data: OpenMeteoForecastResponse = await res.json();

  const heatIndex = data.hourly.temperature_2m.map((t, i) =>
    calculateHeatIndex(t, data.hourly.relative_humidity_2m[i]),
  );

  return {
    latitude: data.latitude,
    longitude: data.longitude,
    timezone: data.timezone,
    current: {
      time: data.current.time,
      temperature2m: data.current.temperature_2m,
      relativeHumidity2m: data.current.relative_humidity_2m,
      apparentTemperature: data.current.apparent_temperature,
      weatherCode: data.current.weather_code,
      windSpeed10m: data.current.wind_speed_10m,
      directNormalIrradiance: data.current.direct_normal_irradiance,
    },
    hourly: {
      time: data.hourly.time,
      temperature2m: data.hourly.temperature_2m,
      relativeHumidity2m: data.hourly.relative_humidity_2m,
      apparentTemperature: data.hourly.apparent_temperature,
      heatIndex,
    },
    daily: {
      time: data.daily.time,
      temperature2mMax: data.daily.temperature_2m_max,
      temperature2mMin: data.daily.temperature_2m_min,
      apparentTemperatureMax: data.daily.apparent_temperature_max,
      uvIndexMax: data.daily.uv_index_max,
      precipitationSum: data.daily.precipitation_sum,
    },
  };
}

/** Simple TTL cache over localStorage, used to avoid refetching on quick reloads. */
export function readCache<T>(key: string, maxAgeMs = CLIMATE_CACHE_TTL_MS): T | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(CACHE_PREFIX + key);
    if (!raw) return null;
    const parsed: { value: T; timestamp: number } = JSON.parse(raw);
    if (Date.now() - parsed.timestamp > maxAgeMs) return null;
    return parsed.value;
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
