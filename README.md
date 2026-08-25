# Heatwave Monitor

**Climate intelligence for heatwave monitoring, prediction, and early warning.**

Heatwave Monitor is a real-time climate dashboard that fetches live weather data for any city on Earth, runs it through a custom heat-risk prediction engine, and visualizes the result as an interactive, accessible, four-page web app — a current-conditions dashboard, an extended forecast/trend view, a full heat-safety reference guide, and a methodology write-up.

> Built with Next.js 16 (App Router) + TypeScript, Tailwind CSS v4, Recharts, and Framer Motion.

---

## Table of Contents

- [Features](#features)
- [Pages](#pages)
- [How It Works](#how-it-works)
- [The Prediction Engine](#the-prediction-engine)
- [Tech Stack](#tech-stack)
- [Project Structure](#project-structure)
- [Getting Started](#getting-started)
- [Design System](#design-system)
- [Accessibility](#accessibility)
- [Data Source & Attribution](#data-source--attribution)
- [Deployment](#deployment)
- [Author](#author)

---

## Features

- **Live current-conditions dashboard** — current temperature, "feels like" temperature, humidity, UV index, and a computed heat index, each with an animated count-up and an hour-over-hour trend indicator.
- **Animated Heat Risk gauge** — a circular gauge classifying conditions into 5 WMO-aligned tiers (Normal → Caution → Extreme Caution → Danger → Extreme Danger), colored and labeled accordingly.
- **Early-warning alert banner** — automatically appears when the 7-day forecast contains a Danger or Extreme Danger day, and names exactly which day(s).
- **Interactive analytics chart** — switchable between an Hourly (24h) view and a 7-Day Forecast view, with gradient-filled temperature and heat-index curves and hover tooltips.
- **7-day forecast cards** — each day color-coded by its own computed risk tier.
- **Extended forecast page** — a trend-anomaly indicator (is the forecast trending hotter or cooler than the surrounding baseline?) and an hour-by-hour data table for the next 24 hours.
- **Full heat-safety reference guide** — every risk tier's thresholds and advisories in one place, plus heat-illness recognition and first-aid guidance, independent of the currently selected city.
- **Methodology page** — explains the heat index formula and risk thresholds in plain language, including a live, server-computed worked example.
- **City search** — debounced autosuggest with full keyboard navigation (arrow keys, Enter, Escape) and screen-reader support, or one click to use your device's location.
- **Unit and theme toggles** — °C/°F and light/dark mode, both remembered between visits.
- **Shareable report export** — generates a downloadable PNG summary card and a one-click "copy as text" summary.
- **Responsive across breakpoints** — mobile, tablet, and desktop layouts, tested down to 375px wide.

## Pages

| Route | Name | Purpose |
|---|---|---|
| `/` | **Dashboard** | The live snapshot: alert banner, risk gauge, metric cards, analytics chart, 7-day forecast, and contextual safety advisories for the selected city. |
| `/forecast` | **Forecast** | A deeper dive: the full analytics chart and 7-day outlook, plus a trend-anomaly card and an hourly detail table. |
| `/safety` | **Safety** | A standalone reference: all 5 risk tiers explained with their advisories, heat-illness stages (cramps → exhaustion → stroke) with symptoms and response steps, and an emergency-services callout. |
| `/about` | **About** | The methodology behind the numbers — the heat index formula, the WMO threshold table, the tech stack, and a live worked example. |

All four pages share one header, one navigation bar, and one live data source — selecting a city or switching units/themes on any page carries over to the rest instantly.

## How It Works

1. **A city is selected** — either typed into the search box (which queries Open-Meteo's geocoding API) or resolved from the browser's Geolocation API, defaulting to Mumbai if neither is available.
2. **Weather data is fetched** — a single request to the Open-Meteo forecast API returns current conditions, 24 hours of hourly data, and a 7-day daily forecast for that location. Results are cached for 10 minutes so switching between pages or quickly reselecting a city doesn't refetch unnecessarily.
3. **The prediction engine runs** — every metric shown on screen (heat index, risk tier, trend anomaly, safety advisories) is *computed*, not fetched — Open-Meteo returns raw meteorological data, and `lib/heatwaveEngine.ts` turns that into the actual risk assessment.
4. **State is shared across pages** — a single `ClimateProvider` (React Context, `lib/ClimateContext.tsx`) owns the selected city, the fetched data, and the derived risk assessment, so every route reads from one consistent source of truth instead of re-fetching independently.
5. **The UI reacts** — components subscribe to that shared state and render accordingly, with Framer Motion handling entrance/pulse animations (respecting `prefers-reduced-motion`) and Recharts rendering the interactive charts.

## The Prediction Engine

All of the logic below lives in `lib/heatwaveEngine.ts` and is pure, deterministic, and independently testable — it takes numbers in and returns a classification out.

**Heat Index** — computed with the NWS/Steadman Rothfusz regression, the same formula used by the US National Weather Service to calculate "feels like" temperature from ambient temperature and relative humidity:

```
HI = -42.379 + 2.049·T + 10.143·RH - 0.225·T·RH - 0.0068·T² - 0.0548·RH²
     + 0.00123·T²·RH + 0.00085·T·RH² - 0.0000199·T²·RH²
```

(T in °F, RH in %, with secondary adjustments at low/high humidity extremes.) Below roughly 27°C/40% RH, where the regression isn't valid, the engine falls back to a simpler average-based approximation per NWS guidance.

**Risk Tiers** — apparent temperature is classified against WMO-aligned thresholds:

| Tier | Threshold |
|---|---|
| Normal | below 32°C |
| Caution | ≥ 32°C |
| Extreme Caution | ≥ 38°C |
| Danger | ≥ 41°C, sustained for 2+ consecutive days |
| Extreme Danger | ≥ 54°C |

Note the **Danger** tier specifically requires two or more consecutive days at or above 41°C — a single hot day is classified as merely "Extreme Caution," which is what makes this a genuine *heatwave* detector rather than a simple thermometer.

**Trend Anomaly** — compares each day's forecast high against the rolling average of the surrounding days in the 7-day window, surfacing whether temperatures are trending upward, downward, or holding steady.

## Tech Stack

| Layer | Choice | Why |
|---|---|---|
| Framework | [Next.js 16](https://nextjs.org) (App Router, TypeScript) | File-based routing across 4 pages, React Server Components for the static `/about` page |
| Styling | [Tailwind CSS v4](https://tailwindcss.com) | CSS-first theming (`@theme`) for the dark/light design system, utility-first for rapid, consistent styling |
| Charts | [Recharts](https://recharts.org) | Smooth, responsive, accessible-friendly SVG charts with gradient fills and custom tooltips |
| Animation | [Framer Motion](https://www.framer.com/motion/) | Entrance transitions, gauge fill animation, pulsing alerts — with `useReducedMotion()` support |
| Icons | [Lucide](https://lucide.dev) | Consistent icon set throughout |
| Analytics | [Vercel Analytics](https://vercel.com/analytics) | Privacy-respecting page view metrics when deployed on Vercel |
| State | React Context (`lib/ClimateContext.tsx`) | One shared data pipeline across all 4 routes — no external state library needed |
| Data source | [Open-Meteo](https://open-meteo.com) | Free, keyless weather and geocoding API |

## Project Structure

```text
app/
├── layout.tsx          # Root layout — theme bootstrap, ClimateProvider, Header, footer, Analytics
├── page.tsx             # Dashboard (/)
├── globals.css          # Design tokens, glassmorphism, scrollbars, reduced-motion rules
├── forecast/page.tsx     # Extended Forecast (/forecast)
├── safety/page.tsx       # Heat Safety Guide (/safety)
└── about/page.tsx        # About This Project (/about)

components/
├── Header.tsx            # Search, geolocation, unit/theme toggles, page navigation
├── AlertBanner.tsx        # Early-warning banner
├── HeatwaveRiskCard.tsx    # Circular risk gauge
├── MetricsGrid.tsx         # Current-conditions metric cards
├── AnalyticsChart.tsx      # Hourly / 7-day chart
├── Forecast7Day.tsx         # 7-day forecast cards
├── SafetyAdvisory.tsx        # Current-risk advisories (Dashboard)
├── ExportReportModal.tsx      # PNG/text report export
├── ThemeToggle.tsx             # Light/dark switch
└── AnimatedNumber.tsx           # Count-up number primitive

lib/
├── heatwaveEngine.ts      # Heat index formula, risk classification, advisories, trend anomaly
├── api.ts                  # Open-Meteo fetchers, error handling, localStorage cache
├── ClimateContext.tsx        # Shared location/climate-data state across all pages
├── types.ts                    # Shared domain types
└── utils.ts                     # Formatting helpers, shared style tokens
```

## Getting Started

**Prerequisites:** [Bun](https://bun.sh) (or npm/yarn/pnpm — swap the commands below accordingly).

```bash
# Install dependencies
bun install

# Start the dev server (Turbopack) at http://localhost:3000
bun run dev

# Type-check
bunx tsc --noEmit

# Lint
bun run lint

# Production build
bun run build

# Run the production build locally
bun run start
```

No environment variables or API keys are required — the weather API used is free and keyless.

## Design System

The **"Solar Thermal"** visual identity transitions from a deep slate background to warm amber, solar orange, and crimson as risk escalates:

| Purpose | Color |
|---|---|
| Background (dark / light) | `#0B0F17` / `#F8FAFC` |
| Normal | Emerald `#10B981` |
| Caution | Amber `#F59E0B` |
| Extreme Caution | Orange `#EA580C` |
| Danger | Red `#DC2626` |
| Extreme Danger | Deep Red `#991B1B` |

Implemented as CSS custom properties via Tailwind v4's `@theme` directive, switchable at runtime via a `data-theme` attribute with no flash-of-wrong-theme on load. Cards use a glassmorphism treatment (`backdrop-filter: blur` over a translucent surface color).

## Accessibility

- Full keyboard navigation: the city search is a proper ARIA combobox (arrow keys, Enter, Escape), and the export modal has a focus trap with focus save/restore.
- A shared, keyboard-only (`:focus-visible`) focus ring is applied consistently across every interactive element.
- ARIA labeling throughout: `role="alert"` on the warning banner, `role="img"` with descriptive summaries on the risk gauge and chart, labeled forecast cards, proper dialog semantics on the modal.
- Risk-tier text colors meet WCAG AA contrast (4.5:1) in both light and dark themes — decorative and text uses of the same risk color are intentionally split.
- Respects `prefers-reduced-motion`, disabling looping/pulsing animations for users who request it.

## Data Source & Attribution

Weather and geocoding data is provided by the [Open-Meteo API](https://open-meteo.com), used under their free, non-commercial license. All risk classifications, the heat index, and the trend analysis shown on this site are computed by this project's own prediction engine — Open-Meteo supplies the raw meteorological inputs only.

## Deployment

This project deploys cleanly to [Vercel](https://vercel.com) with zero configuration:

1. Push this repository to GitHub/GitLab/Bitbucket.
2. Import it in the [Vercel dashboard](https://vercel.com/new) — Next.js is auto-detected.
3. Deploy. No environment variables are required.
4. To see Web Analytics data, open the project in the Vercel dashboard and enable **Analytics** for it — the `<Analytics />` component is already wired into the app, but Vercel's dashboard toggle is a separate, one-time step.

## Author

Built by dharmikchandel.
