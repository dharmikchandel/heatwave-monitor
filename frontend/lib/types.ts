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
