"use client";

import { useClimate } from "@/lib/ClimateContext";

/** The footer's attribution, truthful about where the analytics were computed. */
export default function FooterNote() {
  const { dataSource } = useClimate();
  return dataSource === "backend" ? (
    <>Weather from Open-Meteo, processed by the Heatwave Monitor backend: data cleaning, a trained heatwave model and risk assessment.</>
  ) : (
    <>Climate data from Open-Meteo. All analytics computed locally in your browser.</>
  );
}
