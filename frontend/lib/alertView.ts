// Presentation logic for the alerts and status pages, kept free of React so it is easy to test.

import type { AlertNotification, AlertRecord, AlertTimelineEntry, ServiceStatus, SystemStatus } from "./backend";
import { RISK_LEVEL_LABEL } from "./heatwaveEngine";
import type { HeatRiskLevel } from "./types";

/** The tier an alert is shown in: where it is now while open, how bad it got once resolved. */
export function accentLevel(alert: Pick<AlertRecord, "status" | "currentLevel" | "peakLevel">): HeatRiskLevel {
  return alert.status === "open" ? alert.currentLevel : alert.peakLevel;
}

/** One line for an alert's timeline, e.g. "Escalated to Extreme Danger". */
export function timelineLabel(entry: Pick<AlertTimelineEntry, "kind" | "level">): string {
  const level = RISK_LEVEL_LABEL[entry.level];
  switch (entry.kind) {
    case "opened":
      return `Opened at ${level}`;
    case "escalated":
      return `Escalated to ${level}`;
    case "deescalated":
      return `Eased to ${level}`;
    case "resolved":
      return "Resolved: the danger has passed";
    case "reopened":
      return `Reopened at ${level}`;
  }
}

export interface NotificationSummary {
  total: number;
  sent: number;
  pending: number;
  failed: number;
  cancelled: number;
  /** "3 notifications sent", "2 sent, 1 failed", or "No subscribers were notified". */
  text: string;
}

export function summarizeNotifications(notifications: Pick<AlertNotification, "status">[]): NotificationSummary {
  const count = (s: AlertNotification["status"]) => notifications.filter((n) => n.status === s).length;
  const sent = count("sent");
  const pending = count("pending");
  const failed = count("failed");
  const cancelled = count("cancelled");
  const total = notifications.length;

  let text: string;
  if (total === 0) {
    text = "No subscribers were notified";
  } else if (sent === total) {
    text = `${total} notification${total === 1 ? "" : "s"} sent`;
  } else {
    text = [
      sent ? `${sent} sent` : "",
      pending ? `${pending} waiting to be delivered` : "",
      failed ? `${failed} failed` : "",
      cancelled ? `${cancelled} cancelled` : "",
    ]
      .filter(Boolean)
      .join(", ");
  }
  return { total, sent, pending, failed, cancelled, text };
}

export const SERVICE_INFO: Record<ServiceStatus["name"], { label: string; role: string }> = {
  weather: { label: "Weather data", role: "Fetches live conditions and forecasts for every watched city." },
  processing: { label: "Data processing", role: "Repairs gaps, computes heat index and daily metrics." },
  prediction: { label: "Prediction", role: "Estimates the chance of a heatwave warning for each day." },
  risk: { label: "Risk assessment", role: "Turns probabilities and temperatures into risk levels, with reasons." },
  alert: { label: "Alerts & notifications", role: "Opens, escalates and resolves alerts, and notifies subscribers." },
};

export const CIRCUIT_TEXT: Record<ServiceStatus["circuit"], string> = {
  closed: "Circuit closed: requests flow normally.",
  "half-open": "Circuit half-open: testing whether the service has recovered.",
  open: "Circuit open: the gateway is refusing requests to this service so they fail fast.",
};

export type Tone = "ok" | "warn" | "bad";

/** The headline for the status page. */
export function overallMessage(status: Pick<SystemStatus, "status" | "services">): { title: string; detail: string; tone: Tone } {
  const unhealthy = status.services.filter((s) => !s.ready).map((s) => SERVICE_INFO[s.name]?.label ?? s.name);
  switch (status.status) {
    case "ok":
      return { title: "All systems operational", detail: "Every service is ready and the pipeline is flowing.", tone: "ok" };
    case "degraded":
      return {
        title: "Partially degraded",
        detail: `Not ready: ${unhealthy.join(", ")}. The rest keeps working, and the dashboard shows what is still available.`,
        tone: "warn",
      };
    default:
      return { title: "Backend unavailable", detail: "No backend service is reachable. The dashboard runs in local mode.", tone: "bad" };
  }
}
