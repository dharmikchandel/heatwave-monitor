import type { Metadata } from "next";
import AccountView from "@/components/AccountView";
import RequireAuth from "@/components/RequireAuth";
export const metadata: Metadata = {
  title: "Your account — Heatwave Monitor",
  description: "Your saved cities, alert settings and sign-in details.",
};

export default function Page() {
  return <RequireAuth what="your account"><AccountView /></RequireAuth>;
}
