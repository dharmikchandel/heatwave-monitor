import { describe, expect, test } from "bun:test";
import vectors from "../testdata/engine-vectors.json";
import {
  assessHeatwave,
  buildDailyRiskForecast,
  calculateHeatIndex,
  computeTrendAnomaly,
  evaluateHeatRisk,
  HEAT_RISK_THRESHOLDS_C,
} from "./heatwaveEngine";

// The Go port in services/internal/engine is checked against the same vectors,
// so a behaviour change here fails this test until `bun run vectors` is re-run
// and the Go engine is updated to match.
describe("shared engine vectors", () => {
  test("thresholds match", () => {
    expect<Record<string, number>>({ ...HEAT_RISK_THRESHOLDS_C }).toEqual(vectors.thresholdsC);
  });

  test("heat index", () => {
    for (const v of vectors.heatIndex) {
      expect(calculateHeatIndex(v.tempC, v.humidity)).toBeCloseTo(v.expected, 9);
    }
  });

  test("risk level", () => {
    for (const v of vectors.evaluateHeatRisk) {
      const days = "daysMaxTemp" in v ? v.daysMaxTemp : undefined;
      expect<string>(evaluateHeatRisk(v.tempC, v.apparentTempC, days)).toBe(v.expected);
    }
  });

  test("assessment", () => {
    for (const v of vectors.assessHeatwave) {
      const got = assessHeatwave(v.tempC, v.apparentTempC, v.humidity, v.dailyApparentMax);
      expect<string>(got.riskLevel).toBe(v.expected.riskLevel);
      expect(got.heatIndexC).toBeCloseTo(v.expected.heatIndexC, 9);
      expect(got.consecutiveDangerDays).toBe(v.expected.consecutiveDangerDays);
      expect(got.isHeatwaveWarning).toBe(v.expected.isHeatwaveWarning);
      expect(got.message).toBe(v.expected.message);
    }
  });

  test("trend anomaly", () => {
    for (const v of vectors.trendAnomaly) {
      const got = computeTrendAnomaly(v.dailyTempMax);
      expect(got.isRising).toBe(v.expected.isRising);
      expect(got.anomalyC).toBeCloseTo(v.expected.anomalyC, 9);
    }
  });

  test("daily risk forecast", () => {
    for (const v of vectors.dailyRiskForecast) {
      expect<unknown>(buildDailyRiskForecast(v.daily)).toEqual(v.expected);
    }
  });
});

describe("calculateHeatIndex", () => {
  test("matches the published NWS value for 90°F / 70% RH (≈105.9°F)", () => {
    const fahrenheit = (calculateHeatIndex(((90 - 32) * 5) / 9, 70) * 9) / 5 + 32;
    expect(fahrenheit).toBeCloseTo(105.9, 0);
  });

  test("stays close to ambient temperature in mild, dry conditions", () => {
    const hi = calculateHeatIndex(20, 30);
    expect(hi).toBeGreaterThan(15);
    expect(hi).toBeLessThan(25);
  });

  test("exceeds ambient temperature in hot, humid conditions", () => {
    expect(calculateHeatIndex(35, 80)).toBeGreaterThan(35);
  });
});

describe("evaluateHeatRisk", () => {
  test("classifies below-threshold conditions as normal", () => {
    expect(evaluateHeatRisk(25, 25)).toBe("normal");
  });

  test("requires two consecutive dangerous days before escalating to danger", () => {
    const { danger } = HEAT_RISK_THRESHOLDS_C;
    expect(evaluateHeatRisk(40, danger, [danger])).toBe("extreme-caution");
    expect(evaluateHeatRisk(40, danger, [danger, danger])).toBe("danger");
  });
});

describe("computeTrendAnomaly", () => {
  test("reports no trend with fewer than two data points", () => {
    expect(computeTrendAnomaly([30])).toEqual({ isRising: false, anomalyC: 0 });
  });

  test("flags a rising trend when the latest day is hotter than the baseline", () => {
    expect(computeTrendAnomaly([30, 31, 30, 35]).isRising).toBe(true);
  });
});
