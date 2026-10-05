"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { describeError, safeNextPath } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { cn, FOCUS_RING } from "@/lib/utils";
import { AccountsUnavailable, AuthCard, Field, Form, FormMessage, PasswordField, SubmitButton } from "./AccountForm";

/** Where ?next= points, read when needed (useSearchParams would force a Suspense boundary on the whole page). */
function nextPath(): string {
  return safeNextPath(new URLSearchParams(window.location.search).get("next"));
}

export default function LoginForm() {
  const { status, signIn } = useAuth();
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Signed in (just now, or already): go where the person was headed.
  useEffect(() => {
    if (status === "signed-in") router.replace(nextPath());
  }, [status, router]);

  if (status === "unavailable") return <AccountsUnavailable />;

  async function submit() {
    setBusy(true);
    setError(null);
    const res = await signIn(email.trim(), password);
    if (!res.ok) {
      setError(describeError(res.error));
      setBusy(false);
    }
  }

  return (
    <AuthCard
      title="Sign in"
      subtitle="Save the cities you care about and get alerts in your inbox."
      footer={
        <>
          New here?{" "}
          <Link href="/register" className={cn("rounded font-semibold text-orange-600 hover:underline dark:text-orange-400", FOCUS_RING)}>
            Create an account
          </Link>
        </>
      }
    >
      <Form label="Sign in" onSubmit={submit}>
        <Field label="Email" type="email" value={email} onChange={setEmail} autoComplete="email" disabled={busy} />
        <PasswordField label="Password" value={password} onChange={setPassword} autoComplete="current-password" disabled={busy} />
        <FormMessage tone="error">{error}</FormMessage>
        <SubmitButton busy={busy || status === "signed-in"}>Sign in</SubmitButton>
      </Form>
    </AuthCard>
  );
}
