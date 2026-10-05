"use client";

import { Laptop, Server } from "lucide-react";
import { describeFallback } from "@/lib/backend";
import { useClimate } from "@/lib/ClimateContext";
import { cn } from "@/lib/utils";

/**
 * Says where the numbers on screen came from: the backend (cleaned data, trained model,
 * risk reasoning, alerts) or this browser (Open-Meteo, computed locally). An unexpected
 * fallback is amber, so a degraded system is noticed rather than silently accepted.
 */
export default function SourceBadge({ className }: { className?: string }) {
  const { dataSource, insight, fallbackReason } = useClimate();
  if (!dataSource) return null;

  const isBackend = dataSource === "backend";
  const unexpected = !isBackend && fallbackReason !== null && fallbackReason !== "disabled" && fallbackReason !== "unauthorized";

  let label: string;
  let detail: string;
  if (isBackend) {
    const how = insight?.method === "model" ? "trained model" : insight?.method === "rules" ? "rule-based" : "weather only";
    label = insight?.degraded ? "Backend · degraded" : `Backend · ${how}`;
    detail = [
      "Cleaned weather, heatwave probabilities, risk reasoning and alerts come from the Heatwave Monitor backend.",
      insight?.modelVersion ? `Model ${insight.modelVersion}.` : "",
      insight?.dataQuality !== null && insight?.dataQuality !== undefined ? `Data quality ${Math.round(insight.dataQuality * 100)}%.` : "",
      insight?.degraded ? "A backend service is currently failing, so part of the picture is missing." : "",
    ]
      .filter(Boolean)
      .join(" ");
  } else {
    label = "Local mode";
    detail = describeFallback(fallbackReason);
  }

  const Icon = isBackend ? Server : Laptop;
  return (
    <span
      role="status"
      title={detail}
      aria-label={`Data source: ${label}. ${detail}`}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-2.5 py-1 text-[11px] font-semibold",
        className,
      )}
    >
      <span
        aria-hidden="true"
        className={cn("h-1.5 w-1.5 rounded-full", isBackend && !insight?.degraded ? "bg-emerald-500" : unexpected || insight?.degraded ? "bg-amber-500" : "bg-muted")}
      />
      <Icon className="h-3 w-3" aria-hidden="true" />
      {label}
    </span>
  );
}
