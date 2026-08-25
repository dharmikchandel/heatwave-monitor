import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import type { HeatRiskLevel } from "./types";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Shared focus-visible ring, applied to every interactive element for keyboard-navigation clarity. */
export const FOCUS_RING =
  "outline-none focus-visible:ring-2 focus-visible:ring-orange-500 focus-visible:ring-offset-2 focus-visible:ring-offset-background";

export function formatTemperature(tempC: number, unit: "C" | "F" = "C"): string {
  const value = unit === "F" ? (tempC * 9) / 5 + 32 : tempC;
  return `${Math.round(value)}°${unit}`;
}

export function celsiusToUnit(tempC: number, unit: "C" | "F"): number {
  return unit === "F" ? (tempC * 9) / 5 + 32 : tempC;
}

export function formatDayLabel(isoDate: string, index: number): string {
  if (index === 0) return "Today";
  const date = new Date(`${isoDate}T00:00:00`);
  return date.toLocaleDateString("en-US", { weekday: "short" });
}

export function formatDateLong(isoDate: string): string {
  const date = new Date(`${isoDate}T00:00:00`);
  return date.toLocaleDateString("en-US", { month: "short", day: "numeric" });
}

export function formatHourLabel(isoDateTime: string): string {
  const date = new Date(isoDateTime);
  return date.toLocaleTimeString("en-US", { hour: "numeric", hour12: true });
}

/** Finds the hourly-array index that corresponds to (or immediately follows) `currentTime`. */
export function findHourlyIndex(times: string[], currentTime: string): number {
  const exact = times.indexOf(currentTime);
  if (exact !== -1) return exact;
  const currentMs = new Date(currentTime).getTime();
  let idx = times.findIndex((t) => new Date(t).getTime() > currentMs);
  if (idx === -1) idx = times.length - 1;
  return Math.max(0, idx);
}

export function formatClock(date: Date, timezone?: string): string {
  return date.toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    timeZone: timezone,
  });
}

export const RISK_LEVEL_COLOR: Record<HeatRiskLevel, string> = {
  normal: "#10B981",
  caution: "#F59E0B",
  "extreme-caution": "#EA580C",
  danger: "#DC2626",
  "extreme-danger": "#991B1B",
};

// Text-color pairs use a darker shade in light mode and the vivid shade in dark
// mode, since the vivid 400/500 shades alone fall short of WCAG AA (4.5:1)
// body-text contrast against a near-white background.
export const RISK_LEVEL_TEXT_CLASS: Record<HeatRiskLevel, string> = {
  normal: "text-emerald-700 dark:text-emerald-400",
  caution: "text-amber-700 dark:text-amber-400",
  "extreme-caution": "text-orange-700 dark:text-orange-400",
  danger: "text-red-700 dark:text-red-400",
  "extreme-danger": "text-red-800 dark:text-red-500",
};

export const RISK_LEVEL_BG_CLASS: Record<HeatRiskLevel, string> = {
  normal: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-emerald-500/30",
  caution: "bg-amber-500/15 text-amber-700 dark:text-amber-400 border-amber-500/30",
  "extreme-caution": "bg-orange-500/15 text-orange-700 dark:text-orange-400 border-orange-500/30",
  danger: "bg-red-500/15 text-red-700 dark:text-red-400 border-red-500/30",
  "extreme-danger": "bg-red-800/20 text-red-800 dark:text-red-400 border-red-700/40",
};

/** Maps Open-Meteo WMO weather codes to a short human-readable label. */
export function weatherCodeLabel(code: number): string {
  const map: Record<number, string> = {
    0: "Clear sky",
    1: "Mostly clear",
    2: "Partly cloudy",
    3: "Overcast",
    45: "Fog",
    48: "Rime fog",
    51: "Light drizzle",
    53: "Drizzle",
    55: "Dense drizzle",
    61: "Light rain",
    63: "Rain",
    65: "Heavy rain",
    71: "Light snow",
    73: "Snow",
    75: "Heavy snow",
    80: "Light showers",
    81: "Showers",
    82: "Violent showers",
    95: "Thunderstorm",
    96: "Thunderstorm w/ hail",
    99: "Severe thunderstorm",
  };
  return map[code] ?? "Unknown";
}
