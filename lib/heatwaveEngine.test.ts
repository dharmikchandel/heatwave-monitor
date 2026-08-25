import { describe, expect, test } from "bun:test";
import { calculateHeatIndex, computeTrendAnomaly, evaluateHeatRisk, HEAT_RISK_THRESHOLDS_C } from "./heatwaveEngine";

describe("calculateHeatIndex", () => {
  test("stays close to ambient temperature in mild, dry conditions", () => {
    const hi = calculateHeatIndex(20, 30);
    expect(hi).toBeGreaterThan(15);
    expect(hi).toBeLessThan(25);
  });

  test("exceeds ambient temperature in hot, humid conditions", () => {
    const hi = calculateHeatIndex(35, 80);
    expect(hi).toBeGreaterThan(35);
  });
});

describe("evaluateHeatRisk", () => {
  test("classifies below-threshold conditions as normal", () => {
    expect(evaluateHeatRisk(25, 25)).toBe("normal");
  });

  test("classifies each WMO threshold correctly", () => {
    expect(evaluateHeatRisk(30, HEAT_RISK_THRESHOLDS_C.caution)).toBe("caution");
    expect(evaluateHeatRisk(35, HEAT_RISK_THRESHOLDS_C.extremeCaution)).toBe("extreme-caution");
    expect(evaluateHeatRisk(60, HEAT_RISK_THRESHOLDS_C.extremeDanger)).toBe("extreme-danger");
  });

  test("requires two consecutive dangerous days before escalating to danger", () => {
    const singleHotDay = [HEAT_RISK_THRESHOLDS_C.danger];
    expect(evaluateHeatRisk(40, HEAT_RISK_THRESHOLDS_C.danger, singleHotDay)).toBe("extreme-caution");

    const twoHotDays = [HEAT_RISK_THRESHOLDS_C.danger, HEAT_RISK_THRESHOLDS_C.danger];
    expect(evaluateHeatRisk(40, HEAT_RISK_THRESHOLDS_C.danger, twoHotDays)).toBe("danger");
  });
});

describe("computeTrendAnomaly", () => {
  test("reports no trend with fewer than two data points", () => {
    expect(computeTrendAnomaly([30])).toEqual({ isRising: false, anomalyC: 0 });
  });

  test("flags a rising trend when the latest day is hotter than the baseline", () => {
    const { isRising, anomalyC } = computeTrendAnomaly([30, 31, 30, 35]);
    expect(isRising).toBe(true);
    expect(anomalyC).toBeGreaterThan(1);
  });

  test("does not flag a rising trend when temperatures hold steady", () => {
    const { isRising } = computeTrendAnomaly([30, 30, 30, 30]);
    expect(isRising).toBe(false);
  });
});
