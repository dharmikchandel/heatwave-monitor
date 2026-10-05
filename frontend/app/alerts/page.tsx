"use client";

import { ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import AlertCard, { type DetailState } from "@/components/AlertCard";
import BackendNotice from "@/components/BackendNotice";
import { type AlertRecord, type AlertStatus, fetchAlertDetail, fetchAlerts } from "@/lib/backend";
import { usePolling, useNow } from "@/lib/usePolling";
import { cn, FOCUS_RING, formatRelativeTime } from "@/lib/utils";

type Filter = AlertStatus | "all";

const FILTERS: { value: Filter; label: string }[] = [
  { value: "open", label: "Open" },
  { value: "resolved", label: "Resolved" },
  { value: "all", label: "All" },
];

const EMPTY_TEXT: Record<Filter, { title: string; body: string }> = {
  open: {
    title: "No open alerts",
    body: "Alerts open when a Danger-level heatwave is under way or forecast (apparent temperature of 41 °C or more on two days in a row). None of the watched cities is in that situation right now.",
  },
  resolved: { title: "No resolved alerts yet", body: "Alerts that have run their course will be listed here, with their full timeline." },
  all: { title: "No alerts yet", body: "Nothing has triggered an alert. They open when a Danger-level heatwave is under way or forecast." },
};

export default function AlertsPage() {
  const [filter, setFilter] = useState<Filter>("open");

  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Heat Alerts</h1>
        <p className="mt-1 text-sm text-muted">
          Every watched city is assessed continuously. An alert opens when a Danger-level heatwave is under way or forecast, is updated as it
          escalates, and resolves once the danger has passed. Each shows the reasoning and who was notified.
        </p>
      </div>

      <div role="group" aria-label="Filter alerts" className="flex w-fit gap-1 rounded-full border border-surface-border bg-surface/60 p-1">
        {FILTERS.map((f) => (
          <button
            key={f.value}
            type="button"
            aria-pressed={filter === f.value}
            onClick={() => setFilter(f.value)}
            className={cn(
              "rounded-full px-3.5 py-1.5 text-xs font-semibold transition",
              filter === f.value ? "bg-orange-600 text-white shadow-sm" : "text-muted hover:text-foreground",
              FOCUS_RING,
            )}
          >
            {f.label}
          </button>
        ))}
      </div>

      {/* Remounting on a filter change restarts the polling cleanly. */}
      <AlertsFeed key={filter} filter={filter} />
    </>
  );
}

function AlertsFeed({ filter }: { filter: Filter }) {
  const load = useCallback((signal: AbortSignal) => fetchAlerts(filter, { signal }), [filter]);
  const { data, failure, isLoading, updatedAt, refresh } = usePolling(load, 15_000);
  const now = useNow();

  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [details, setDetails] = useState<Record<number, DetailState>>({});
  const expandedRef = useRef(expanded);
  useEffect(() => {
    expandedRef.current = expanded;
  }, [expanded]);

  const loadDetail = useCallback((id: number) => {
    fetchAlertDetail(id)
      .then((r) => setDetails((d) => ({ ...d, [id]: r.ok ? { status: "ready", data: r.data } : { status: "error" } })))
      .catch(() => setDetails((d) => ({ ...d, [id]: { status: "error" } })));
  }, []);

  // Keep any open details as fresh as the list.
  useEffect(() => {
    expandedRef.current.forEach((id) => loadDetail(id));
  }, [updatedAt, loadDetail]);

  function toggle(id: number) {
    const next = new Set(expanded);
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
      if (!details[id]) {
        setDetails((d) => ({ ...d, [id]: { status: "loading" } }));
        loadDetail(id);
      }
    }
    setExpanded(next);
  }

  if (!data && isLoading) return <FeedSkeleton />;
  if (!data) return <BackendNotice what="Alerts" reason={failure ?? "unavailable"} onRetry={refresh} />;

  // Open alerts first, then the most recently opened.
  const alerts: AlertRecord[] = [...data.alerts].sort((a, b) => (a.status === b.status ? 0 : a.status === "open" ? -1 : 1));

  return (
    <>
      {failure && (
        <p role="status" className="rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-foreground/90">
          The backend is not answering right now; showing the last alerts it sent{updatedAt && now ? ` (${formatRelativeTime(updatedAt, now)})` : ""}.
        </p>
      )}

      {alerts.length === 0 ? (
        <div role="status" className="glass-card flex flex-col items-center gap-3 rounded-2xl p-8 text-center">
          <span className="flex h-11 w-11 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-700 dark:text-emerald-400">
            <ShieldCheck className="h-5 w-5" aria-hidden="true" />
          </span>
          <h2 className="text-base font-bold">{EMPTY_TEXT[filter].title}</h2>
          <p className="max-w-lg text-sm text-muted">{EMPTY_TEXT[filter].body}</p>
        </div>
      ) : (
        <ul className="flex flex-col gap-4" aria-label="Alerts">
          {alerts.map((alert) => (
            <li key={alert.id}>
              <AlertCard
                alert={alert}
                now={now}
                expanded={expanded.has(alert.id)}
                onToggle={() => toggle(alert.id)}
                detail={details[alert.id] ?? { status: "idle" }}
              />
            </li>
          ))}
        </ul>
      )}

      <p className="text-center text-[11px] text-muted">
        {updatedAt && now ? `Updated ${formatRelativeTime(updatedAt, now)} · ` : ""}refreshes every 15 seconds
      </p>
    </>
  );
}

function FeedSkeleton() {
  return (
    <div className="flex flex-col gap-4" aria-hidden="true">
      {[0, 1].map((i) => (
        <div key={i} className="glass-card shimmer h-44 rounded-2xl" />
      ))}
    </div>
  );
}
