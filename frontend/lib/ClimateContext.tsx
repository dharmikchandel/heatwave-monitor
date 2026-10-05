"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError, climateCacheKey, fetchClimateData, readCache, writeCache } from "./api";
import { useAuth } from "./AuthContext";
import { backend, climateFromBackend, insightFromBackend, riskFromBackend, type BackendFailure } from "./backend";
import { assessHeatwave, buildDailyRiskForecast } from "./heatwaveEngine";
import type { BackendInsight, ClimateData, DailyRiskForecast, DataSource, GeoLocation, HeatwaveAssessment } from "./types";

const DEFAULT_LOCATION: GeoLocation = {
  id: 0,
  name: "Mumbai",
  country: "India",
  admin1: "Maharashtra",
  latitude: 19.076,
  longitude: 72.8777,
  timezone: "Asia/Kolkata",
};

const UNIT_STORAGE_KEY = "heatwave-unit";

function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 429) return "The weather service is receiving too many requests right now. Please wait a moment and try again.";
    if (err.status >= 500) return "The weather service is temporarily unavailable. Please try again shortly.";
    if (err.status === 404) return "No weather data is available for this location.";
  }
  return "Couldn't reach the climate data service. Check your connection and try again.";
}

interface ClimateContextValue {
  location: GeoLocation | null;
  climateData: ClimateData | null;
  dailyForecast: DailyRiskForecast[];
  assessment: HeatwaveAssessment | null;
  unit: "C" | "F";
  setUnit: (unit: "C" | "F") => void;
  isLoading: boolean;
  isLocating: boolean;
  error: string | null;
  lastUpdated: Date | null;
  /** Where the data came from: the backend, or computed in the browser from Open-Meteo. */
  dataSource: DataSource | null;
  /** Probabilities, explanations and alerts from the backend (null in local mode). */
  insight: BackendInsight | null;
  /** Why the backend was not used, when it was not. */
  fallbackReason: BackendFailure | null;
  selectLocation: (location: GeoLocation) => void;
  locateDevice: () => void;
  retry: () => void;
}

const ClimateContext = createContext<ClimateContextValue | null>(null);

/**
 * Owns the single shared "selected location → fetched climate data" pipeline
 * so every route (dashboard, forecast, safety, about) sees the same city and
 * the same in-flight/cached data instead of each page re-fetching on its own.
 */
