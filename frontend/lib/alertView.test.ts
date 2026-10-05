import { describe, expect, test } from "bun:test";
import { accentLevel, CIRCUIT_TEXT, overallMessage, SERVICE_INFO, summarizeNotifications, timelineLabel } from "./alertView";
import type { ServiceStatus } from "./backend";

describe("accentLevel", () => {
  test("an open alert shows where it is now; a resolved one shows how bad it got", () => {
    expect(accentLevel({ status: "open", currentLevel: "danger", peakLevel: "extreme-danger" })).toBe("danger");
    expect(accentLevel({ status: "resolved", currentLevel: "normal", peakLevel: "extreme-danger" })).toBe("extreme-danger");
  });
});

describe("timelineLabel", () => {
  test("reads naturally for every kind of step", () => {
    expect(timelineLabel({ kind: "opened", level: "danger" })).toBe("Opened at Danger");
    expect(timelineLabel({ kind: "escalated", level: "extreme-danger" })).toBe("Escalated to Extreme Danger");
    expect(timelineLabel({ kind: "deescalated", level: "caution" })).toBe("Eased to Caution");
    expect(timelineLabel({ kind: "resolved", level: "normal" })).toBe("Resolved: the danger has passed");
    expect(timelineLabel({ kind: "reopened", level: "danger" })).toBe("Reopened at Danger");
  });
});

describe("summarizeNotifications", () => {
  const n = (...statuses: string[]) => statuses.map((status) => ({ status: status as "sent" }));

  test("nobody subscribed", () => {
    expect(summarizeNotifications([]).text).toBe("No subscribers were notified");
  });

  test("everything delivered, singular and plural", () => {
    expect(summarizeNotifications(n("sent")).text).toBe("1 notification sent");
    expect(summarizeNotifications(n("sent", "sent", "sent")).text).toBe("3 notifications sent");
  });

  test("problems are spelled out, never hidden behind the successes", () => {
    const s = summarizeNotifications(n("sent", "sent", "failed", "pending", "cancelled"));
    expect(s).toMatchObject({ total: 5, sent: 2, failed: 1, pending: 1, cancelled: 1 });
    expect(s.text).toBe("2 sent, 1 waiting to be delivered, 1 failed, 1 cancelled");
    expect(summarizeNotifications(n("failed")).text).toBe("1 failed");
  });
});

describe("overallMessage", () => {
  const svc = (name: ServiceStatus["name"], ready: boolean) => ({ name, ready }) as Pick<ServiceStatus, "name" | "ready"> as ServiceStatus;

  test("healthy", () => {
    expect(overallMessage({ status: "ok", services: [] })).toMatchObject({ title: "All systems operational", tone: "ok" });
  });

  test("degraded names the services that are not ready, in plain words", () => {
    const m = overallMessage({ status: "degraded", services: [svc("weather", true), svc("risk", false), svc("alert", false)] });
    expect(m.tone).toBe("warn");
    expect(m.detail).toContain("Risk assessment, Alerts & notifications");
    expect(m.detail).not.toContain("Weather data");
  });

  test("down says the dashboard keeps working locally", () => {
    const m = overallMessage({ status: "down", services: [svc("weather", false)] });
    expect(m).toMatchObject({ title: "Backend unavailable", tone: "bad" });
    expect(m.detail).toContain("local mode");
  });
});

test("every service the gateway reports has a name and role, and every circuit state an explanation", () => {
  for (const name of ["weather", "processing", "prediction", "risk", "alert"] as const) {
    expect(SERVICE_INFO[name].label.length).toBeGreaterThan(3);
    expect(SERVICE_INFO[name].role.length).toBeGreaterThan(10);
  }
  for (const state of ["closed", "open", "half-open"] as const) expect(CIRCUIT_TEXT[state].length).toBeGreaterThan(10);
});
