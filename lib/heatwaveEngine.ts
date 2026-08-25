// Pure, client-side heatwave prediction & risk engine.
// No network calls, no side effects — everything here is deterministic math
// over the numbers the API layer hands it.

import type { DailyRiskForecast, DailyWeather, HeatRiskLevel, HeatwaveAssessment, SafetyTip } from "./types";

const celsiusToFahrenheit = (c: number) => (c * 9) / 5 + 32;
const fahrenheitToCelsius = (f: number) => ((f - 32) * 5) / 9;

/**
 * Steadman/NWS Rothfusz regression heat index.
 * The regression is only valid roughly above 27C / 40% RH; below that band
 * the "feels like" temperature is close to the ambient temperature, per NWS
 * guidance, so we fall back to a simple average-based approximation.
 */
export function calculateHeatIndex(tempC: number, humidity: number): number {
  const T = celsiusToFahrenheit(tempC);
  const RH = Math.min(100, Math.max(0, humidity));

  const simpleHI = 0.5 * (T + 61 + (T - 68) * 1.2 + RH * 0.094);

  if ((simpleHI + T) / 2 < 80) {
    return fahrenheitToCelsius(simpleHI);
  }

  let HI =
    -42.379 +
    2.04901523 * T +
    10.14333127 * RH -
    0.22475541 * T * RH -
    0.00683783 * T * T -
    0.05481717 * RH * RH +
    0.00122874 * T * T * RH +
    0.00085282 * T * RH * RH -
    0.00000199 * T * T * RH * RH;

  if (RH < 13 && T >= 80 && T <= 112) {
    HI -= ((13 - RH) / 4) * Math.sqrt((17 - Math.abs(T - 95)) / 17);
  } else if (RH > 85 && T >= 80 && T <= 87) {
    HI += ((RH - 85) / 10) * ((87 - T) / 5);
  }

  return fahrenheitToCelsius(HI);
}

/** WMO-aligned apparent-temperature thresholds, in Celsius. */
export const HEAT_RISK_THRESHOLDS_C = {
  caution: 32,
  extremeCaution: 38,
  danger: 41,
  extremeDanger: 54,
} as const;

function countConsecutiveDaysAtOrAbove(values: number[], threshold: number): number {
  let count = 0;
  for (let i = values.length - 1; i >= 0; i -= 1) {
    if (values[i] >= threshold) {
      count += 1;
    } else {
      break;
    }
  }
  return count;
}

/**
 * Determines the current heat risk level.
 * `daysMaxTemp` is a trailing series of daily max apparent temperatures
 * (oldest first, most recent/today last) used to detect sustained heatwave
 * conditions — the WMO "danger" tier requires >=2 consecutive days at or
 * above the danger threshold, not just a single hot reading.
 */
export function evaluateHeatRisk(tempC: number, apparentTempC: number, daysMaxTemp: number[] = []): HeatRiskLevel {
  const { caution, extremeCaution, danger, extremeDanger } = HEAT_RISK_THRESHOLDS_C;

  if (apparentTempC >= extremeDanger) return "extreme-danger";

  if (apparentTempC >= danger) {
    const series = daysMaxTemp.length > 0 ? daysMaxTemp : [apparentTempC];
    const consecutive = countConsecutiveDaysAtOrAbove(series, danger);
    return consecutive >= 2 ? "danger" : "extreme-caution";
  }

  if (apparentTempC >= extremeCaution) return "extreme-caution";
  if (apparentTempC >= caution) return "caution";
  return "normal";
}

export function assessHeatwave(
  tempC: number,
  apparentTempC: number,
  humidity: number,
  dailyApparentMax: number[],
): HeatwaveAssessment {
  const riskLevel = evaluateHeatRisk(tempC, apparentTempC, dailyApparentMax);
  const heatIndexC = calculateHeatIndex(tempC, humidity);
  const consecutiveDangerDays = countConsecutiveDaysAtOrAbove(dailyApparentMax, HEAT_RISK_THRESHOLDS_C.danger);

  const messages: Record<HeatRiskLevel, string> = {
    normal: "Conditions are within a safe, comfortable range.",
    caution: "Heat is building. Stay hydrated and limit strenuous outdoor activity during peak hours.",
    "extreme-caution": "Elevated heat stress risk. Heat cramps and exhaustion are possible with prolonged exposure.",
    danger: `Heatwave warning: dangerous heat has persisted for ${consecutiveDangerDays} consecutive day${consecutiveDangerDays === 1 ? "" : "s"}. Heat exhaustion is likely, heat stroke is possible.`,
    "extreme-danger": "Extreme danger: heat stroke is imminent with continued exposure. Avoid outdoor activity entirely.",
  };

  return {
    riskLevel,
    heatIndexC,
    consecutiveDangerDays,
    isHeatwaveWarning: riskLevel === "danger" || riskLevel === "extreme-danger",
    message: messages[riskLevel],
  };
}

