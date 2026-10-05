// Shared domain types for the heatwave monitoring dashboard.

export type HeatRiskLevel = "normal" | "caution" | "extreme-caution" | "danger" | "extreme-danger";

export interface SafetyTip {
  title: string;
  description: string;
}

export interface GeoLocation {
  id: number;
  name: string;
  country: string;
  admin1?: string;
  latitude: number;
  longitude: number;
  timezone: string;
}

export interface CurrentWeather {
  time: string;
  temperature2m: number;
  relativeHumidity2m: number;
  apparentTemperature: number;
  weatherCode: number;
  windSpeed10m: number;
  directNormalIrradiance: number;
}

export interface HourlyWeather {
  time: string[];
  temperature2m: number[];
  relativeHumidity2m: number[];
  apparentTemperature: number[];
  heatIndex: number[];
}

export interface DailyWeather {
  time: string[];
  temperature2mMax: number[];
  temperature2mMin: number[];
  apparentTemperatureMax: number[];
  uvIndexMax: number[];
  precipitationSum: number[];
}

export interface ClimateData {
  latitude: number;
  longitude: number;
  timezone: string;
  current: CurrentWeather;
  hourly: HourlyWeather;
  daily: DailyWeather;
}

export interface DailyRiskForecast {
  date: string;
  tempMax: number;
  tempMin: number;
  apparentTempMax: number;
  uvIndexMax: number;
  precipitationSum: number;
  riskLevel: HeatRiskLevel;
}

export interface HeatwaveAssessment {
  riskLevel: HeatRiskLevel;
  heatIndexC: number;
  consecutiveDangerDays: number;
  isHeatwaveWarning: boolean;
  message: string;
}

/** Where the data on screen came from. */
export type DataSource = "backend" | "local";

export interface BackendAlert {
  id: number;
  locationId: number;
  locationName: string;
  status: "open" | "resolved";
  currentLevel: HeatRiskLevel;
  peakLevel: HeatRiskLevel;
  headline: string;
  summary: string;
  openedAt: string;
}

/**
 * What the backend knows beyond the numbers the dashboard already shows: heatwave
 * probabilities from the trained model, the reasoning behind the risk level, and
 * open alerts. Present only when the data came from the backend.
 */
export interface BackendInsight {
  locationId: number;
  /** "ok", "partial" (something missing) or "warming" (new location). */
  status: string;
  /** True when a backend service is failing (not merely without data yet). */
  degraded: boolean;
  /** "model" (trained) or "rules"; null when no prediction was available. */
  method: string | null;
  modelVersion: string | null;
  /** Chance that each forecast day is a heatwave-warning day, keyed by date. */
  probabilityByDate: Record<string, number>;
  peak: { date: string; probability: number } | null;
  rationale: string[];
  /** The most severe tier now or expected in the forecast. */
  alertLevel: HeatRiskLevel | null;
  heatwaveExpected: boolean;
  openAlerts: BackendAlert[];
  /** 0-1: how much of the weather data was real rather than repaired. */
  dataQuality: number | null;
  generatedAt: string;
}
