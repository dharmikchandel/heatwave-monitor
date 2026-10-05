"use client";

import { Activity, AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import BackendNotice from "@/components/BackendNotice";
import ModelCard from "@/components/ModelCard";
import ServiceStatusCard from "@/components/ServiceStatusCard";
import { overallMessage, type Tone } from "@/lib/alertView";
import { fetchModelInfo, fetchSystemStatus, type SystemStatus } from "@/lib/backend";
import { usePolling, useNow } from "@/lib/usePolling";
import { cn, formatRelativeTime } from "@/lib/utils";

const TONE_STYLE: Record<Tone, { box: string; icon: typeof Activity; iconClass: string }> = {
  ok: { box: "border-emerald-500/30 bg-emerald-500/10", icon: CheckCircle2, iconClass: "text-emerald-600 dark:text-emerald-400" },
  warn: { box: "border-amber-500/30 bg-amber-500/10", icon: AlertTriangle, iconClass: "text-amber-600 dark:text-amber-400" },
  bad: { box: "border-red-600/30 bg-red-600/10", icon: XCircle, iconClass: "text-red-600 dark:text-red-400" },
};

// Stable functions (not recreated each render), so polling is not restarted by a re-render.
const loadStatus = (signal: AbortSignal) => fetchSystemStatus({ signal });
const loadModel = (signal: AbortSignal) => fetchModelInfo({ signal });

export default function StatusPage() {
  const status = usePolling(loadStatus, 10_000);
  const model = usePolling(loadModel, 60_000);
  const now = useNow(5_000);

  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">System Status</h1>
        <p className="mt-1 text-sm text-muted">
          Each reading flows through five services in turn: weather data, processing, prediction, risk assessment and alerts. Here is how each is
          doing, as seen by the gateway that fronts them.
        </p>
      </div>

      {!status.data && status.isLoading && <div className="glass-card shimmer h-28 rounded-2xl" aria-hidden="true" />}
      {!status.data && !status.isLoading && <BackendNotice what="Service health details" reason={status.failure ?? "unavailable"} onRetry={status.refresh} />}

      {status.data && (
        <>
          <Overall data={status.data} stale={status.failure !== null} />

          <ol className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" aria-label="Services in pipeline order">
            {status.data.services.map((service, i) => (
              <ServiceStatusCard key={service.name} service={service} step={i + 1} />
            ))}
          </ol>

          {model.data && <ModelCard model={model.data} />}

          <p className="text-center text-[11px] text-muted">
            {status.updatedAt && now ? `Checked ${formatRelativeTime(status.updatedAt, now)} · ` : ""}refreshes every 10 seconds
          </p>
        </>
      )}
    </>
  );
}

function Overall({ data, stale }: { data: SystemStatus; stale: boolean }) {
  const { title, detail, tone } = overallMessage(data);
  const style = TONE_STYLE[tone];
  const Icon = style.icon;
  return (
    <div role="status" aria-live="polite" className={cn("flex items-start gap-3 rounded-2xl border p-4", style.box)}>
      <Icon className={cn("mt-0.5 h-5 w-5 shrink-0", style.iconClass)} aria-hidden="true" />
      <div>
        <p className="text-sm font-bold">{title}</p>
        <p className="text-sm text-foreground/90">{detail}</p>
        {stale && <p className="mt-1 text-xs text-muted">The gateway is not answering right now; this is the last status it reported.</p>}
      </div>
    </div>
  );
}
