"use client";

import { Moon, Sun } from "lucide-react";
import { useLayoutEffect, useState } from "react";
import { cn, FOCUS_RING } from "@/lib/utils";

const STORAGE_KEY = "heatwave-theme";

function getStoredTheme(): "light" | "dark" {
  if (typeof window === "undefined") return "dark";
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // localStorage unavailable — fall through to media-query preference.
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export default function ThemeToggle() {
  const [theme, setTheme] = useState<"light" | "dark">(getStoredTheme);

  // Keeps <html data-theme> in sync with state, including on React's dev-mode
  // strict remount, which clears the attribute the inline bootstrap script set.
  useLayoutEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
  }, [theme]);

  function toggle() {
    const next = theme === "dark" ? "light" : "dark";
    setTheme(next);
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // best-effort persistence only
    }
  }

  return (
    <button
      type="button"
      onClick={toggle}
      aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}
      aria-pressed={theme === "dark"}
      className={cn(
        "flex h-9 w-9 items-center justify-center rounded-full border border-surface-border bg-surface/60 text-foreground transition hover:bg-surface",
        FOCUS_RING,
      )}
    >
      {theme === "dark" ? <Sun className="h-4 w-4" aria-hidden="true" /> : <Moon className="h-4 w-4" aria-hidden="true" />}
    </button>
  );
}
