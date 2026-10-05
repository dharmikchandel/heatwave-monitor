"use client";

import { Activity, BellRing, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useCallback, useState } from "react";
import BackendNotice from "@/components/BackendNotice";
import { asRead, describeError, fetchUsers, setUserDisabled, type AccountUser } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { useNow, usePolling } from "@/lib/usePolling";
import { cn, FOCUS_RING, formatRelativeTime } from "@/lib/utils";

export default function AdminView() {
  const { user: me } = useAuth();
  const load = useCallback((signal: AbortSignal) => fetchUsers({ signal }).then(asRead), []);
  const { data, failure, isLoading, refresh } = usePolling(load, 30_000);
  const now = useNow();
  const [busyId, setBusyId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function toggle(u: AccountUser) {
    setBusyId(u.id);
    setError(null);
    const res = await setUserDisabled(u.id, !u.disabled);
    setBusyId(null);
    if (!res.ok) setError(describeError(res.error));
    refresh();
  }

  if (!data && isLoading) return <div className="glass-card shimmer h-40 rounded-2xl" aria-hidden="true" />;
  if (!data) return <BackendNotice what="Accounts" reason={failure ?? "unavailable"} onRetry={refresh} />;

  const users = data.users;
  const admins = users.filter((u) => u.role === "admin").length;
  const disabled = users.filter((u) => u.disabled).length;

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Administration</h1>
          <p className="mt-1 text-sm text-muted">
            {users.length} account{users.length === 1 ? "" : "s"}, {admins} administrator{admins === 1 ? "" : "s"}, {disabled} disabled. Disabling an account signs
            it out at once and stops it signing in.
          </p>
        </div>
        <div className="flex gap-2">
          <Link href="/status" className={cn("flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}>
            <Activity className="h-3.5 w-3.5" aria-hidden="true" /> System status
          </Link>
          <Link href="/alerts" className={cn("flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}>
            <BellRing className="h-3.5 w-3.5" aria-hidden="true" /> Alerts
          </Link>
          <button type="button" onClick={refresh} className={cn("flex items-center gap-1.5 rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}>
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" /> Refresh
          </button>
        </div>
      </div>

      {error && <p role="alert" className="rounded-xl border border-red-600/30 bg-red-600/10 px-3 py-2 text-xs">{error}</p>}

      <UsersTable users={users} meId={me?.id ?? null} now={now} busyId={busyId} onToggle={(u) => void toggle(u)} />
    </>
  );
}

interface UsersTableProps {
  users: AccountUser[];
  /** The signed-in administrator, who cannot disable themselves. */
  meId: number | null;
  now: Date | null;
  busyId: number | null;
  onToggle: (user: AccountUser) => void;
}

export function UsersTable({ users, meId, now, busyId, onToggle }: UsersTableProps) {
  return (
    <div className="glass-card overflow-x-auto rounded-2xl">
      <table className="w-full min-w-[640px] text-left text-sm">
        <caption className="sr-only">Accounts</caption>
        <thead className="border-b border-surface-border text-xs uppercase tracking-wide text-muted">
          <tr>
            <th scope="col" className="px-4 py-3 font-semibold">Account</th>
            <th scope="col" className="px-4 py-3 font-semibold">Role</th>
            <th scope="col" className="px-4 py-3 font-semibold">Status</th>
            <th scope="col" className="px-4 py-3 font-semibold">Last sign-in</th>
            <th scope="col" className="px-4 py-3 text-right font-semibold">Action</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-surface-border">
          {users.map((u) => (
            <tr key={u.id}>
              <td className="px-4 py-3">
                <p className="font-semibold">{u.displayName || "—"}</p>
                <p className="break-all text-xs text-muted">{u.email}</p>
              </td>
              <td className="px-4 py-3">{u.role === "admin" ? "Administrator" : "Member"}</td>
              <td className="px-4 py-3">
                <span className={cn("inline-flex items-center gap-1.5 text-xs font-semibold", u.disabled ? "text-red-700 dark:text-red-400" : "text-emerald-700 dark:text-emerald-400")}>
                  <span className={cn("h-2 w-2 rounded-full", u.disabled ? "bg-red-600" : "bg-emerald-500")} aria-hidden="true" />
                  {u.disabled ? "Disabled" : "Active"}
                </span>
              </td>
              <td className="px-4 py-3 text-xs text-muted">{u.lastLoginAt && now ? formatRelativeTime(new Date(u.lastLoginAt), now) : "—"}</td>
              <td className="px-4 py-3 text-right">
                {u.id === meId ? (
                  <span className="text-xs text-muted">This is you</span>
                ) : (
                  <button
                    type="button"
                    onClick={() => onToggle(u)}
                    disabled={busyId === u.id}
                    aria-label={`${u.disabled ? "Enable" : "Disable"} ${u.email}`}
                    className={cn(
                      "rounded-full border px-3 py-1 text-xs font-semibold transition disabled:opacity-60",
                      u.disabled ? "border-emerald-500/40 hover:bg-emerald-500/10" : "border-red-600/40 text-red-700 hover:bg-red-600/10 dark:text-red-400",
                      FOCUS_RING,
                    )}
                  >
                    {u.disabled ? "Enable" : "Disable"}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
