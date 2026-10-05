"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { describeError, MIN_PASSWORD_LENGTH, passwordProblem, safeNextPath } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { cn, FOCUS_RING } from "@/lib/utils";
import { AccountsUnavailable, AuthCard, Field, Form, FormMessage, PasswordField, SubmitButton } from "./AccountForm";

export default function RegisterForm() {
  const { status, signUp } = useAuth();
  const router = useRouter();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (status === "signed-in") router.replace(safeNextPath(new URLSearchParams(window.location.search).get("next")));
  }, [status, router]);

  if (status === "unavailable") return <AccountsUnavailable />;

  async function submit() {
    const problem = passwordProblem(password);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError(null);
    const res = await signUp({ email: email.trim(), password, displayName: name.trim() });
    if (!res.ok) {
      setError(describeError(res.error));
      setBusy(false);
    }
  }

  return (
    <AuthCard
      title="Create an account"
      subtitle="Free. Save cities, and get heat alerts in your inbox."
      footer={
        <>
          Already have an account?{" "}
          <Link href="/login" className={cn("rounded font-semibold text-orange-600 hover:underline dark:text-orange-400", FOCUS_RING)}>
            Sign in
          </Link>
        </>
      }
    >
      <Form label="Create an account" onSubmit={submit}>
        <Field label="Name" value={name} onChange={setName} autoComplete="name" required={false} disabled={busy} maxLength={60} />
        <Field label="Email" type="email" value={email} onChange={setEmail} autoComplete="email" disabled={busy} />
        <PasswordField
          label="Password"
          value={password}
          onChange={setPassword}
          autoComplete="new-password"
          hint={`At least ${MIN_PASSWORD_LENGTH} characters. A few random words make a strong, memorable password.`}
          disabled={busy}
        />
        <FormMessage tone="error">{error}</FormMessage>
        <SubmitButton busy={busy || status === "signed-in"}>Create account</SubmitButton>
      </Form>
    </AuthCard>
  );
}
