"use client";

import { Bell, Inbox, LogIn, LogOut, Shield, UserRound } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { asRead, fetchInbox } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { useInboxChanged } from "@/lib/useInboxChanged";
import { usePolling } from "@/lib/usePolling";
import { cn, FOCUS_RING } from "@/lib/utils";

/** The header's account area: a sign-in link, or the inbox bell and the account menu. Absent where there are no accounts. */
export default function UserMenu() {
  const { status } = useAuth();
  const pathname = usePathname();

  if (status === "unavailable") return null;
  if (status === "loading") return <div className="h-9 w-20 shimmer rounded-full" aria-hidden="true" />;
  if (status === "anonymous") {
    const here = pathname === "/login" || pathname === "/register" ? "" : `?next=${encodeURIComponent(pathname)}`;
    return (
      <Link
        href={`/login${here}`}
        className={cn("flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3.5 py-2 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}
      >
        <LogIn className="h-3.5 w-3.5" aria-hidden="true" /> Sign in
      </Link>
    );
  }
  return <SignedInMenu />;
}

const loadUnread = (signal: AbortSignal) => fetchInbox(1, { signal }).then(asRead);

function SignedInMenu() {
  const { user, signOut, sessionEnded } = useAuth();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  const inbox = usePolling(loadUnread, 60_000);
  const unread = inbox.data?.unread ?? 0;

  // A page that marks things read tells the bell straight away instead of waiting for the next poll.
  useInboxChanged(inbox.refresh);

  // The server ended the session (an administrator disabled the account, say): show it.
  useEffect(() => {
    if (inbox.failure === "unauthorized") sessionEnded();
  }, [inbox.failure, sessionEnded]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const leave = useCallback(async () => {
    setOpen(false);
    await signOut();
    router.push("/");
  }, [signOut, router]);

  if (!user) return null;
  const initial = (user.displayName || user.email).trim().charAt(0).toUpperCase();

  const item = cn("flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-sm transition hover:bg-orange-500/10", FOCUS_RING);

  return (
    <div className="flex items-center gap-2">
      <Link
        href="/inbox"
        aria-label={unread > 0 ? `Inbox, ${unread} unread` : "Inbox"}
        className={cn("relative flex h-9 w-9 items-center justify-center rounded-full border border-surface-border bg-surface/60 transition hover:bg-surface", FOCUS_RING)}
      >
        <Bell className="h-4 w-4" aria-hidden="true" />
        {unread > 0 && (
          <span
            aria-hidden="true"
            className="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-red-600 px-1 text-[10px] font-bold text-white"
          >
            {unread > 9 ? "9+" : unread}
          </span>
        )}
      </Link>

      <div ref={rootRef} className="relative">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label={`Account menu for ${user.displayName || user.email}`}
          className={cn("flex h-9 w-9 items-center justify-center rounded-full bg-orange-600 text-sm font-bold text-white shadow-sm transition hover:bg-orange-500", FOCUS_RING)}
        >
          {initial}
        </button>
        {open && (
          <div role="menu" aria-label="Account" className="glass-card absolute right-0 top-full z-50 mt-2 w-60 rounded-xl p-1.5 shadow-xl">
            <div className="border-b border-surface-border px-3 pb-2 pt-1.5">
              <p className="truncate text-sm font-semibold">{user.displayName || "Your account"}</p>
              <p className="truncate text-xs text-muted">{user.email}</p>
            </div>
            <div className="pt-1.5">
              <Link role="menuitem" href="/account" onClick={() => setOpen(false)} className={item}>
                <UserRound className="h-4 w-4" aria-hidden="true" /> Account & cities
              </Link>
              <Link role="menuitem" href="/inbox" onClick={() => setOpen(false)} className={item}>
                <Inbox className="h-4 w-4" aria-hidden="true" /> Inbox{unread > 0 ? ` (${unread})` : ""}
              </Link>
              {user.role === "admin" && (
                <Link role="menuitem" href="/admin" onClick={() => setOpen(false)} className={item}>
                  <Shield className="h-4 w-4" aria-hidden="true" /> Administration
                </Link>
              )}
              <button role="menuitem" type="button" onClick={leave} className={item}>
                <LogOut className="h-4 w-4" aria-hidden="true" /> Sign out
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
