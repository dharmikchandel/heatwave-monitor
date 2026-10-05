"use client";

import { BellRing, CheckCheck } from "lucide-react";
import Link from "next/link";
import { useCallback } from "react";
import BackendNotice from "@/components/BackendNotice";
import { asRead, fetchInbox, markAllRead, markRead, type InboxItem } from "@/lib/auth";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import { useInboxChanged } from "@/lib/useInboxChanged";
import { useNow, usePolling } from "@/lib/usePolling";
import { cn, FOCUS_RING, formatRelativeTime, RISK_LEVEL_COLOR } from "@/lib/utils";

const KIND_TEXT: Record<InboxItem["kind"], string> = {
  opened: "Heat alert opened",
  escalated: "Heat alert escalated",
  resolved: "All clear",
};

export default function InboxView() {
  const load = useCallback((signal: AbortSignal) => fetchInbox(50, { signal }).then(asRead), []);
  const { data, failure, isLoading, refresh } = usePolling(load, 30_000);
  const now = useNow();
  useInboxChanged(refresh); // marking something read here (or anywhere) shows at once, not at the next poll

  if (!data && isLoading) return <div className="glass-card shimmer h-40 rounded-2xl" aria-hidden="true" />;
  if (!data) return <BackendNotice what="Your inbox" reason={failure ?? "unavailable"} onRetry={refresh} />;

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Inbox</h1>
          <p className="mt-1 text-sm text-muted">
            {data.unread > 0 ? `${data.unread} unread` : "Nothing unread"}. Alerts for the cities you saved arrive here.
          </p>
        </div>
        {data.unread > 0 && (
          <button
            type="button"
            onClick={() => void markAllRead()}
            className={cn("flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}
          >
            <CheckCheck className="h-3.5 w-3.5" aria-hidden="true" /> Mark all read
          </button>
        )}
      </div>

      {data.notifications.length === 0 ? (
        <div role="status" className="glass-card flex flex-col items-center gap-3 rounded-2xl p-8 text-center">
          <span className="flex h-11 w-11 items-center justify-center rounded-full bg-surface text-muted">
            <BellRing className="h-5 w-5" aria-hidden="true" />
          </span>
          <h2 className="text-base font-bold">No notifications yet</h2>
          <p className="max-w-md text-sm text-muted">
            On the dashboard, pick a city and press <strong>Alerts off</strong> to turn alerts on. You will be told here when a heatwave is under way
            or forecast there, and again when it has passed.
          </p>
        </div>
      ) : (
        <ul className="flex flex-col gap-3" aria-label="Notifications">
          {data.notifications.map((n) => (
            <li key={n.id}>
              <InboxCard item={n} now={now} />
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

export function InboxCard({ item, now }: { item: InboxItem; now: Date | null }) {
  const unread = !item.readAt;
  const color = item.kind === "resolved" ? "#10b981" : RISK_LEVEL_COLOR[item.level];
  return (
    <article
      aria-label={`${KIND_TEXT[item.kind]}: ${item.locationName}${unread ? " (unread)" : ""}`}
      className={cn("glass-card flex flex-col gap-2 rounded-2xl border-l-4 p-4", unread ? "" : "opacity-80")}
      style={{ borderLeftColor: color }}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex items-center gap-2">
          {unread && <span className="h-2 w-2 rounded-full bg-orange-500" aria-hidden="true" />}
          <h3 className="text-sm font-bold">{item.headline}</h3>
        </div>
        <span className="text-[11px] text-muted">{now ? formatRelativeTime(new Date(item.createdAt), now) : ""}</span>
      </div>
      <p className="text-sm text-foreground/90">{item.summary}</p>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="font-semibold" style={{ color }}>
          {KIND_TEXT[item.kind]} · {RISK_LEVEL_LABEL[item.level]}
        </span>
        <span className="flex items-center gap-3">
          <Link href="/alerts" className={cn("rounded font-semibold text-orange-600 hover:underline dark:text-orange-400", FOCUS_RING)}>
            View alerts
          </Link>
          {unread && (
            <button type="button" onClick={() => void markRead(item.id)} className={cn("rounded font-semibold hover:underline", FOCUS_RING)}>
              Mark read
            </button>
          )}
        </span>
      </div>
    </article>
  );
}
