import { CloudOff, Laptop, RefreshCw } from "lucide-react";
import type { BackendFailure } from "@/lib/backend";
import { cn, FOCUS_RING } from "@/lib/utils";

interface BackendNoticeProps {
  /** What the page needs the backend for, e.g. "Alerts". */
  what: string;
  reason: BackendFailure;
  onRetry?: () => void;
}

/** Shown instead of a page's content when the backend cannot supply it. */
export default function BackendNotice({ what, reason, onRetry }: BackendNoticeProps) {
  const notConnected = reason === "disabled";
  const Icon = notConnected ? Laptop : CloudOff;
  const message = notConnected
    ? `${what} come from the Heatwave Monitor backend, which is not connected to this deployment. The dashboard still works: it fetches weather directly and computes risk in your browser.`
    : reason === "unauthorized"
      ? `Sign in to see ${what.toLowerCase()}.`
      : reason === "rate_limited"
      ? `The backend is busy right now. ${what} will load again in a moment.`
      : reason === "timeout"
        ? `The backend is slow to answer, so ${what.toLowerCase()} could not be loaded yet.`
        : reason === "not_ready"
          ? `The backend is still preparing this data, so ${what.toLowerCase()} are not available yet.`
        : reason === "bad_response"
          ? `The backend sent an answer this page could not understand, so ${what.toLowerCase()} are unavailable.`
          : `The backend is unreachable, so ${what.toLowerCase()} are unavailable. The dashboard falls back to local mode.`;

  return (
    <div role="status" className="glass-card flex flex-col items-center gap-3 rounded-2xl p-8 text-center">
      <span className="flex h-11 w-11 items-center justify-center rounded-full bg-surface text-muted">
        <Icon className="h-5 w-5" aria-hidden="true" />
      </span>
      <h2 className="text-base font-bold">{notConnected ? "Backend not connected" : "Backend unavailable"}</h2>
      <p className="max-w-md text-sm text-muted">{message}</p>
      {onRetry && !notConnected && (
        <button
          type="button"
          onClick={onRetry}
          className={cn("flex items-center gap-1.5 rounded-full bg-orange-600 px-3.5 py-1.5 text-xs font-semibold text-white transition hover:bg-orange-500", FOCUS_RING)}
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" /> Try again
        </button>
      )}
    </div>
  );
}
