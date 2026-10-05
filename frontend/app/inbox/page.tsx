import type { Metadata } from "next";
import InboxView from "@/components/InboxView";
import RequireAuth from "@/components/RequireAuth";
export const metadata: Metadata = {
  title: "Inbox — Heatwave Monitor",
  description: "Heat alerts for the cities you saved.",
};

export default function Page() {
  return <RequireAuth what="your inbox"><InboxView /></RequireAuth>;
}
