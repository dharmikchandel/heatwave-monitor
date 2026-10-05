import type { Metadata } from "next";
import type { ReactNode } from "react";

export const metadata: Metadata = {
  title: "Heat Alerts — Heatwave Monitor",
  description: "Open and resolved heatwave alerts for every watched city, with the reasoning and notification history behind each.",
};

export default function AlertsLayout({ children }: { children: ReactNode }) {
  return children;
}
