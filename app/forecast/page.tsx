"use client";

import { motion } from "framer-motion";
import { AlertCircle, Minus, TrendingDown, TrendingUp } from "lucide-react";
import AnalyticsChart from "@/components/AnalyticsChart";
import Forecast7Day from "@/components/Forecast7Day";
import { computeTrendAnomaly } from "@/lib/heatwaveEngine";
import { useClimate } from "@/lib/ClimateContext";
import { celsiusToUnit, findHourlyIndex, formatHourLabel } from "@/lib/utils";

export default function ForecastPage() {
  const { location, climateData, dailyForecast, unit, isLoading, error, retry } = useClimate();

  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Extended Forecast &amp; Trend Analysis</h1>
        <p className="mt-1 text-sm text-muted">
          A deeper look at {location?.name ?? "your location"}&apos;s thermal trajectory — hourly detail, the full 7-day
          outlook, and whether temperatures are trending upward relative to the recent baseline.
        </p>
      </div>

      {error && (
        <div role="alert" className="flex items-center justify-between gap-3 rounded-2xl border border-red-600/30 bg-red-600/10 p-4">
          <div className="flex items-center gap-3">
            <AlertCircle className="h-5 w-5 shrink-0 text-red-600 dark:text-red-500" aria-hidden="true" />
            <p className="text-sm text-foreground/90">{error}</p>
          </div>
          <button
            type="button"
            onClick={retry}
            className="flex shrink-0 items-center gap-1.5 rounded-full bg-red-600 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-red-500"
          >
            Retry
          </button>
        </div>
      )}

      {!climateData && isLoading && (
        <div className="flex flex-col gap-5">
          <div className="glass-card shimmer h-24 rounded-2xl" />
          <div className="glass-card shimmer h-80 rounded-2xl" />
          <div className="glass-card shimmer h-64 rounded-2xl" />
        </div>
      )}

      {climateData && (
        <>
          <TrendAnomalyCard dailyTempMax={climateData.daily.temperature2mMax} unit={unit} />
          <AnalyticsChart climateData={climateData} unit={unit} />
          <Forecast7Day dailyForecast={dailyForecast} unit={unit} />
          <HourlyTable climateData={climateData} unit={unit} />
        </>
      )}
    </>
  );
}

function TrendAnomalyCard({ dailyTempMax, unit }: { dailyTempMax: number[]; unit: "C" | "F" }) {
  const { isRising, anomalyC } = computeTrendAnomaly(dailyTempMax);
  const anomaly = unit === "F" ? (anomalyC * 9) / 5 : anomalyC;
  const Icon = Math.abs(anomalyC) < 0.5 ? Minus : isRising ? TrendingUp : TrendingDown;
  const tone = Math.abs(anomalyC) < 0.5 ? "text-muted" : isRising ? "text-red-600 dark:text-red-400" : "text-cyan-600 dark:text-cyan-400";

  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      className="glass-card flex items-center gap-4 rounded-2xl p-4 sm:p-5"
    >
      <div className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-surface ${tone}`}>
        <Icon className="h-5 w-5" aria-hidden="true" />
      </div>
      <div>
        <p className="text-sm font-bold">
          {Math.abs(anomalyC) < 0.5
            ? "Temperatures are holding steady"
            : isRising
              ? "Temperatures are trending upward"
              : "Temperatures are trending downward"}
        </p>
        <p className="mt-0.5 text-xs text-muted">
          Today&apos;s forecast high is {anomaly > 0 ? "+" : ""}
          {anomaly.toFixed(1)}°{unit} versus the rolling baseline of the preceding days in this 7-day window.
        </p>
      </div>
    </motion.div>
  );
}

function HourlyTable({ climateData, unit }: { climateData: NonNullable<ReturnType<typeof useClimate>["climateData"]>; unit: "C" | "F" }) {
  const { hourly, current } = climateData;
  const startIdx = findHourlyIndex(hourly.time, current.time);
  const rows = hourly.time.slice(startIdx, startIdx + 24).map((time, i) => {
    const idx = startIdx + i;
    return {
      time: formatHourLabel(time),
      temp: celsiusToUnit(hourly.temperature2m[idx], unit),
      feelsLike: celsiusToUnit(hourly.apparentTemperature[idx], unit),
      humidity: hourly.relativeHumidity2m[idx],
      heatIndex: celsiusToUnit(hourly.heatIndex[idx], unit),
    };
  });

  return (
    <div className="glass-card rounded-2xl p-4 sm:p-6">
      <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">Hourly Detail (Next 24h)</h2>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[480px] text-left text-sm">
          <thead>
            <tr className="border-b border-surface-border text-xs text-muted">
              <th scope="col" className="py-2 pr-3 font-medium">
                Time
              </th>
              <th scope="col" className="py-2 pr-3 font-medium">
                Temp
              </th>
              <th scope="col" className="py-2 pr-3 font-medium">
                Feels Like
              </th>
              <th scope="col" className="py-2 pr-3 font-medium">
                Humidity
              </th>
              <th scope="col" className="py-2 font-medium">
                Heat Index
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={row.time + i} className="border-b border-surface-border/60 last:border-0">
                <td className="py-2 pr-3 font-medium">{row.time}</td>
                <td className="py-2 pr-3 text-muted">{Math.round(row.temp)}°{unit}</td>
                <td className="py-2 pr-3 text-muted">{Math.round(row.feelsLike)}°{unit}</td>
                <td className="py-2 pr-3 text-muted">{Math.round(row.humidity)}%</td>
                <td className="py-2 text-muted">{Math.round(row.heatIndex)}°{unit}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
