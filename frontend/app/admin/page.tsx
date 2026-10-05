import type { Metadata } from "next";
import AdminView from "@/components/AdminView";
import RequireAuth from "@/components/RequireAuth";
export const metadata: Metadata = {
  title: "Administration — Heatwave Monitor",
  description: "Manage Heatwave Monitor accounts.",
};

export default function Page() {
  return <RequireAuth what="administration" admin><AdminView /></RequireAuth>;
}
