import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import sample from "../../testdata/climate.sample.json";
import type { AlertDetail, AlertRecord, ModelInfo, ServiceStatus } from "@/lib/backend";
import AlertCard from "./AlertCard";
import BackendNotice from "./BackendNotice";
import ModelCard from "./ModelCard";
import ServiceStatusCard from "./ServiceStatusCard";

// The open alert is the one the Go backend produced for the fixture's building heatwave.
const open = (sample.alerts as unknown as AlertRecord[])[0];
const now = new Date("2026-05-01T09:30:00Z");

const resolved: AlertRecord = {
  ...open,
  id: 4,
  status: "resolved",
  currentLevel: "normal",
  resolvedAt: "2026-05-01T09:20:00Z",
  reopenCount: 2,
  acknowledgedAt: "2026-05-01T09:10:00Z",
};

const detail: AlertDetail = {
  ...open,
  history: [
    { at: "2026-05-01T09:00:00Z", kind: "opened", level: "danger" },
    { at: "2026-05-01T09:10:00Z", kind: "escalated", level: "extreme-danger", note: "forecast worsened" },
  ],
  notifications: [
    { id: 1, subscriptionId: 1, kind: "opened", level: "danger", channel: "log", status: "sent", attempts: 1, createdAt: "2026-05-01T09:00:00Z" },
    { id: 2, subscriptionId: 2, kind: "opened", level: "danger", channel: "webhook", status: "failed", attempts: 10, createdAt: "2026-05-01T09:00:00Z" },
  ],
};

const render = (el: React.ReactElement) => renderToStaticMarkup(el);
const noop = () => {};

describe("AlertCard", () => {
  test("an open alert tells the story: what, how bad, why, when", () => {
    const html = render(<AlertCard alert={open} now={now} expanded={false} onToggle={noop} detail={{ status: "idle" }} />);
    expect(html).toContain("Extreme Danger heat alert for Mumbai");
    expect(html).toContain(">Open<");
    expect(html).toContain(">Extreme Danger<");
    for (const line of open.details.rationale) expect(html).toContain(line.replace(/&/g, "&amp;").replace(/—/g, "—"));
    expect(html).toContain("Warning days");
    expect(html).toContain("Peak chance");
    expect(html).toContain("trained model");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("Loading details");
  });

  test("it is labelled by its own headline for screen readers", () => {
    const html = render(<AlertCard alert={open} now={now} expanded={false} onToggle={noop} detail={{ status: "idle" }} />);
    expect(html).toContain('aria-labelledby="alert-3-title"');
    expect(html).toContain('id="alert-3-title"');
  });

  test("a resolved alert shows how bad it got, when it ended, and its history of reopening", () => {
    const html = render(<AlertCard alert={resolved} now={now} expanded={false} onToggle={noop} detail={{ status: "idle" }} />);
    expect(html).toContain(">Resolved<");
    expect(html).toContain("Peaked at Extreme Danger");
    expect(html).toContain("reopened 2×");
    expect(html).toContain("acknowledged");
    expect(html).toContain("Resolved 10m ago");
  });

  test("expanded: loading, error and ready states", () => {
    const loading = render(<AlertCard alert={open} now={now} expanded onToggle={noop} detail={{ status: "loading" }} />);
    expect(loading).toContain("Loading details");
    expect(loading).toContain('aria-expanded="true"');
    expect(loading).toContain('id="alert-3-details"');

    const error = render(<AlertCard alert={open} now={now} expanded onToggle={noop} detail={{ status: "error" }} />);
    expect(error).toContain('role="alert"');
    expect(error).toContain("Could not load");

    const ready = render(<AlertCard alert={open} now={now} expanded onToggle={noop} detail={{ status: "ready", data: detail }} />);
    expect(ready).toContain("Opened at Danger");
    expect(ready).toContain("Escalated to Extreme Danger");
    expect(ready).toContain("forecast worsened");
    expect(ready).toContain("1 sent, 1 failed");
    expect(ready).toContain("after 10 attempts");
  });

  test("renders before the page has a clock without printing placeholders", () => {
    const html = render(<AlertCard alert={open} now={null} expanded={false} onToggle={noop} detail={{ status: "idle" }} />);
    expect(html).not.toContain("undefined");
    expect(html).not.toContain("NaN");
    expect(html).toContain("Opened ");
  });
});

