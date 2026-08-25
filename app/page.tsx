"use client";

import { AlertCircle, FileDown, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import AlertBanner from "@/components/AlertBanner";
import AnalyticsChart from "@/components/AnalyticsChart";
import ExportReportModal from "@/components/ExportReportModal";
import Forecast7Day from "@/components/Forecast7Day";
import Header from "@/components/Header";
import HeatwaveRiskCard from "@/components/HeatwaveRiskCard";
import MetricsGrid from "@/components/MetricsGrid";
import SafetyAdvisory from "@/components/SafetyAdvisory";
import { climateCacheKey, fetchClimateData, readCache, writeCache } from "@/lib/api";
import { assessHeatwave, buildDailyRiskForecast } from "@/lib/heatwaveEngine";
import type { ClimateData, GeoLocation } from "@/lib/types";
import { cn, FOCUS_RING } from "@/lib/utils";

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

export default function Home() {
  const [location, setLocation] = useState<GeoLocation | null>(null);
  const [climateData, setClimateData] = useState<ClimateData | null>(null);
  const [unit, setUnit] = useState<"C" | "F">(() => {
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
  const [isExportOpen, setIsExportOpen] = useState(false);

  const resolveInitialLocation = useCallback(() => {
    if (typeof navigator === "undefined" || !navigator.geolocation) {
      setLocation(DEFAULT_LOCATION);
      return;
    }
    setIsLocating(true);
    navigator.geolocation.getCurrentPosition(
      (position) => {
        setLocation({
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
        setLocation(DEFAULT_LOCATION);
        setIsLocating(false);
      },
      { timeout: 8000, maximumAge: 5 * 60 * 1000 },
    );
  }, []);

  useEffect(() => {
    // Synchronizes with the browser Geolocation API on mount — genuinely async
    // and can't be expressed as a lazy initializer or derived render value.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    resolveInitialLocation();
  }, [resolveInitialLocation]);

  const loadClimateData = useCallback(async (loc: GeoLocation) => {
    setIsLoading(true);
    setError(null);

    const cacheKey = climateCacheKey(loc.latitude, loc.longitude);
    const cached = readCache<ClimateData>(cacheKey);
    if (cached) {
      setClimateData(cached);
      setIsLoading(false);
    }

    try {
      const data = await fetchClimateData(loc.latitude, loc.longitude);
      setClimateData(data);
      writeCache(cacheKey, data);
    } catch {
      if (!cached) setError("Couldn't reach the climate data service. Check your connection and try again.");
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

  function handleUnitChange(next: "C" | "F") {
    setUnit(next);
    try {
      localStorage.setItem(UNIT_STORAGE_KEY, next);
    } catch {
      // best-effort persistence only
    }
  }

  const dailyForecast = useMemo(() => (climateData ? buildDailyRiskForecast(climateData.daily) : []), [climateData]);

  const assessment = climateData
    ? assessHeatwave(
        climateData.current.temperature2m,
        climateData.current.apparentTemperature,
        climateData.current.relativeHumidity2m,
        dailyForecast.map((d) => d.apparentTempMax),
      )
    : null;

  const locationLabel = location
    ? `${location.name}${location.admin1 ? `, ${location.admin1}` : ""}${location.country ? `, ${location.country}` : ""}`
    : "";

  return (
    <>
      <Header
        currentLocation={location}
        onLocationSelect={setLocation}
        onUseMyLocation={resolveInitialLocation}
        isLocating={isLocating}
        unit={unit}
        onUnitChange={handleUnitChange}
      />

      <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col gap-5 px-4 py-6 sm:px-6 lg:px-8">
        {error && (
          <div role="alert" className="flex items-center justify-between gap-3 rounded-2xl border border-red-600/30 bg-red-600/10 p-4">
            <div className="flex items-center gap-3">
              <AlertCircle className="h-5 w-5 shrink-0 text-red-600 dark:text-red-500" aria-hidden="true" />
              <p className="text-sm text-foreground/90">{error}</p>
            </div>
            <button
              type="button"
              onClick={() => location && loadClimateData(location)}
              className={cn(
                "flex shrink-0 items-center gap-1.5 rounded-full bg-red-600 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-red-500",
                FOCUS_RING,
              )}
            >
              <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" /> Retry
            </button>
          </div>
        )}

        {!climateData && isLoading && <LoadingSkeleton />}

        {climateData && assessment && (
          <>
            <AlertBanner assessment={assessment} dailyForecast={dailyForecast} locationName={location?.name ?? "your area"} />

            <div className="grid grid-cols-1 gap-5 lg:grid-cols-[320px_1fr]">
              <HeatwaveRiskCard assessment={assessment} unit={unit} />

              <div className="flex flex-col gap-5">
                <div className="flex items-center justify-between">
                  <h2 className="text-sm font-bold uppercase tracking-wide text-muted">
                    {locationLabel || "Current Conditions"}
                  </h2>
                  <button
                    type="button"
                    onClick={() => setIsExportOpen(true)}
                    className={cn(
                      "flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3 py-1.5 text-xs font-semibold transition hover:bg-surface",
                      FOCUS_RING,
                    )}
                  >
                    <FileDown className="h-3.5 w-3.5" aria-hidden="true" /> Export Report
                  </button>
                </div>
                <MetricsGrid climateData={climateData} heatIndexC={assessment.heatIndexC} unit={unit} />
              </div>
            </div>

            <AnalyticsChart climateData={climateData} unit={unit} />
            <Forecast7Day dailyForecast={dailyForecast} unit={unit} />
            <SafetyAdvisory riskLevel={assessment.riskLevel} />

            <ExportReportModal
              isOpen={isExportOpen}
              onClose={() => setIsExportOpen(false)}
              locationName={location?.name ?? "Unknown"}
              assessment={assessment}
              climateData={climateData}
              unit={unit}
            />
          </>
        )}
      </main>

      <footer className="mx-auto w-full max-w-7xl px-4 py-6 text-center text-xs text-muted sm:px-6 lg:px-8">
        Climate data from Open-Meteo. All analytics computed locally in your browser — no backend, no tracking.
      </footer>
    </>
  );
}

function LoadingSkeleton() {
  return (
    <div className="flex flex-col gap-5">
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[320px_1fr]">
        <div className="glass-card shimmer h-[420px] rounded-2xl" />
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          {Array.from({ length: 5 }).map((_, i) => (
            <div key={i} className="glass-card shimmer h-32 rounded-2xl" />
          ))}
        </div>
      </div>
      <div className="glass-card shimmer h-80 rounded-2xl" />
      <div className="glass-card shimmer h-40 rounded-2xl" />
    </div>
  );
}
