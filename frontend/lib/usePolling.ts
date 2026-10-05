"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ApiRead, BackendFailure } from "./backend";

export interface Polled<T> {
  /** The latest successful answer; kept while later polls fail, so the page can show it as stale. */
  data: T | null;
  /** Why the latest poll failed, or null if it succeeded. */
  failure: BackendFailure | null;
  /** True until the first answer (or failure) arrives. */
  isLoading: boolean;
  updatedAt: Date | null;
  /** Poll right now (and restart the timer). */
  refresh: () => void;
}

/**
 * Polls `load` every `intervalMs` while the tab is visible, and immediately when it
 * becomes visible again. To restart from scratch (a different filter), remount the
 * component with a new `key`.
 */
export function usePolling<T>(load: (signal: AbortSignal) => Promise<ApiRead<T>>, intervalMs: number): Polled<T> {
  const [data, setData] = useState<T | null>(null);
  const [failure, setFailure] = useState<BackendFailure | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  const loadRef = useRef(load);
  const runNow = useRef<() => void>(() => {});
  useEffect(() => {
    loadRef.current = load;
  }, [load]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    async function poll() {
      clearTimeout(timer);
      try {
        const result = await loadRef.current(controller.signal);
        if (stopped) return;
        if (result.ok) {
          setData(result.data);
          setFailure(null);
          setUpdatedAt(new Date());
        } else {
          setFailure(result.reason);
        }
        setIsLoading(false);
      } catch {
        return; // aborted: the component is going away
      }
      schedule();
    }

    function schedule() {
      if (stopped) return;
      timer = setTimeout(() => {
        if (typeof document !== "undefined" && document.hidden) schedule(); // idle while the tab is hidden
        else void poll();
      }, intervalMs);
    }

    const onVisible = () => {
      if (!document.hidden && !stopped) void poll();
    };

    runNow.current = () => void poll();
    document.addEventListener("visibilitychange", onVisible);
    void poll();

    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [intervalMs]);

  const refresh = useCallback(() => runNow.current(), []);
  return { data, failure, isLoading, updatedAt, refresh };
}

/** The current time, refreshed periodically, for relative labels ("5m ago"). Null until mounted. */
export function useNow(intervalMs = 30_000): Date | null {
  const [now, setNow] = useState<Date | null>(null);
  useEffect(() => {
    const tick = () => setNow(new Date());
    const first = setTimeout(tick, 0);
    const id = setInterval(tick, intervalMs);
    return () => {
      clearTimeout(first);
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}
