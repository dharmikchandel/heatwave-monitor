import { CIRCUIT_TEXT, SERVICE_INFO } from "@/lib/alertView";
import type { ServiceStatus } from "@/lib/backend";
import { cn } from "@/lib/utils";

const STATUS_STYLE: Record<ServiceStatus["status"], { label: string; dot: string; text: string }> = {
  ok: { label: "Operational", dot: "bg-emerald-500", text: "text-emerald-700 dark:text-emerald-400" },
  unavailable: { label: "Not ready", dot: "bg-amber-500", text: "text-amber-700 dark:text-amber-400" },
  down: { label: "Down", dot: "bg-red-600", text: "text-red-700 dark:text-red-400" },
};

/** `step` is the service's place in the data pipeline, or null for one outside it (accounts). */
export default function ServiceStatusCard({ service, step }: { service: ServiceStatus; step: number | null }) {
  const info = SERVICE_INFO[service.name] ?? { label: service.name, role: "" };
  const style = STATUS_STYLE[service.status];
  const checks = Object.entries(service.checks ?? {});

  return (
    <li className="glass-card flex flex-col gap-2 rounded-2xl p-4">
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2">
          {step !== null && (
            <span className="flex h-6 w-6 items-center justify-center rounded-full bg-surface text-[11px] font-bold text-muted" aria-hidden="true">
              {step}
            </span>
          )}
          <h3 className="text-sm font-bold">{info.label}</h3>
        </div>
        <span className={cn("inline-flex items-center gap-1.5 text-xs font-semibold", style.text)}>
          <span className={cn("h-2 w-2 rounded-full", style.dot)} aria-hidden="true" />
          {style.label}
        </span>
      </div>

      <p className="text-xs text-muted">{info.role}</p>

      <dl className="mt-1 flex flex-wrap gap-x-5 gap-y-1 text-[11px] text-muted">
        {service.status !== "down" && (
          <div className="flex gap-1">
            <dt className="font-semibold">Response</dt>
            <dd>{service.latencyMs < 10 ? service.latencyMs.toFixed(1) : Math.round(service.latencyMs)} ms</dd>
          </div>
        )}
        <div className="flex gap-1" title={CIRCUIT_TEXT[service.circuit]}>
          <dt className="font-semibold">Circuit</dt>
          <dd className={cn(service.circuit !== "closed" && "font-semibold text-red-700 dark:text-red-400")}>{service.circuit}</dd>
        </div>
        {checks.map(([name, value]) => (
          <div key={name} className="flex gap-1">
            <dt className="font-semibold capitalize">{name}</dt>
            <dd className={cn(value !== "ok" && "font-semibold text-amber-700 dark:text-amber-400")}>{value}</dd>
          </div>
        ))}
      </dl>

      {service.error && <p className="text-[11px] font-semibold text-red-700 dark:text-red-400">{service.error}</p>}
      {service.circuit !== "closed" && <p className="text-[11px] text-muted">{CIRCUIT_TEXT[service.circuit]}</p>}
    </li>
  );
}
