"use client";

import { motion } from "framer-motion";
import { HeartPulse } from "lucide-react";
import { generateAdvisories, RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import type { HeatRiskLevel } from "@/lib/types";
import { RISK_LEVEL_COLOR } from "@/lib/utils";

interface SafetyAdvisoryProps {
  riskLevel: HeatRiskLevel;
}

export default function SafetyAdvisory({ riskLevel }: SafetyAdvisoryProps) {
  const advisories = generateAdvisories(riskLevel);
  const color = RISK_LEVEL_COLOR[riskLevel];

  return (
    <div className="glass-card rounded-2xl p-4 sm:p-6">
      <div className="mb-4 flex items-center gap-2">
        <HeartPulse className="h-4 w-4" style={{ color }} aria-hidden="true" />
        <h2 className="text-sm font-bold uppercase tracking-wide text-muted">
          Safety Advisory — {RISK_LEVEL_LABEL[riskLevel]}
        </h2>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {advisories.map((tip, i) => (
          <motion.div
            key={tip.title}
            initial={{ opacity: 0, x: -8 }}
            animate={{ opacity: 1, x: 0 }}
            transition={{ duration: 0.3, delay: i * 0.05 }}
            className="flex gap-3 rounded-xl border border-surface-border bg-surface/40 p-3"
          >
            <span className="mt-0.5 h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: color }} aria-hidden="true" />
            <div>
              <p className="text-sm font-semibold">{tip.title}</p>
              <p className="mt-0.5 text-xs leading-relaxed text-muted">{tip.description}</p>
            </div>
          </motion.div>
        ))}
      </div>
    </div>
  );
}