export function ClimateProvider({ children }: { children: ReactNode }) {
  const [location, setLocationState] = useState<GeoLocation | null>(null);
  const [climateData, setClimateData] = useState<ClimateData | null>(null);
  const [unit, setUnitState] = useState<"C" | "F">(() => {
    if (typeof window === "undefined") return "C";
    try {
      const stored = localStorage.getItem(UNIT_STORAGE_KEY);
      return stored === "F" ? "F" : "C";
    } catch {
      return "C";
    }
  });
  const [isLoading, setIsLoading] = useState(true);
  const [isLocating, setIsLocating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [dataSource, setDataSource] = useState<DataSource | null>(null);
  const [insight, setInsight] = useState<BackendInsight | null>(null);
  const [backendRisk, setBackendRisk] = useState<ReturnType<typeof riskFromBackend>>(null);
  const [fallbackReason, setFallbackReason] = useState<BackendFailure | null>(null);
  const latestRequest = useRef(0); // ignore answers for a location the user has already left
  const { version: authVersion } = useAuth();

  const locateDevice = useCallback(() => {
    if (typeof navigator === "undefined" || !navigator.geolocation) {
      setLocationState(DEFAULT_LOCATION);
      return;
    }
    setIsLocating(true);
    navigator.geolocation.getCurrentPosition(
      (position) => {
        setLocationState({
          id: -1,
          name: "My Location",
          country: "",
          latitude: position.coords.latitude,
          longitude: position.coords.longitude,
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        });
        setIsLocating(false);
      },
      () => {
        setLocationState(DEFAULT_LOCATION);
        setIsLocating(false);
      },
      { timeout: 8000, maximumAge: 5 * 60 * 1000 },
    );
  }, []);

  useEffect(() => {
    // Synchronizes with the browser Geolocation API on mount — genuinely async
    // and can't be expressed as a lazy initializer or derived render value.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    locateDevice();
  }, [locateDevice]);

  const loadClimateData = useCallback(async (loc: GeoLocation) => {
    const requestId = ++latestRequest.current;
    const isCurrent = () => requestId === latestRequest.current;
    setIsLoading(true);
    setError(null);

    // 1. The backend: cleaned data, a trained heatwave model, risk reasoning and alerts.
    let reason: BackendFailure = "unavailable";
    try {
      const result = await backend.fetchClimate(loc);
      if (!isCurrent()) return;
      if (result.ok) {
        const data = climateFromBackend(result.climate);
        if (data) {
          setClimateData(data);
          setBackendRisk(riskFromBackend(result.climate));
          setInsight(insightFromBackend(result.climate, result.locationId));
          setDataSource("backend");
          setFallbackReason(null);
          setLastUpdated(new Date(result.climate.weather.fetchedAt));
          setIsLoading(false);
          return;
        }
        reason = "bad_response";
      } else {
        reason = result.reason;
      }
    } catch {
      if (!isCurrent()) return;
    }

    // 2. Fallback: fetch Open-Meteo directly and compute everything in the browser, as before.
    setDataSource("local");
    setInsight(null);
    setBackendRisk(null);
    setFallbackReason(reason);

    const cacheKey = climateCacheKey(loc.latitude, loc.longitude);
    const cached = readCache<ClimateData>(cacheKey);
    if (cached) {
      setClimateData(cached.value);
      setLastUpdated(new Date(cached.timestamp));
      setIsLoading(false);
    }

    try {
      const data = await fetchClimateData(loc.latitude, loc.longitude);
      if (!isCurrent()) return;
      setClimateData(data);
      setLastUpdated(new Date());
      writeCache(cacheKey, data);
    } catch (err) {
      if (isCurrent() && !cached) setError(describeError(err));
    } finally {
      if (isCurrent()) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    // Synchronizes with the remote climate API whenever the selected location
    // changes — an unavoidable async network fetch, not derivable state.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (location) loadClimateData(location);
  }, [location, loadClimateData]);

  // Signing in or out changes what the backend lets this person do (adding a new city needs an
  // account), so look again. The first value is the initial one: the load above already covers it.
  const seenAuthVersion = useRef(authVersion);
  useEffect(() => {
    if (seenAuthVersion.current === authVersion) return;
    seenAuthVersion.current = authVersion;
    backend.reset();
    // Synchronizes with the remote climate API after the signed-in user changed: the same
    // unavoidable async fetch as the effect above, not derivable state.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (location) void loadClimateData(location);
  }, [authVersion, location, loadClimateData]);

  function selectLocation(next: GeoLocation) {
    setLocationState(next);
  }

  function setUnit(next: "C" | "F") {
    setUnitState(next);
    try {
      localStorage.setItem(UNIT_STORAGE_KEY, next);
    } catch {
      // best-effort persistence only
    }
  }

  function retry() {
    backend.reset(); // an explicit retry should really try the backend again
    if (location) loadClimateData(location);
  }

  // The backend's verdict (which also knows yesterday and the heatwave probability) wins when present;
  // otherwise the same engine as always computes it here.
  const dailyForecast = climateData ? (backendRisk?.dailyForecast ?? buildDailyRiskForecast(climateData.daily)) : [];

  const assessment = !climateData
    ? null
    : (backendRisk?.assessment ??
      assessHeatwave(
        climateData.current.temperature2m,
        climateData.current.apparentTemperature,
        climateData.current.relativeHumidity2m,
        dailyForecast.map((d) => d.apparentTempMax),
      ));

  const value: ClimateContextValue = {
    location,
    climateData,
    dailyForecast,
    assessment,
    unit,
    setUnit,
    isLoading,
    isLocating,
    error,
    lastUpdated,
    dataSource,
    insight,
    fallbackReason,
    selectLocation,
    locateDevice,
    retry,
  };

  return <ClimateContext.Provider value={value}>{children}</ClimateContext.Provider>;
}

export function useClimate(): ClimateContextValue {
  const ctx = useContext(ClimateContext);
  if (!ctx) throw new Error("useClimate must be used within a ClimateProvider");
  return ctx;
}
