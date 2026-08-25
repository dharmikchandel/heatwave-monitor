"use client";

import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { ApiError, climateCacheKey, fetchClimateData, readCache, writeCache } from "./api";
import { assessHeatwave, buildDailyRiskForecast } from "./heatwaveEngine";
import type { ClimateData, DailyRiskForecast, GeoLocation, HeatwaveAssessment } from "./types";

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
    setIsLoading(true);
    setError(null);

    const cacheKey = climateCacheKey(loc.latitude, loc.longitude);
    const cached = readCache<ClimateData>(cacheKey);
    if (cached) {
      setClimateData(cached.value);
      setLastUpdated(new Date(cached.timestamp));
      setIsLoading(false);
    }

    try {
      const data = await fetchClimateData(loc.latitude, loc.longitude);
      setClimateData(data);
      setLastUpdated(new Date());
      writeCache(cacheKey, data);
    } catch (err) {
      if (!cached) setError(describeError(err));
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    // Synchronizes with the remote climate API whenever the selected location
    // changes — an unavoidable async network fetch, not derivable state.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (location) loadClimateData(location);
  }, [location, loadClimateData]);

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
    if (location) loadClimateData(location);
  }

  const dailyForecast = climateData ? buildDailyRiskForecast(climateData.daily) : [];

  const assessment = climateData
    ? assessHeatwave(
        climateData.current.temperature2m,
        climateData.current.apparentTemperature,
        climateData.current.relativeHumidity2m,
        dailyForecast.map((d) => d.apparentTempMax),
      )
    : null;

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
