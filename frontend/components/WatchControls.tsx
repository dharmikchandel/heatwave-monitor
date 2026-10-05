"use client";

import { Bell, BellOff, Loader2, Star } from "lucide-react";
import Link from "next/link";
import { describeError } from "@/lib/auth";
import { useAuth } from "@/lib/AuthContext";
import { useClimate } from "@/lib/ClimateContext";
import { useMyCities } from "@/lib/useMyCities";
import { cn, FOCUS_RING } from "@/lib/utils";

const BUTTON = "flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-xs font-semibold transition disabled:opacity-60";

/**
 * Save the city on screen and turn alerts for it on or off. It needs the backend's id for the
 * city, so it is absent in local mode; anonymous visitors get an invitation to sign in instead.
 */
export default function WatchControls() {
  const { status } = useAuth();
  const { location, insight } = useClimate();
  const mine = useMyCities();

  const locationId = insight?.locationId;
  if (!location || !locationId || status === "loading" || status === "unavailable") return null;

  if (status === "anonymous") {
    return (
      <Link href="/login?next=%2F" className={cn("rounded text-xs font-semibold text-orange-600 hover:underline dark:text-orange-400", FOCUS_RING)}>
        Sign in to save this city and get alerts
      </Link>
    );
  }

  const saved = mine.isSaved(locationId);
  const alertsOn = mine.followFor(locationId) !== undefined;
  const ready = mine.cities !== null && mine.follows !== null;

  const city = {
    locationId,
    name: location.name,
    country: location.country,
    admin1: location.admin1,
    latitude: location.latitude,
    longitude: location.longitude,
    timezone: location.timezone,
  };

  async function toggleSaved() {
    if (saved) await mine.remove(locationId!);
    else await mine.save(city);
  }

  async function toggleAlerts() {
    if (alertsOn) {
      await mine.setAlerts(locationId!, null);
      return;
    }
    // Alerts are for cities on your list, so turning them on saves the city too.
    if (!saved && !(await mine.save(city))) return;
    await mine.setAlerts(locationId!, "danger");
  }

  return (
    <WatchButtons
      saved={saved}
      alertsOn={alertsOn}
      ready={ready}
      busy={mine.busy}
      error={mine.error ? describeError(mine.error) : null}
      onToggleSaved={toggleSaved}
      onToggleAlerts={toggleAlerts}
    />
  );
}

interface WatchButtonsProps {
  saved: boolean;
  alertsOn: boolean;
  /** False until the person's lists have loaded: pressing before that could act on stale state. */
  ready: boolean;
  busy: boolean;
  error: string | null;
  onToggleSaved: () => void;
  onToggleAlerts: () => void;
}

export function WatchButtons({ saved, alertsOn, ready, busy, error, onToggleSaved, onToggleAlerts }: WatchButtonsProps) {
  const idle = "border-surface-border bg-surface/60 hover:bg-surface";
  const active = "border-orange-600 bg-orange-600 text-white hover:bg-orange-500";

  return (
    <div className="flex flex-col items-end gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" onClick={onToggleSaved} disabled={!ready || busy} aria-pressed={saved} className={cn(BUTTON, saved ? active : idle, FOCUS_RING)}>
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : <Star className={cn("h-3.5 w-3.5", saved && "fill-current")} aria-hidden="true" />}
          {saved ? "Saved" : "Save city"}
        </button>
        <button
          type="button"
          onClick={onToggleAlerts}
          disabled={!ready || busy}
          aria-pressed={alertsOn}
          title={alertsOn ? "You get an inbox alert when Danger or worse is under way or forecast here" : "Get an inbox alert when Danger or worse is under way or forecast here"}
          className={cn(BUTTON, alertsOn ? active : idle, FOCUS_RING)}
        >
          {alertsOn ? <Bell className="h-3.5 w-3.5" aria-hidden="true" /> : <BellOff className="h-3.5 w-3.5" aria-hidden="true" />}
          {alertsOn ? "Alerts on" : "Alerts off"}
        </button>
      </div>
      {error && (
        <p role="alert" className="text-[11px] text-red-700 dark:text-red-400">
          {error}
        </p>
      )}
    </div>
  );
}
