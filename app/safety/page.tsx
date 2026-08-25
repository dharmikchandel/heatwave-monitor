"use client";

import { motion } from "framer-motion";
import { Phone, ShieldAlert } from "lucide-react";
import { generateAdvisories, HEAT_RISK_THRESHOLDS_C, RISK_LEVEL_LABEL, RISK_LEVEL_ORDER } from "@/lib/heatwaveEngine";
import { useClimate } from "@/lib/ClimateContext";
import { cn, RISK_LEVEL_BG_CLASS, RISK_LEVEL_COLOR, RISK_LEVEL_TEXT_CLASS } from "@/lib/utils";

const THRESHOLD_DESCRIPTIONS: Record<(typeof RISK_LEVEL_ORDER)[number], string> = {
  normal: "Apparent temperature below the caution threshold. Standard precautions apply.",
  caution: `Apparent temperature ≥ ${HEAT_RISK_THRESHOLDS_C.caution}°C. Fatigue is possible with prolonged exposure or activity.`,
  "extreme-caution": `Apparent temperature ≥ ${HEAT_RISK_THRESHOLDS_C.extremeCaution}°C. Heat cramps and heat exhaustion become possible.`,
  danger: `Apparent temperature ≥ ${HEAT_RISK_THRESHOLDS_C.danger}°C for 2+ consecutive days. Heat exhaustion is likely, heat stroke possible.`,
  "extreme-danger": `Apparent temperature ≥ ${HEAT_RISK_THRESHOLDS_C.extremeDanger}°C. Heat stroke is highly likely with continued exposure.`,
};

const ILLNESS_STAGES = [
  {
    name: "Heat Cramps",
    symptoms: "Painful muscle spasms, usually in the legs or abdomen, during or after heavy exertion or sweating.",
    response: "Stop activity, move to a cool place, hydrate with water or an electrolyte drink, gently stretch the affected muscle.",
  },
  {
    name: "Heat Exhaustion",
    symptoms: "Heavy sweating, cold/pale/clammy skin, fast weak pulse, nausea, headache, dizziness, or fainting.",
    response: "Move to a cool place, loosen clothing, apply cool wet cloths, sip water. Seek medical care if symptoms worsen or last over an hour.",
  },
  {
    name: "Heat Stroke",
    symptoms: "Body temperature above 40°C, hot/red/dry or damp skin, fast strong pulse, confusion, slurred speech, or loss of consciousness.",
    response: "This is a medical emergency. Call emergency services immediately. Move to a cool place and lower body temperature with cool cloths or a bath while waiting for help — do not give fluids to an unconscious person.",
  },
];

export default function SafetyPage() {
  const { location, assessment } = useClimate();

  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Heat Safety Guide</h1>
        <p className="mt-1 text-sm text-muted">
          A complete reference for recognizing and responding to every heat risk tier, plus general heat illness first aid — independent of
          where you are or what the forecast says.
        </p>
      </div>

      {assessment && (
        <div
          className={cn("flex items-center gap-3 rounded-2xl border p-4", RISK_LEVEL_BG_CLASS[assessment.riskLevel])}
        >
          <ShieldAlert className="h-5 w-5 shrink-0" aria-hidden="true" />
          <p className="text-sm">
            Right now in <span className="font-semibold">{location?.name ?? "your area"}</span>, the current heat risk is{" "}
            <span className="font-bold">{RISK_LEVEL_LABEL[assessment.riskLevel]}</span>. See the matching tier below for specific
            guidance.
          </p>
        </div>
      )}

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">Risk Tiers &amp; Advisories</h2>
        <div className="flex flex-col gap-4">
          {RISK_LEVEL_ORDER.map((level, i) => (
            <motion.div
              key={level}
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.3, delay: i * 0.05 }}
              className="rounded-xl border border-surface-border p-4"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span
                  className="h-2.5 w-2.5 rounded-full"
                  style={{ backgroundColor: RISK_LEVEL_COLOR[level] }}
                  aria-hidden="true"
                />
                <h3 className={cn("text-sm font-bold", RISK_LEVEL_TEXT_CLASS[level])}>{RISK_LEVEL_LABEL[level]}</h3>
                {assessment?.riskLevel === level && (
                  <span className="rounded-full bg-orange-500/15 px-2 py-0.5 text-[10px] font-semibold text-orange-700 dark:text-orange-400">
                    Current
                  </span>
                )}
              </div>
              <p className="mt-1.5 text-xs text-muted">{THRESHOLD_DESCRIPTIONS[level]}</p>
              <ul className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
                {generateAdvisories(level).map((tip) => (
                  <li key={tip.title} className="rounded-lg bg-surface/50 p-2.5">
                    <p className="text-xs font-semibold">{tip.title}</p>
                    <p className="mt-0.5 text-[11px] leading-relaxed text-muted">{tip.description}</p>
                  </li>
                ))}
              </ul>
            </motion.div>
          ))}
        </div>
      </div>

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">Recognizing Heat Illness</h2>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          {ILLNESS_STAGES.map((stage) => (
            <div key={stage.name} className="rounded-xl border border-surface-border p-4">
              <h3 className="text-sm font-bold">{stage.name}</h3>
              <p className="mt-2 text-xs font-semibold text-muted">Symptoms</p>
              <p className="text-xs leading-relaxed text-muted">{stage.symptoms}</p>
              <p className="mt-2 text-xs font-semibold text-muted">Response</p>
              <p className="text-xs leading-relaxed text-muted">{stage.response}</p>
            </div>
          ))}
        </div>
      </div>

      <div className="flex items-center gap-3 rounded-2xl border border-red-700/40 bg-red-800/10 p-4">
        <Phone className="h-5 w-5 shrink-0 text-red-700 dark:text-red-400" aria-hidden="true" />
        <p className="text-sm text-foreground/90">
          <span className="font-bold text-red-700 dark:text-red-400">Suspected heat stroke is a medical emergency.</span> Call your local
          emergency number immediately — this guide is informational and does not replace professional medical care.
        </p>
      </div>
    </>
  );
}