export function generateAdvisories(riskLevel: HeatRiskLevel): SafetyTip[] {
  const base: SafetyTip[] = [
    { title: "Hydrate often", description: "Drink water regularly even if you don't feel thirsty. Avoid alcohol and excess caffeine." },
    { title: "Time it right", description: "Schedule outdoor activity for early morning or evening when temperatures are lower." },
  ];

  const byLevel: Record<HeatRiskLevel, SafetyTip[]> = {
    normal: [
      { title: "Enjoy the outdoors", description: "Conditions are comfortable — a good day for normal outdoor activity." },
      ...base,
    ],
    caution: [
      ...base,
      { title: "Dress light", description: "Wear lightweight, light-colored, loose-fitting clothing and sunscreen." },
    ],
    "extreme-caution": [
      ...base,
      { title: "Watch for symptoms", description: "Know the signs of heat cramps and heat exhaustion: heavy sweating, weakness, dizziness." },
      { title: "Seek shade & AC", description: "Take frequent breaks in air-conditioned or shaded spaces." },
    ],
    danger: [
      { title: "Limit outdoor exposure", description: "Avoid strenuous outdoor activity, especially between 11am and 4pm." },
      { title: "Check on vulnerable people", description: "Elderly neighbors, infants, and outdoor workers are at highest risk — check in regularly." },
      { title: "Recognize heat exhaustion", description: "Cool, pale, clammy skin, nausea, or a fast pulse means stop activity and cool down immediately." },
      ...base,
    ],
    "extreme-danger": [
      { title: "Stay indoors", description: "Avoid all outdoor activity. Stay in air-conditioned locations if at all possible." },
      { title: "Heat stroke is a medical emergency", description: "Confusion, hot/dry skin, or loss of consciousness requires immediate emergency medical care." },
      { title: "Never leave anyone in a parked vehicle", description: "Cabin temperatures can become lethal within minutes." },
      ...base,
    ],
  };

  return byLevel[riskLevel];
}

/**
 * Compares each day's max temperature against the rolling baseline average
 * of the preceding days to flag an upward heatwave trajectory.
 */
export function computeTrendAnomaly(dailyTempMax: number[]): { isRising: boolean; anomalyC: number } {
  if (dailyTempMax.length < 2) {
    return { isRising: false, anomalyC: 0 };
  }
  const baseline = dailyTempMax.slice(0, -1);
  const baselineAvg = baseline.reduce((sum, t) => sum + t, 0) / baseline.length;
  const latest = dailyTempMax[dailyTempMax.length - 1];
  const anomalyC = latest - baselineAvg;
  return { isRising: anomalyC > 1, anomalyC };
}

export function buildDailyRiskForecast(daily: DailyWeather): DailyRiskForecast[] {
  const forecasts: DailyRiskForecast[] = [];
  const runningApparentMax: number[] = [];

  for (let i = 0; i < daily.time.length; i += 1) {
    const apparentTempMax = daily.apparentTemperatureMax[i];
    runningApparentMax.push(apparentTempMax);
    const riskLevel = evaluateHeatRisk(daily.temperature2mMax[i], apparentTempMax, runningApparentMax);

    forecasts.push({
      date: daily.time[i],
      tempMax: daily.temperature2mMax[i],
      tempMin: daily.temperature2mMin[i],
      apparentTempMax,
      uvIndexMax: daily.uvIndexMax[i],
      precipitationSum: daily.precipitationSum[i],
      riskLevel,
    });
  }

  return forecasts;
}

export const RISK_LEVEL_LABEL: Record<HeatRiskLevel, string> = {
  normal: "Normal",
  caution: "Caution",
  "extreme-caution": "Extreme Caution",
  danger: "Danger",
  "extreme-danger": "Extreme Danger",
};

export const RISK_LEVEL_ORDER: HeatRiskLevel[] = ["normal", "caution", "extreme-caution", "danger", "extreme-danger"];
