"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchSubscriptions,
  fetchWatchlist,
  followLocation,
  removeCity,
  saveCity,
  unfollow,
  type ApiError,
  type MySubscription,
  type WatchedCity,
} from "./auth";
import { useAuth } from "./AuthContext";
import type { HeatRiskLevel } from "./types";

export interface MyCities {
  /** Null until loaded. */
  cities: WatchedCity[] | null;
  follows: MySubscription[] | null;
  error: ApiError | null;
  busy: boolean;
  isSaved: (locationId: number) => boolean;
  /** The in-app subscription for exactly this city, if any. */
  followFor: (locationId: number) => MySubscription | undefined;
  save: (city: Omit<WatchedCity, "addedAt">) => Promise<boolean>;
  remove: (locationId: number) => Promise<boolean>;
  /** Turn alerts for a city on (at a threshold) or off (null). */
  setAlerts: (locationId: number, minLevel: HeatRiskLevel | null) => Promise<boolean>;
}

/** The signed-in user's saved cities and the cities they want alerts for. Idle when nobody is signed in. */
export function useMyCities(): MyCities {
  const { status, sessionEnded } = useAuth();
  const [cities, setCities] = useState<WatchedCity[] | null>(null);
  const [follows, setFollows] = useState<MySubscription[] | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const signedIn = status === "signed-in";

  useEffect(() => {
    if (!signedIn) return;
    const controller = new AbortController();
    void Promise.all([fetchWatchlist({ signal: controller.signal }), fetchSubscriptions({ signal: controller.signal })]).then(([w, s]) => {
      if (controller.signal.aborted) return;
      if (w.ok && s.ok) {
        setCities(w.data.cities);
        setFollows(s.data.subscriptions);
        setError(null);
      } else {
        const failure = !w.ok ? w.error : !s.ok ? s.error : null;
        if (failure?.status === 401) sessionEnded();
        setError(failure);
      }
    });
    return () => controller.abort();
  }, [signedIn, sessionEnded]);

  // Runs a change, then refreshes what it touched. Returns whether it worked.
  const change = useCallback(
    async (run: () => Promise<{ ok: boolean; error?: ApiError }>, refresh: "cities" | "follows" | "both"): Promise<boolean> => {
      setBusy(true);
      try {
        const res = await run();
        if (!res.ok) {
          if (res.error?.status === 401) sessionEnded();
          setError(res.error ?? null);
          return false;
        }
        setError(null);
        if (refresh !== "follows") {
          const w = await fetchWatchlist();
          if (w.ok) setCities(w.data.cities);
        }
        if (refresh !== "cities") {
          const s = await fetchSubscriptions();
          if (s.ok) setFollows(s.data.subscriptions);
        }
        return true;
      } finally {
        setBusy(false);
      }
    },
    [sessionEnded],
  );

  const save = useCallback((city: Omit<WatchedCity, "addedAt">) => change(() => saveCity(city), "cities"), [change]);

  const remove = useCallback(
    async (locationId: number) => {
      const sub = follows?.find((f) => f.locationId === locationId);
      // Removing a city also stops its alerts, so nothing is left following a city the user no longer lists.
      if (sub) await unfollow(sub.id);
      return change(() => removeCity(locationId), "both");
    },
    [change, follows],
  );

  const setAlerts = useCallback(
    (locationId: number, minLevel: HeatRiskLevel | null) => {
      const existing = follows?.find((f) => f.locationId === locationId);
      if (minLevel === null) return existing ? change(() => unfollow(existing.id), "follows") : Promise.resolve(true);
      return change(() => followLocation(locationId, minLevel), "follows");
    },
    [change, follows],
  );

  return {
    cities,
    follows,
    error,
    busy,
    isSaved: (id) => cities?.some((c) => c.locationId === id) ?? false,
    followFor: (id) => follows?.find((f) => f.locationId === id),
    save,
    remove,
    setAlerts,
  };
}
