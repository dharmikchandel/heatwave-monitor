import { Code2, Cpu, Database, ShieldCheck } from "lucide-react";
import { calculateHeatIndex, HEAT_RISK_THRESHOLDS_C, RISK_LEVEL_LABEL, RISK_LEVEL_ORDER } from "@/lib/heatwaveEngine";
import { RISK_LEVEL_COLOR } from "@/lib/utils";

const EXAMPLE_TEMP_C = 32;
const EXAMPLE_HUMIDITY = 70;
const exampleHeatIndex = calculateHeatIndex(EXAMPLE_TEMP_C, EXAMPLE_HUMIDITY);

const STACK = [
  { icon: Code2, title: "Next.js App Router + TypeScript", detail: "Every route in this project is statically typed and server/client-rendered per component, not per page." },
  { icon: Cpu, title: "Recharts + Framer Motion", detail: "Chart rendering and UI motion, tuned to respect prefers-reduced-motion." },
  { icon: Database, title: "Open-Meteo API", detail: "Free, keyless weather API — the only external service this project talks to." },
  { icon: ShieldCheck, title: "Zero backend", detail: "No servers, databases, or API routes of our own. Every calculation on this site runs in your browser." },
] as const;

export default function AboutPage() {
  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">About This Project</h1>
        <p className="mt-1 text-sm text-muted">
          Heatwave Monitor is a client-side climate intelligence dashboard: it fetches public weather data, runs a
          heatwave-prediction engine entirely in the browser, and visualizes the result — no backend required.
        </p>
      </div>

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">Architecture</h2>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {STACK.map((item) => (
            <div key={item.title} className="flex gap-3 rounded-xl border border-surface-border bg-surface/40 p-3">
              <item.icon className="h-5 w-5 shrink-0 text-orange-500" aria-hidden="true" />
              <div>
                <p className="text-sm font-semibold">{item.title}</p>
                <p className="mt-0.5 text-xs leading-relaxed text-muted">{item.detail}</p>
              </div>
            </div>
          ))}
        </div>
      </div>

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-2 text-sm font-bold uppercase tracking-wide text-muted">The Heat Index Formula</h2>
        <p className="text-sm leading-relaxed text-muted">
          &ldquo;Feels like&rdquo; temperature is computed with the NWS/Steadman Rothfusz regression — the same formula
          used by the US National Weather Service — combining ambient temperature and relative humidity. Below roughly
          27°C/40% RH, where the regression isn&apos;t valid, the engine falls back to a simpler average-based
          approximation, per NWS guidance.
        </p>
        <div className="mt-4 overflow-x-auto rounded-xl border border-surface-border bg-surface/40 p-4 font-mono text-xs leading-relaxed">
          HI = -42.379 + 2.049·T + 10.143·RH - 0.225·T·RH - 0.0068·T² - 0.0548·RH² + 0.00123·T²·RH + 0.00085·T·RH² -
          0.0000199·T²·RH²
          <br />
          <span className="text-muted">(T in °F, RH in %, with secondary adjustments at low/high humidity extremes)</span>
        </div>
        <div className="mt-4 rounded-xl border border-orange-500/30 bg-orange-500/10 p-4">
          <p className="text-xs font-semibold text-orange-700 dark:text-orange-400">Worked example</p>
          <p className="mt-1 text-sm">
            At {EXAMPLE_TEMP_C}°C with {EXAMPLE_HUMIDITY}% relative humidity, the computed heat index is{" "}
            <span className="font-bold">{exampleHeatIndex.toFixed(1)}°C</span> — computed server-side on this very page
            by the same <code className="rounded bg-surface px-1 py-0.5">calculateHeatIndex()</code> function the live
            dashboard uses.
          </p>
        </div>
      </div>

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-4 text-sm font-bold uppercase tracking-wide text-muted">WMO-Aligned Risk Thresholds</h2>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[420px] text-left text-sm">
            <thead>
              <tr className="border-b border-surface-border text-xs text-muted">
                <th scope="col" className="py-2 pr-3 font-medium">
                  Tier
                </th>
                <th scope="col" className="py-2 font-medium">
                  Apparent Temperature
                </th>
              </tr>
            </thead>
            <tbody>
              {RISK_LEVEL_ORDER.map((level) => (
                <tr key={level} className="border-b border-surface-border/60 last:border-0">
                  <td className="flex items-center gap-2 py-2 pr-3 font-medium">
                    <span className="h-2 w-2 rounded-full" style={{ backgroundColor: RISK_LEVEL_COLOR[level] }} aria-hidden="true" />
                    {RISK_LEVEL_LABEL[level]}
                  </td>
                  <td className="py-2 text-muted">
                    {level === "normal" && `< ${HEAT_RISK_THRESHOLDS_C.caution}°C`}
                    {level === "caution" && `≥ ${HEAT_RISK_THRESHOLDS_C.caution}°C`}
                    {level === "extreme-caution" && `≥ ${HEAT_RISK_THRESHOLDS_C.extremeCaution}°C`}
                    {level === "danger" && `≥ ${HEAT_RISK_THRESHOLDS_C.danger}°C for 2+ consecutive days`}
                    {level === "extreme-danger" && `≥ ${HEAT_RISK_THRESHOLDS_C.extremeDanger}°C`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="glass-card rounded-2xl p-4 sm:p-6">
        <h2 className="mb-2 text-sm font-bold uppercase tracking-wide text-muted">Privacy &amp; Data</h2>
        <p className="text-sm leading-relaxed text-muted">
          This site has no backend, no accounts, and no analytics. Weather data is fetched directly from Open-Meteo in
          your browser and cached in <code className="rounded bg-surface px-1 py-0.5">localStorage</code> for 10 minutes
          purely to avoid redundant requests. Your selected city, unit, and theme preference are stored the same way, on
          your device only.
        </p>
      </div>
    </>
  );
}
