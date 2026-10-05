import type { Metadata } from "next";
import RegisterForm from "@/components/RegisterForm";
export const metadata: Metadata = {
  title: "Create an account — Heatwave Monitor",
  description: "Create a Heatwave Monitor account to save cities and get heat alerts.",
};

export default function Page() {
  return <RegisterForm />;
}
