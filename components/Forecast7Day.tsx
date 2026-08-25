"use client";

import { motion } from "framer-motion";
import { CloudRain, Sun } from "lucide-react";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import type { DailyRiskForecast } from "@/lib/types";
import { celsiusToUnit, cn, formatDayLabel, RISK_LEVEL_BG_CLASS, RISK_LEVEL_COLOR } from "@/lib/utils";

interface Forecast7DayProps {
  dailyForecast: DailyRiskForecast[];
  unit: "C" | "F";
}

export default function Forecast7Day({ dailyForecast, unit }: Forecast7DayProps) {
  return (
    <div className="glass-card rounded-2xl p-4 sm:p-6">
      <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">7-Day Heatwave Outlook</h2>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-7">
        {dailyForecast.map((day, i) => {
          const dayLabel = formatDayLabel(day.date, i);
          return (
            <motion.div
              key={day.date}
              initial={{ opacity: 0, y: 12 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.3, delay: i * 0.05 }}
              role="group"
              aria-label={`${dayLabel}: high ${Math.round(celsiusToUnit(day.tempMax, unit))}, low ${Math.round(celsiusToUnit(day.tempMin, unit))} degrees ${unit === "C" ? "Celsius" : "Fahrenheit"}, risk ${RISK_LEVEL_LABEL[day.riskLevel]}, UV index ${day.uvIndexMax.toFixed(1)}`}
              className={cn("flex flex-col items-center gap-2 rounded-xl border p-3 text-center", RISK_LEVEL_BG_CLASS[day.riskLevel])}
            >
              <p className="text-xs font-bold">{dayLabel}</p>
              <div
                className="flex h-8 w-8 items-center justify-center rounded-full"
                style={{ backgroundColor: `${RISK_LEVEL_COLOR[day.riskLevel]}22`, color: RISK_LEVEL_COLOR[day.riskLevel] }}
                aria-hidden="true"
              >
                {day.precipitationSum > 1 ? <CloudRain className="h-4 w-4" /> : <Sun className="h-4 w-4" />}
              </div>
              <div className="flex items-baseline gap-1" aria-hidden="true">
                <span className="text-sm font-bold">{Math.round(celsiusToUnit(day.tempMax, unit))}°</span>
                <span className="text-xs text-muted">{Math.round(celsiusToUnit(day.tempMin, unit))}°</span>
              </div>
              <span className="text-[10px] font-semibold uppercase tracking-wide" aria-hidden="true">
                {RISK_LEVEL_LABEL[day.riskLevel]}
              </span>
              <span className="text-[10px] text-muted" aria-hidden="true">
                UV {day.uvIndexMax.toFixed(1)}
              </span>
            </motion.div>
          );
        })}
      </div>
    </div>
  );
}
