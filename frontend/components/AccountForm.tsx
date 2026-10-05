"use client";

import { Eye, EyeOff, Laptop, Loader2 } from "lucide-react";
import Link from "next/link";
import { useId, useState, type FormEvent, type ReactNode } from "react";
import { cn, FOCUS_RING } from "@/lib/utils";

const INPUT =
  "w-full rounded-xl border border-surface-border bg-surface/60 px-3.5 py-2.5 text-sm transition placeholder:text-muted disabled:opacity-60";

interface FieldProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: "text" | "email";
  autoComplete?: string;
  hint?: string;
  required?: boolean;
  disabled?: boolean;
  maxLength?: number;
}

export function Field({ label, value, onChange, type = "text", autoComplete, hint, required = true, disabled, maxLength }: FieldProps) {
  const id = useId();
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-xs font-semibold">
        {label}
        {!required && <span className="font-normal text-muted"> (optional)</span>}
      </label>
      <input
        id={id}
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={autoComplete}
        required={required}
        disabled={disabled}
        maxLength={maxLength}
        aria-describedby={hint ? `${id}-hint` : undefined}
        className={cn(INPUT, FOCUS_RING)}
      />
      {hint && (
        <p id={`${id}-hint`} className="text-[11px] text-muted">
          {hint}
        </p>
      )}
    </div>
  );
}

interface PasswordFieldProps extends Omit<FieldProps, "type" | "maxLength"> {
  autoComplete: "current-password" | "new-password";
}

/** A password box with a show/hide toggle. Whatever the person types is only ever sent to the account API. */
export function PasswordField({ label, value, onChange, autoComplete, hint, required = true, disabled }: PasswordFieldProps) {
  const id = useId();
  const [shown, setShown] = useState(false);
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-xs font-semibold">
        {label}
      </label>
      <div className="relative">
        <input
          id={id}
          type={shown ? "text" : "password"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          required={required}
          disabled={disabled}
          aria-describedby={hint ? `${id}-hint` : undefined}
          className={cn(INPUT, "pr-11", FOCUS_RING)}
        />
        <button
          type="button"
          onClick={() => setShown((s) => !s)}
          aria-label={shown ? "Hide password" : "Show password"}
          aria-pressed={shown}
          className={cn("absolute right-1.5 top-1/2 flex h-8 w-8 -translate-y-1/2 items-center justify-center rounded-lg text-muted hover:text-foreground", FOCUS_RING)}
        >
          {shown ? <EyeOff className="h-4 w-4" aria-hidden="true" /> : <Eye className="h-4 w-4" aria-hidden="true" />}
        </button>
      </div>
      {hint && (
        <p id={`${id}-hint`} className="text-[11px] text-muted">
          {hint}
        </p>
      )}
    </div>
  );
}

/** An error (announced at once) or a quiet confirmation. Renders nothing without a message. */
export function FormMessage({ tone, children }: { tone: "error" | "ok"; children?: ReactNode }) {
  if (!children) return null;
  return (
    <p
      role={tone === "error" ? "alert" : "status"}
      className={cn(
        "rounded-xl border px-3 py-2 text-xs",
        tone === "error" ? "border-red-600/30 bg-red-600/10 text-foreground/90" : "border-emerald-500/30 bg-emerald-500/10 text-foreground/90",
      )}
    >
      {children}
    </p>
  );
}

interface SubmitButtonProps {
  busy: boolean;
  children: ReactNode;
  variant?: "primary" | "danger";
  disabled?: boolean;
}

export function SubmitButton({ busy, children, variant = "primary", disabled }: SubmitButtonProps) {
  return (
    <button
      type="submit"
      disabled={busy || disabled}
      className={cn(
        "flex items-center justify-center gap-2 rounded-xl px-4 py-2.5 text-sm font-semibold text-white shadow-sm transition disabled:opacity-60",
        variant === "danger" ? "bg-red-600 hover:bg-red-500" : "bg-orange-600 hover:bg-orange-500",
        FOCUS_RING,
      )}
    >
      {busy && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
      {children}
    </button>
  );
}

/** A form that blocks double submission and keeps the browser's own validation messages. */
export function Form({ onSubmit, children, label }: { onSubmit: () => void | Promise<void>; children: ReactNode; label: string }) {
  function handle(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    void onSubmit();
  }
  return (
    <form onSubmit={handle} aria-label={label} className="flex flex-col gap-4">
      {children}
    </form>
  );
}

/** A centred card for the sign-in and registration pages. */
export function AuthCard({ title, subtitle, children, footer }: { title: string; subtitle: string; children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="mx-auto flex w-full max-w-md flex-col gap-5 py-4 sm:py-10">
      <div className="text-center">
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">{title}</h1>
        <p className="mt-1 text-sm text-muted">{subtitle}</p>
      </div>
      <div className="glass-card rounded-2xl p-5 sm:p-6">{children}</div>
      {footer && <p className="text-center text-sm text-muted">{footer}</p>}
    </div>
  );
}

/** Shown where accounts are not offered because this deployment has no backend. */
export function AccountsUnavailable() {
  return (
    <div role="status" className="glass-card mx-auto flex max-w-md flex-col items-center gap-3 rounded-2xl p-8 text-center">
      <span className="flex h-11 w-11 items-center justify-center rounded-full bg-surface text-muted">
        <Laptop className="h-5 w-5" aria-hidden="true" />
      </span>
      <h1 className="text-base font-bold">Accounts are not available here</h1>
      <p className="text-sm text-muted">
        Accounts, saved cities and in-app alerts come from the Heatwave Monitor backend, which is not connected to this deployment. The dashboard
        still works without them.
      </p>
      <Link href="/" className={cn("rounded-full bg-orange-600 px-3.5 py-1.5 text-xs font-semibold text-white transition hover:bg-orange-500", FOCUS_RING)}>
        Back to the dashboard
      </Link>
    </div>
  );
}