describe("ServiceStatusCard", () => {
  const base: ServiceStatus = { name: "risk", status: "ok", ready: true, latencyMs: 1.7, checks: { database: "ok" }, circuit: "closed" };

  test("a healthy service", () => {
    const html = render(<ServiceStatusCard service={base} step={4} />);
    expect(html).toContain("Risk assessment");
    expect(html).toContain("Operational");
    expect(html).toContain("1.7 ms");
    expect(html).toContain(">4<");
    expect(html).not.toContain("Circuit open");
  });

  test("not ready: says why", () => {
    const html = render(<ServiceStatusCard service={{ ...base, status: "unavailable", ready: false, checks: { database: "disk I/O error" } }} step={1} />);
    expect(html).toContain("Not ready");
    expect(html).toContain("disk I/O error");
  });

  test("down: no latency, the error, and the open circuit explained", () => {
    const html = render(<ServiceStatusCard service={{ ...base, status: "down", ready: false, latencyMs: 0, error: "not reachable", circuit: "open", checks: undefined }} step={4} />);
    expect(html).toContain("Down");
    expect(html).toContain("not reachable");
    expect(html).not.toContain("Response");
    expect(html).toContain("refusing requests");
  });
});

describe("BackendNotice", () => {
  test("not configured: an explanation, and no pointless retry button", () => {
    const html = render(<BackendNotice what="Alerts" reason="disabled" onRetry={noop} />);
    expect(html).toContain("Backend not connected");
    expect(html).toContain("still works");
    expect(html).not.toContain("Try again");
  });

  test("unreachable: offers a retry", () => {
    const html = render(<BackendNotice what="Service health details" reason="unavailable" onRetry={noop} />);
    expect(html).toContain("Backend unavailable");
    expect(html).toContain("service health details are unavailable");
    expect(html).toContain("Try again");
  });

  test("every other reason has its own wording", () => {
    const texts = new Set(["rate_limited", "timeout", "bad_response", "not_ready", "unavailable"].map((r) => render(<BackendNotice what="Alerts" reason={r as "timeout"} />)));
    expect(texts.size).toBe(5);
  });
});

describe("ModelCard", () => {
  const model: ModelInfo = {
    loaded: true,
    version: "lr-20261004-2635e65f",
    training: { cities: new Array(37).fill("x"), years: "1991-2024", samples: 1, baseRate: 0.06, target: "heatwave-warning day" },
    evaluation: {
      holdoutYears: "2020-2024",
      perHorizon: [
        { horizon: 0, auc: 0.99, brier: 0.01, brierClimatology: 0.059 },
        { horizon: 6, auc: 0.98, brier: 0.023, brierClimatology: 0.059 },
      ],
      leaveOneCityOut: { cities: [{ city: "Dubai", auc: 0.9944 }, { city: "Nowhere", auc: null }] },
    },
  };

  test("states what it is and how well it did, in plain terms", () => {
    const html = render(<ModelCard model={model} />);
    expect(html).toContain("lr-20261004-2635e65f");
    expect(html).toContain("1991-2024 weather from 37 cities");
    expect(html).toContain("83% lower error than climatology today, 61% 1 days out");
    expect(html).toContain("Dubai (AUC 0.9944)");
    expect(html).not.toContain("Nowhere");
  });

  test("with no model it says the fallback is in use", () => {
    const html = render(<ModelCard model={{ loaded: false }} />);
    expect(html).toContain("rule-based fallback");
  });
});
