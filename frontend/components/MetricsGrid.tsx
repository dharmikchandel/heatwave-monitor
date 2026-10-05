"use client";

import { motion } from "framer-motion";
import { Droplets, Flame, Sun, Thermometer, ThermometerSun } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ClimateData } from "@/lib/types";
import { celsiusToUnit, cn, findHourlyIndex } from "@/lib/utils";
import AnimatedNumber from "./AnimatedNumber";

interface MetricsGridProps {
  climateData: ClimateData;
  heatIndexC: number;
  unit: "C" | "F";
}

interface MetricCard {
  key: string;
  label: string;
  icon: LucideIcon;
  value: number;
  displayUnit: string;
  decimals?: number;
  trend?: number | null;
  accent: string;
}

export default function MetricsGrid({ climateData, heatIndexC, unit }: MetricsGridProps) {
  const { current, hourly, daily } = climateData;
  const idx = findHourlyIndex(hourly.time, current.time);
  const prevIdx = idx > 0 ? idx - 1 : null;

  const trendFor = (series: number[]) => (prevIdx !== null ? series[idx] - series[prevIdx] : null);

  const cards: MetricCard[] = [
    {
      key: "temp",
      label: "Current Temp",
      icon: Thermometer,
      value: celsiusToUnit(current.temperature2m, unit),
      displayUnit: `°${unit}`,
      decimals: 1,
      trend: trendFor(hourly.temperature2m.map((c) => celsiusToUnit(c, unit))),
      accent: "text-orange-500 bg-orange-500/10",
    },
    {
      key: "feels-like",
      label: "Feels Like",
      icon: ThermometerSun,
      value: celsiusToUnit(current.apparentTemperature, unit),
      displayUnit: `°${unit}`,
      decimals: 1,
      trend: trendFor(hourly.apparentTemperature.map((c) => celsiusToUnit(c, unit))),
      accent: "text-red-500 bg-red-500/10",
    },
    {
      key: "humidity",
      label: "Humidity",
      icon: Droplets,
      value: current.relativeHumidity2m,
      displayUnit: "%",
      trend: trendFor(hourly.relativeHumidity2m),
      accent: "text-cyan-500 bg-cyan-500/10",
    },
    {
      key: "uv",
      label: "UV Index (Today)",
      icon: Sun,
      value: daily.uvIndexMax[0] ?? 0,
      displayUnit: "",
      decimals: 1,
      trend: null,
      accent: "text-amber-400 bg-amber-400/10",
    },
    {
      key: "heat-index",
      label: "Heat Index",
      icon: Flame,
      value: celsiusToUnit(heatIndexC, unit),
      displayUnit: `°${unit}`,
      decimals: 1,
      trend: trendFor(hourly.heatIndex.map((c) => celsiusToUnit(c, unit))),
      accent: "text-rose-500 bg-rose-500/10",
    },
  ];

  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      {cards.map((card, i) => (
        <motion.div
          key={card.key}
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.35, delay: i * 0.05 }}
          className="glass-card flex flex-col gap-3 rounded-2xl p-4"
        >
          <div className="flex items-center justify-between">
            <div className={cn("flex h-9 w-9 items-center justify-center rounded-lg", card.accent)} aria-hidden="true">
              <card.icon className="h-5 w-5" />
            </div>
            {card.trend !== null && card.trend !== undefined && Math.abs(card.trend) >= 0.1 && (
              <span
                className={cn(
                  "text-xs font-semibold",
                  card.trend > 0 ? "text-red-600 dark:text-red-400" : "text-cyan-600 dark:text-cyan-400",
                )}
                aria-label={`${card.trend > 0 ? "up" : "down"} ${Math.abs(card.trend).toFixed(1)} from last hour`}
              >
                {card.trend > 0 ? "▲" : "▼"} {Math.abs(card.trend).toFixed(1)}
              </span>
            )}
          </div>
          <div>
            <p className="text-xs font-medium text-muted">{card.label}</p>
            <p className="mt-0.5 text-2xl font-bold tracking-tight">
              <AnimatedNumber value={card.value} decimals={card.decimals ?? 0} />
              <span className="ml-0.5 text-base font-semibold text-muted">{card.displayUnit}</span>
            </p>
          </div>
        </motion.div>
      ))}
    </div>
  );
}
