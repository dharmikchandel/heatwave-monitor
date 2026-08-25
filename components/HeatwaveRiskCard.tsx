"use client";

import { motion, useReducedMotion } from "framer-motion";
import { AlertTriangle, Flame, ShieldAlert, ShieldCheck, ShieldQuestion } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { HEAT_RISK_THRESHOLDS_C, RISK_LEVEL_LABEL, RISK_LEVEL_ORDER } from "@/lib/heatwaveEngine";
import type { HeatRiskLevel, HeatwaveAssessment } from "@/lib/types";
import { celsiusToUnit, cn, RISK_LEVEL_COLOR, RISK_LEVEL_TEXT_CLASS } from "@/lib/utils";
import AnimatedNumber from "./AnimatedNumber";

interface HeatwaveRiskCardProps {
  assessment: HeatwaveAssessment;
  unit: "C" | "F";
}

const RISK_ICON: Record<HeatRiskLevel, LucideIcon> = {
  normal: ShieldCheck,
  caution: ShieldQuestion,
  "extreme-caution": ShieldAlert,
  danger: AlertTriangle,
  "extreme-danger": Flame,
};

const GAUGE_MIN = 15;
const GAUGE_MAX = HEAT_RISK_THRESHOLDS_C.extremeDanger;

const RADIUS = 76;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

export default function HeatwaveRiskCard({ assessment, unit }: HeatwaveRiskCardProps) {
  const { riskLevel, heatIndexC, message } = assessment;
  const color = RISK_LEVEL_COLOR[riskLevel];
  const Icon = RISK_ICON[riskLevel];
  const prefersReducedMotion = useReducedMotion();

  const percent = Math.min(1, Math.max(0, (heatIndexC - GAUGE_MIN) / (GAUGE_MAX - GAUGE_MIN)));
  const dashOffset = CIRCUMFERENCE * (1 - percent);
  const isSevere = riskLevel === "danger" || riskLevel === "extreme-danger";
  const heatIndexValue = celsiusToUnit(heatIndexC, unit);

  return (
    <div
      className="glass-card flex flex-col items-center gap-5 rounded-2xl p-6 text-center"
      role="img"
      aria-label={`Current heat risk: ${RISK_LEVEL_LABEL[riskLevel]}. Heat index ${Math.round(heatIndexValue)} degrees ${unit === "C" ? "Celsius" : "Fahrenheit"}. ${message}`}
    >
      <div className="flex w-full items-center justify-between" aria-hidden="true">
        <h2 className="text-sm font-bold uppercase tracking-wide text-muted">Current Heat Risk</h2>
        <motion.div
          animate={isSevere && !prefersReducedMotion ? { scale: [1, 1.08, 1] } : { scale: 1 }}
          transition={isSevere && !prefersReducedMotion ? { duration: 1.6, repeat: Infinity, ease: "easeInOut" } : undefined}
          className="flex h-7 w-7 items-center justify-center rounded-full"
          style={{ backgroundColor: `${color}26`, color }}
        >
          <Icon className="h-4 w-4" />
        </motion.div>
      </div>

      <div className="relative flex h-48 w-48 items-center justify-center" aria-hidden="true">
        <svg viewBox="0 0 176 176" className="h-full w-full -rotate-90">
          <circle cx="88" cy="88" r={RADIUS} fill="none" stroke="var(--grid-line)" strokeWidth="12" />
          <motion.circle
            cx="88"
            cy="88"
            r={RADIUS}
            fill="none"
            stroke={color}
            strokeWidth="12"
            strokeLinecap="round"
            strokeDasharray={CIRCUMFERENCE}
            initial={{ strokeDashoffset: prefersReducedMotion ? dashOffset : CIRCUMFERENCE }}
            animate={{ strokeDashoffset: dashOffset }}
            transition={prefersReducedMotion ? { duration: 0 } : { duration: 1, ease: "easeOut" }}
          />
        </svg>
        <div className="absolute flex flex-col items-center">
          <p className="text-4xl font-extrabold tracking-tight">
            <AnimatedNumber value={heatIndexValue} decimals={0} />
            <span className="text-xl">°{unit}</span>
          </p>
          <p className="text-[11px] font-medium text-muted">Heat Index</p>
        </div>
      </div>

      <div className="w-full rounded-xl border px-4 py-2.5" style={{ backgroundColor: `${color}17`, borderColor: `${color}40` }} aria-hidden="true">
        <p className={cn("text-lg font-bold tracking-tight", RISK_LEVEL_TEXT_CLASS[riskLevel])}>{RISK_LEVEL_LABEL[riskLevel]}</p>
      </div>

      <p className="text-sm leading-relaxed text-muted" aria-hidden="true">
        {message}
      </p>

      <div className="flex w-full items-center justify-between gap-1" aria-hidden="true">
        {RISK_LEVEL_ORDER.map((level) => (
          <div key={level} className="flex flex-1 flex-col items-center gap-1">
            <span
              className={cn("h-1.5 w-full rounded-full transition-opacity", level === riskLevel ? "opacity-100" : "opacity-25")}
              style={{ backgroundColor: RISK_LEVEL_COLOR[level] }}
            />
          </div>
        ))}
      </div>
    </div>
  );
}
