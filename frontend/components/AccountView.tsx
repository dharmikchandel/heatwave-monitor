"use client";

import { Bell, MapPin, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { changePassword, deleteAccount, describeError, passwordProblem, type MySubscription, type WatchedCity } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { useClimate } from "@/lib/ClimateContext";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import { useMyCities } from "@/lib/useMyCities";
import type { HeatRiskLevel } from "@/lib/types";
import { cn, FOCUS_RING } from "@/lib/utils";
import { Form, FormMessage, PasswordField, SubmitButton } from "./AccountForm";

const SECTION = "glass-card flex flex-col gap-4 rounded-2xl p-5";
const SECTION_TITLE = "text-sm font-bold uppercase tracking-wide text-muted";

export default function AccountView() {
  const { user, signOut } = useAuth();
  const router = useRouter();
  if (!user) return null;

  return (
    <>
      <div>
        <h1 className="text-xl font-bold tracking-tight sm:text-2xl">Your account</h1>
        <p className="mt-1 text-sm text-muted">Your saved cities, the alerts you want, and your sign-in details.</p>
      </div>

      <section aria-labelledby="profile-title" className={SECTION}>
        <h2 id="profile-title" className={SECTION_TITLE}>
          Profile
        </h2>
        <dl className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-xs font-semibold text-muted">Name</dt>
            <dd>{user.displayName || "—"}</dd>
          </div>
          <div>
            <dt className="text-xs font-semibold text-muted">Email</dt>
            <dd className="break-all">{user.email}</dd>
          </div>
          <div>
            <dt className="text-xs font-semibold text-muted">Role</dt>
            <dd className="capitalize">{user.role === "admin" ? "Administrator" : "Member"}</dd>
          </div>
          <div>
            <dt className="text-xs font-semibold text-muted">Member since</dt>
            <dd>{new Date(user.createdAt).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" })}</dd>
          </div>
        </dl>
        <div>
          <button
            type="button"
            onClick={async () => {
              await signOut();
              router.push("/");
            }}
            className={cn("rounded-full border border-surface-border bg-surface/60 px-3.5 py-1.5 text-xs font-semibold transition hover:bg-surface", FOCUS_RING)}
          >
            Sign out
          </button>
        </div>
      </section>

      <MyCitiesSection />
      <PasswordSection />
      <DeleteSection />
    </>
  );
}

const ALERT_CHOICES: { value: "off" | HeatRiskLevel; label: string }[] = [
  { value: "off", label: "No alerts" },
  { value: "danger", label: `${RISK_LEVEL_LABEL.danger} or worse` },
  { value: "extreme-danger", label: `${RISK_LEVEL_LABEL["extreme-danger"]} only` },
];

function MyCitiesSection() {
  const mine = useMyCities();
  const { selectLocation } = useClimate();
  const router = useRouter();

  function open(city: WatchedCity) {
    selectLocation({ id: city.locationId, name: city.name, country: city.country, admin1: city.admin1, latitude: city.latitude, longitude: city.longitude, timezone: city.timezone });
    router.push("/");
  }

  const savedIds = new Set((mine.cities ?? []).map((c) => c.locationId));
  const orphans: MySubscription[] = (mine.follows ?? []).filter((f) => f.locationId !== null && !savedIds.has(f.locationId));

  return (
    <section aria-labelledby="cities-title" className={SECTION}>
      <h2 id="cities-title" className={SECTION_TITLE}>
        My cities
      </h2>
      <p className="text-sm text-muted">
        Alerts arrive in your inbox when a heatwave is under way or forecast for a city. Save a city from the dashboard, then choose how worried you want to be.
      </p>

      {mine.error && <FormMessage tone="error">{describeError(mine.error)}</FormMessage>}

      {mine.cities === null && !mine.error && <div className="shimmer h-16 rounded-xl" aria-hidden="true" />}

      {mine.cities?.length === 0 && (
        <p role="status" className="rounded-xl border border-dashed border-surface-border p-4 text-center text-sm text-muted">
          No saved cities yet. Open the dashboard, pick a city and press <strong>Save city</strong>.
        </p>
      )}

      {mine.cities && mine.cities.length > 0 && (
        <ul className="flex flex-col divide-y divide-surface-border" aria-label="Saved cities">
          {mine.cities.map((city) => {
            const sub = mine.followFor(city.locationId);
            const choice = sub ? sub.minLevel : "off";
            return (
              <li key={city.locationId} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <button type="button" onClick={() => open(city)} className={cn("flex min-w-0 items-center gap-2 rounded text-left hover:underline", FOCUS_RING)}>
                  <MapPin className="h-4 w-4 shrink-0 text-orange-500" aria-hidden="true" />
                  <span className="truncate text-sm font-semibold">
                    {city.name}
                    <span className="font-normal text-muted">
                      {city.admin1 ? `, ${city.admin1}` : ""}
                      {city.country ? `, ${city.country}` : ""}
                    </span>
                  </span>
                </button>
                <div className="flex items-center gap-2">
                  <label className="flex items-center gap-1.5 text-xs font-semibold">
                    <Bell className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
                    <span className="sr-only">Alerts for {city.name}</span>
                    <select
                      value={choice}
                      disabled={mine.busy}
                      onChange={(e) => void mine.setAlerts(city.locationId, e.target.value === "off" ? null : (e.target.value as HeatRiskLevel))}
                      className={cn("rounded-lg border border-surface-border bg-surface/60 px-2 py-1.5 text-xs", FOCUS_RING)}
                    >
                      {ALERT_CHOICES.map((c) => (
                        <option key={c.value} value={c.value}>
                          {c.label}
                        </option>
                      ))}
                    </select>
                  </label>
                  <button
                    type="button"
                    disabled={mine.busy}
                    onClick={() => void mine.remove(city.locationId)}
                    aria-label={`Remove ${city.name}`}
                    className={cn("flex h-8 w-8 items-center justify-center rounded-lg text-muted transition hover:bg-red-600/10 hover:text-red-600 disabled:opacity-60", FOCUS_RING)}
                  >
                    <Trash2 className="h-4 w-4" aria-hidden="true" />
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {orphans.length > 0 && (
        <div className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold text-muted">Alerts for cities that are not on your list</h3>
          <ul className="flex flex-col gap-1">
            {orphans.map((sub) => (
              <li key={sub.id} className="flex items-center justify-between gap-3 text-sm">
                <span>Location #{sub.locationId}</span>
                <button
                  type="button"
                  disabled={mine.busy}
                  onClick={() => void mine.setAlerts(sub.locationId as number, null)}
                  className={cn("rounded text-xs font-semibold text-red-700 hover:underline dark:text-red-400", FOCUS_RING)}
                >
                  Stop these alerts
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function PasswordSection() {
  const { sessionEnded } = useAuth();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  async function submit() {
    setDone(false);
    const problem = passwordProblem(next);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError(null);
    const res = await changePassword({ current, new: next });
    setBusy(false);
    if (!res.ok) {
      if (res.error.status === 401 && res.error.code === "unauthorized") sessionEnded();
      setError(describeError(res.error));
      return;
    }
    setCurrent("");
    setNext("");
    setDone(true);
  }

  return (
    <section aria-labelledby="password-title" className={SECTION}>
      <h2 id="password-title" className={SECTION_TITLE}>
        Change password
      </h2>
      <p className="text-sm text-muted">Changing it signs you out everywhere else.</p>
      <Form label="Change password" onSubmit={submit}>
        <div className="grid gap-4 sm:grid-cols-2">
          <PasswordField label="Current password" value={current} onChange={setCurrent} autoComplete="current-password" disabled={busy} />
          <PasswordField label="New password" value={next} onChange={setNext} autoComplete="new-password" disabled={busy} />
        </div>
        <FormMessage tone="error">{error}</FormMessage>
        <FormMessage tone="ok">{done ? "Password changed. Your other sessions have been signed out." : null}</FormMessage>
        <div>
          <SubmitButton busy={busy}>Change password</SubmitButton>
        </div>
      </Form>
    </section>
  );
}

function DeleteSection() {
  const { sessionEnded } = useAuth();
  const router = useRouter();
  const [confirming, setConfirming] = useState(false);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    setBusy(true);
    setError(null);
    const res = await deleteAccount(password);
    if (!res.ok) {
      setBusy(false);
      setError(describeError(res.error));
      return;
    }
    sessionEnded();
    router.push("/");
  }

  return (
    <section aria-labelledby="delete-title" className={cn(SECTION, "border-red-600/30")}>
      <h2 id="delete-title" className={cn(SECTION_TITLE, "text-red-700 dark:text-red-400")}>
        Delete account
      </h2>
      <p className="text-sm text-muted">This permanently removes your account, saved cities, alert settings and inbox. It cannot be undone.</p>
      {!confirming ? (
        <div>
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className={cn("rounded-full border border-red-600/40 px-3.5 py-1.5 text-xs font-semibold text-red-700 transition hover:bg-red-600/10 dark:text-red-400", FOCUS_RING)}
          >
            Delete my account…
          </button>
        </div>
      ) : (
        <Form label="Delete account" onSubmit={submit}>
          <PasswordField label="Enter your password to confirm" value={password} onChange={setPassword} autoComplete="current-password" disabled={busy} />
          <FormMessage tone="error">{error}</FormMessage>
          <div className="flex gap-2">
            <SubmitButton busy={busy} variant="danger">
              Permanently delete
            </SubmitButton>
            <button
              type="button"
              onClick={() => {
                setConfirming(false);
                setPassword("");
                setError(null);
              }}
              className={cn("rounded-xl border border-surface-border px-4 py-2.5 text-sm font-semibold transition hover:bg-surface", FOCUS_RING)}
            >
              Cancel
            </button>
          </div>
        </Form>
      )}
    </section>
  );
}
