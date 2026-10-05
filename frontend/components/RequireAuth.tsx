"use client";

import { ShieldAlert } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";
import { useAuth } from "@/lib/AuthContext";
import { cn, FOCUS_RING } from "@/lib/utils";
import { AccountsUnavailable } from "./AccountForm";

interface RequireAuthProps {
  /** What the page is, for the prompt: "your inbox". */
  what: string;
  /** Only administrators may see this page. */
  admin?: boolean;
  children: ReactNode;
}

/** Shows its children to a signed-in person (an administrator, if `admin`); otherwise says what is needed. */
export default function RequireAuth({ what, admin = false, children }: RequireAuthProps) {
  const { status, user } = useAuth();
  const pathname = usePathname();

  if (status === "loading") return <div className="glass-card shimmer h-40 rounded-2xl" aria-hidden="true" />;
  if (status === "unavailable") return <AccountsUnavailable />;

  if (status === "anonymous") {
    const next = encodeURIComponent(pathname);
    return (
      <div role="status" className="glass-card mx-auto flex max-w-md flex-col items-center gap-3 rounded-2xl p-8 text-center">
        <h1 className="text-base font-bold">Sign in to see {what}</h1>
        <p className="text-sm text-muted">It is tied to your account.</p>
        <div className="flex gap-2">
          <Link href={`/login?next=${next}`} className={cn("rounded-full bg-orange-600 px-3.5 py-1.5 text-xs font-semibold text-white transition hover:bg-orange-500", FOCUS_RING)}>
            Sign in
          </Link>
          <Link href={`/register?next=${next}`} className={cn("rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}>
            Create an account
          </Link>
        </div>
      </div>
    );
  }

  if (admin && user?.role !== "admin") {
    return (
      <div role="alert" className="glass-card mx-auto flex max-w-md flex-col items-center gap-3 rounded-2xl p-8 text-center">
        <span className="flex h-11 w-11 items-center justify-center rounded-full bg-amber-500/15 text-amber-700 dark:text-amber-400">
          <ShieldAlert className="h-5 w-5" aria-hidden="true" />
        </span>
        <h1 className="text-base font-bold">Administrator access required</h1>
        <p className="text-sm text-muted">Your account does not have access to {what}.</p>
      </div>
    );
  }

  return <>{children}</>;
}
