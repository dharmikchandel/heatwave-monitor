import type { Metadata } from "next";
import LoginForm from "@/components/LoginForm";
export const metadata: Metadata = {
  title: "Sign in — Heatwave Monitor",
  description: "Sign in to Heatwave Monitor.",
};

export default function Page() {
  return <LoginForm />;
}
