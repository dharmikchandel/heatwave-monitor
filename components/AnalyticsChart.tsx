"use client";

import { useMemo, useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { ClimateData } from "@/lib/types";
import { celsiusToUnit, cn, findHourlyIndex, FOCUS_RING, formatDateLong, formatHourLabel } from "@/lib/utils";

interface AnalyticsChartProps {
  climateData: ClimateData;
  unit: "C" | "F";
}

type ChartView = "hourly" | "7day";

interface ChartPoint {
  label: string;
  temperature: number;
  heatIndex: number;
  humidity: number;
}

function CustomTooltip({
  active,
  payload,
  label,
  unit,
}: {
  active?: boolean;
  payload?: { name: string; value: number; color: string }[];
  label?: string;
  unit: "C" | "F";
}) {
  if (!active || !payload || payload.length === 0) return null;
  return (
    <div className="glass-card rounded-xl px-3 py-2 text-xs shadow-xl">
      <p className="mb-1 font-semibold">{label}</p>
      {payload.map((entry) => (
        <p key={entry.name} className="flex items-center gap-1.5" style={{ color: entry.color }}>
          <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: entry.color }} />
          {entry.name}: <span className="font-semibold">{entry.value.toFixed(1)}{entry.name === "Humidity" ? "%" : `°${unit}`}</span>
        </p>
      ))}
    </div>
  );
}

export default function AnalyticsChart({ climateData, unit }: AnalyticsChartProps) {
  const [view, setView] = useState<ChartView>("hourly");

  const hourlyData: ChartPoint[] = useMemo(() => {
    const { hourly, current } = climateData;
    const startIdx = findHourlyIndex(hourly.time, current.time);
    const slice = hourly.time.slice(startIdx, startIdx + 24);
    return slice.map((time, i) => {
      const idx = startIdx + i;
      return {
        label: formatHourLabel(time),
        temperature: celsiusToUnit(hourly.temperature2m[idx], unit),
        heatIndex: celsiusToUnit(hourly.heatIndex[idx], unit),
        humidity: hourly.relativeHumidity2m[idx],
      };
    });
  }, [climateData, unit]);

  const dailyData: ChartPoint[] = useMemo(() => {
    const { daily } = climateData;
    return daily.time.map((time, i) => ({
      label: formatDateLong(time),
      temperature: celsiusToUnit(daily.temperature2mMax[i], unit),
      heatIndex: celsiusToUnit(daily.apparentTemperatureMax[i], unit),
      humidity: 0,
    }));
  }, [climateData, unit]);

  const data = view === "hourly" ? hourlyData : dailyData;

  const temps = data.map((d) => d.temperature);
  const heatIndexes = data.map((d) => d.heatIndex);
  const chartSummary =
    data.length > 0
      ? `${view === "hourly" ? "Hourly" : "7-day"} chart. Temperature ranges from ${Math.round(Math.min(...temps))} to ${Math.round(Math.max(...temps))}°${unit}. Heat index ranges from ${Math.round(Math.min(...heatIndexes))} to ${Math.round(Math.max(...heatIndexes))}°${unit}.`
      : "No chart data available.";

  return (
    <div className="glass-card rounded-2xl p-4 sm:p-6">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-bold uppercase tracking-wide text-muted">Thermal Analytics</h2>
        <div className="flex items-center rounded-full border border-surface-border bg-surface/60 p-0.5 text-xs font-semibold" role="group" aria-label="Chart time range">
          {([
            { key: "hourly", label: "Hourly (24h)" },
            { key: "7day", label: "7-Day Forecast" },
          ] as const).map((option) => (
            <button
              key={option.key}
              type="button"
              onClick={() => setView(option.key)}
              aria-pressed={view === option.key}
              className={cn(
                "rounded-full px-3 py-1.5 transition",
                view === option.key ? "bg-orange-600 text-white shadow-sm" : "text-muted hover:text-foreground",
                FOCUS_RING,
              )}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      <div className="h-72 w-full sm:h-80" role="img" aria-label={chartSummary}>
        <ResponsiveContainer width="100%" height="100%" aria-hidden="true">
          <AreaChart data={data} margin={{ top: 8, right: 8, left: -18, bottom: 0 }}>
            <defs>
              <linearGradient id="tempGradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#EA580C" stopOpacity={0.45} />
                <stop offset="95%" stopColor="#EA580C" stopOpacity={0} />
              </linearGradient>
              <linearGradient id="heatIndexGradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#DC2626" stopOpacity={0.35} />
                <stop offset="95%" stopColor="#DC2626" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--grid-line)" vertical={false} />
            <XAxis
              dataKey="label"
              tick={{ fontSize: 11, fill: "var(--muted)" }}
              axisLine={{ stroke: "var(--grid-line)" }}
              tickLine={false}
              interval={view === "hourly" ? 2 : 0}
            />
            <YAxis
              tick={{ fontSize: 11, fill: "var(--muted)" }}
              axisLine={false}
              tickLine={false}
              width={36}
              tickFormatter={(v: number) => `${Math.round(v)}°`}
            />
            <Tooltip content={<CustomTooltip unit={unit} />} />
            <Legend wrapperStyle={{ fontSize: 12 }} iconType="circle" />
            <Area
              type="monotone"
              dataKey="temperature"
              name="Temperature"
              stroke="#EA580C"
              strokeWidth={2.5}
              fill="url(#tempGradient)"
              dot={false}
              activeDot={{ r: 4 }}
            />
            <Area
              type="monotone"
              dataKey="heatIndex"
              name="Heat Index"
              stroke="#DC2626"
              strokeWidth={2.5}
              fill="url(#heatIndexGradient)"
              dot={false}
              activeDot={{ r: 4 }}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
