"use client";

import { AnimatePresence, motion, useReducedMotion } from "framer-motion";
import { AlertTriangle, Flame } from "lucide-react";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import type { DailyRiskForecast, HeatwaveAssessment } from "@/lib/types";
import { formatDayLabel } from "@/lib/utils";

interface AlertBannerProps {
  assessment: HeatwaveAssessment;
  dailyForecast: DailyRiskForecast[];
  locationName: string;
}

export default function AlertBanner({ assessment, dailyForecast, locationName }: AlertBannerProps) {
  const prefersReducedMotion = useReducedMotion();
  const warningDays = dailyForecast.filter((d) => d.riskLevel === "danger" || d.riskLevel === "extreme-danger");
  const extremeDays = dailyForecast.filter((d) => d.riskLevel === "extreme-danger");

  if (warningDays.length === 0) return null;

  const isExtreme = extremeDays.length > 0;
  const dayLabels = warningDays
    .map((d) => formatDayLabel(d.date, dailyForecast.findIndex((x) => x.date === d.date)))
    .join(", ");

  return (
    <AnimatePresence>
      <motion.div
        initial={{ opacity: 0, height: 0 }}
        animate={{ opacity: 1, height: "auto" }}
        exit={{ opacity: 0, height: 0 }}
        className="overflow-hidden"
      >
        <div
          role="alert"
          className={`relative flex items-start gap-3 overflow-hidden rounded-2xl border p-4 sm:items-center ${
            isExtreme
              ? "border-red-800/50 bg-gradient-to-r from-red-900/40 via-red-800/25 to-red-900/40"
              : "border-red-600/40 bg-gradient-to-r from-red-600/20 via-orange-600/15 to-red-600/20"
          }`}
        >
          <motion.div
            className="absolute inset-0 -z-10"
            animate={prefersReducedMotion ? { opacity: 0.2 } : { opacity: [0.15, 0.35, 0.15] }}
            transition={prefersReducedMotion ? undefined : { duration: 2.4, repeat: Infinity, ease: "easeInOut" }}
            style={{
              background: isExtreme
                ? "radial-gradient(circle at 20% 50%, rgba(153,27,27,0.6), transparent 60%)"
                : "radial-gradient(circle at 20% 50%, rgba(220,38,38,0.5), transparent 60%)",
            }}
          />

          <motion.div
            animate={prefersReducedMotion ? { scale: 1 } : { scale: [1, 1.12, 1] }}
            transition={prefersReducedMotion ? undefined : { duration: 1.6, repeat: Infinity, ease: "easeInOut" }}
            className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl ${
              isExtreme ? "bg-red-700 text-white" : "bg-red-600 text-white"
            }`}
          >
            {isExtreme ? <Flame className="h-5 w-5" aria-hidden="true" /> : <AlertTriangle className="h-5 w-5" aria-hidden="true" />}
          </motion.div>

          <div className="min-w-0 flex-1">
            <p className="text-sm font-bold tracking-tight text-red-600 dark:text-red-400 sm:text-base">
              {isExtreme ? "Extreme Heatwave Alert" : "Heatwave Early Warning"} — {RISK_LEVEL_LABEL[assessment.riskLevel]}
            </p>
            <p className="mt-0.5 text-sm text-foreground/90">
              {assessment.message} Elevated risk expected in <span className="font-semibold">{locationName}</span> on{" "}
              <span className="font-semibold">{dayLabels}</span>.
            </p>
          </div>
        </div>
      </motion.div>
    </AnimatePresence>
  );
}
