import type { Metadata } from "next";
import type { ReactNode } from "react";

export const metadata: Metadata = {
  title: "System Status — Heatwave Monitor",
  description: "Live health of the Heatwave Monitor backend services, and the heatwave prediction model behind the forecasts.",
};

export default function StatusLayout({ children }: { children: ReactNode }) {
  return children;
}
