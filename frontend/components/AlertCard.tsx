import { CheckCircle2, ChevronDown, Flame } from "lucide-react";
import type { AlertDetail, AlertRecord } from "@/lib/backend";
import { accentLevel, summarizeNotifications, timelineLabel } from "@/lib/alertView";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import { cn, FOCUS_RING, formatDateLong, formatRelativeTime, RISK_LEVEL_BG_CLASS, RISK_LEVEL_COLOR } from "@/lib/utils";

export type DetailState = { status: "idle" } | { status: "loading" } | { status: "error" } | { status: "ready"; data: AlertDetail };

interface AlertCardProps {
  alert: AlertRecord;
  /** For relative times; null before the page has mounted. */
  now: Date | null;
  expanded: boolean;
  onToggle: () => void;
  detail: DetailState;
}

function ago(iso: string, now: Date | null): string {
  return now ? formatRelativeTime(new Date(iso), now) : "";
}

export default function AlertCard({ alert, now, expanded, onToggle, detail }: AlertCardProps) {
  const level = accentLevel(alert);
  const color = RISK_LEVEL_COLOR[level];
  const isOpen = alert.status === "open";
  const titleId = `alert-${alert.id}-title`;
  const panelId = `alert-${alert.id}-details`;
  const { peak, warningDays, rationale } = alert.details;
  const peakPercent = Math.round(peak.probability * 100);

  return (
    <article aria-labelledby={titleId} className="glass-card overflow-hidden rounded-2xl border-l-4" style={{ borderLeftColor: color }}>
      <div className="flex flex-col gap-3 p-4 sm:p-5">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h3 id={titleId} className="text-base font-bold tracking-tight">
              {alert.headline}
            </h3>
            <p className="mt-0.5 text-xs text-muted">
              {isOpen ? `Opened ${ago(alert.openedAt, now)}` : `Resolved ${alert.resolvedAt ? ago(alert.resolvedAt, now) : ""}`}
              {isOpen && alert.updatedAt !== alert.openedAt ? ` · updated ${ago(alert.updatedAt, now)}` : ""}
              {alert.reopenCount > 0 ? ` · reopened ${alert.reopenCount}×` : ""}
              {alert.acknowledgedAt ? " · acknowledged" : ""}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <span
              className={cn(
                "inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] font-bold",
                isOpen ? "border-red-600/30 bg-red-600/10 text-red-700 dark:text-red-400" : "border-surface-border bg-surface/60 text-muted",
              )}
            >
              {isOpen ? <Flame className="h-3 w-3" aria-hidden="true" /> : <CheckCircle2 className="h-3 w-3" aria-hidden="true" />}
              {isOpen ? "Open" : "Resolved"}
            </span>
            <span className={cn("rounded-full border px-2.5 py-1 text-[11px] font-bold", RISK_LEVEL_BG_CLASS[level])}>
              {isOpen ? RISK_LEVEL_LABEL[level] : `Peaked at ${RISK_LEVEL_LABEL[level]}`}
            </span>
          </div>
        </div>

        {rationale.length > 0 && (
          <ul className="flex flex-col gap-1 text-sm leading-relaxed text-foreground/90">
            {rationale.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}

        <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted">
          {warningDays.length > 0 && (
            <div className="flex gap-1.5">
              <dt className="font-semibold">Warning days</dt>
              <dd>{warningDays.map((d) => formatDateLong(d)).join(", ")}</dd>
            </div>
          )}
          <div className="flex gap-1.5">
            <dt className="font-semibold">Peak chance</dt>
            <dd>
              {peakPercent}% on {formatDateLong(peak.date)}
            </dd>
          </div>
          <div className="flex gap-1.5">
            <dt className="font-semibold">Estimated by</dt>
            <dd>{alert.details.method === "model" ? `trained model ${alert.details.modelVersion}` : "rule-based estimate"}</dd>
          </div>
        </dl>

        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-controls={panelId}
          className={cn("flex w-fit items-center gap-1 rounded-full text-xs font-semibold text-muted transition hover:text-foreground", FOCUS_RING)}
        >
          <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", expanded && "rotate-180")} aria-hidden="true" />
          Timeline &amp; notifications
        </button>
      </div>

      {expanded && (
        <div id={panelId} className="border-t border-surface-border/60 bg-surface/30 p-4 sm:p-5">
          {detail.status === "loading" || detail.status === "idle" ? (
            <p className="text-xs text-muted">Loading details…</p>
          ) : detail.status === "error" ? (
            <p role="alert" className="text-xs text-red-700 dark:text-red-400">
              Could not load the details of this alert.
            </p>
          ) : (
            <div className="grid gap-5 sm:grid-cols-2">
              <section aria-label="Timeline">
                <h4 className="mb-2 text-[11px] font-bold uppercase tracking-wide text-muted">Timeline</h4>
                <ol className="flex flex-col gap-2">
                  {detail.data.history.map((entry, i) => (
                    <li key={i} className="flex gap-2 text-xs">
                      <span className="mt-1 h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: RISK_LEVEL_COLOR[entry.level] }} aria-hidden="true" />
                      <div>
                        <p className="font-semibold">{timelineLabel(entry)}</p>
                        <p className="text-muted" title={new Date(entry.at).toLocaleString()}>
                          {ago(entry.at, now)}
                          {entry.note ? ` · ${entry.note}` : ""}
                        </p>
                      </div>
                    </li>
                  ))}
                </ol>
              </section>

              <section aria-label="Notifications">
                <h4 className="mb-2 text-[11px] font-bold uppercase tracking-wide text-muted">Notifications</h4>
                <p className="mb-2 text-xs font-semibold">{summarizeNotifications(detail.data.notifications).text}</p>
                <ul className="flex flex-col gap-1 text-xs text-muted">
                  {detail.data.notifications.map((n) => (
                    <li key={n.id}>
                      <span className="font-semibold capitalize text-foreground/80">{n.kind}</span> via {n.channel} ·{" "}
                      <span className={cn(n.status === "failed" && "font-semibold text-red-700 dark:text-red-400")}>{n.status}</span>
                      {n.attempts > 1 ? ` after ${n.attempts} attempts` : ""}
                    </li>
                  ))}
                </ul>
              </section>
            </div>
          )}
        </div>
      )}
    </article>
  );
}
