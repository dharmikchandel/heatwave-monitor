"use client";

import { AlertCircle, FileDown, RefreshCw } from "lucide-react";
import { useState } from "react";
import AlertBanner from "@/components/AlertBanner";
import AnalyticsChart from "@/components/AnalyticsChart";
import ExportReportModal from "@/components/ExportReportModal";
import Forecast7Day from "@/components/Forecast7Day";
import HeatwaveRiskCard from "@/components/HeatwaveRiskCard";
import MetricsGrid from "@/components/MetricsGrid";
import SafetyAdvisory from "@/components/SafetyAdvisory";
import { useClimate } from "@/lib/ClimateContext";
import { cn, FOCUS_RING } from "@/lib/utils";

export default function Home() {
  const { location, climateData, dailyForecast, assessment, unit, isLoading, error, retry } = useClimate();
  const [isExportOpen, setIsExportOpen] = useState(false);

  const locationLabel = location
    ? `${location.name}${location.admin1 ? `, ${location.admin1}` : ""}${location.country ? `, ${location.country}` : ""}`
    : "";

  return (
    <>
      {error && (
        <div role="alert" className="flex items-center justify-between gap-3 rounded-2xl border border-red-600/30 bg-red-600/10 p-4">
          <div className="flex items-center gap-3">
            <AlertCircle className="h-5 w-5 shrink-0 text-red-600 dark:text-red-500" aria-hidden="true" />
            <p className="text-sm text-foreground/90">{error}</p>
          </div>
          <button
            type="button"
            onClick={retry}
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
                <h2 className="text-sm font-bold uppercase tracking-wide text-muted">{locationLabel || "Current Conditions"}</h2>
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
