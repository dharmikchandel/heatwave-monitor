"use client";

import { AnimatePresence, motion } from "framer-motion";
import { Loader2, LocateFixed, MapPin, Search, Thermometer } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { searchLocations } from "@/lib/api";
import type { GeoLocation } from "@/lib/types";
import { cn, FOCUS_RING, formatClock } from "@/lib/utils";
import ThemeToggle from "./ThemeToggle";

interface HeaderProps {
  currentLocation: GeoLocation | null;
  onLocationSelect: (location: GeoLocation) => void;
  onUseMyLocation: () => void;
  isLocating: boolean;
  unit: "C" | "F";
  onUnitChange: (unit: "C" | "F") => void;
}

export default function Header({
  currentLocation,
  onLocationSelect,
  onUseMyLocation,
  isLocating,
  unit,
  onUnitChange,
}: HeaderProps) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<GeoLocation[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const [isOpen, setIsOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [now, setNow] = useState<Date | null>(() => (typeof window === "undefined" ? null : new Date()));
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const isDropdownVisible = isOpen && query.trim().length >= 2 && results.length > 0;

  useEffect(() => {
    const interval = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(interval);
  }, []);

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  useEffect(() => {
    const trimmed = query.trim();
    if (trimmed.length < 2) {
      return;
    }

    const controller = new AbortController();
    const timeout = setTimeout(async () => {
      setIsSearching(true);
      try {
        const locations = await searchLocations(trimmed, controller.signal);
        setResults(locations);
        setActiveIndex(-1);
      } catch {
        // Aborted or network error — leave results as-is; the user can retry.
      } finally {
        setIsSearching(false);
      }
    }, 300);

    return () => {
      clearTimeout(timeout);
      controller.abort();
    };
  }, [query]);

  function handleSelect(location: GeoLocation) {
    onLocationSelect(location);
    setQuery("");
    setResults([]);
    setIsOpen(false);
    setActiveIndex(-1);
  }

  function handleInputKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (!isDropdownVisible) return;

    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIndex((prev) => (prev + 1) % results.length);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIndex((prev) => (prev <= 0 ? results.length - 1 : prev - 1));
    } else if (e.key === "Enter") {
      if (activeIndex >= 0 && activeIndex < results.length) {
        e.preventDefault();
        handleSelect(results[activeIndex]);
      }
    } else if (e.key === "Escape") {
      setIsOpen(false);
      setActiveIndex(-1);
    }
  }

  return (
    <header className="sticky top-0 z-40 border-b border-surface-border bg-background/80 backdrop-blur-md">
      <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-3 px-4 py-3 sm:px-6 lg:px-8">
        <div className="flex items-center gap-2.5">
          <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-gradient-to-br from-orange-500 to-red-600 shadow-lg shadow-orange-600/20">
            <Thermometer className="h-5 w-5 text-white" strokeWidth={2.5} />
          </div>
          <div className="leading-tight">
            <h1 className="text-base font-bold tracking-tight sm:text-lg">Heatwave Monitor</h1>
            <p className="text-[11px] font-medium text-muted">Climate Intelligence Dashboard</p>
          </div>
        </div>

        <div ref={containerRef} className="relative ml-0 flex-1 sm:ml-4 sm:min-w-[240px] sm:max-w-md">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted" />
            <input
              ref={inputRef}
              type="text"
              role="combobox"
              aria-label="Search for a city"
              aria-expanded={isDropdownVisible}
              aria-controls="location-search-listbox"
              aria-autocomplete="list"
              aria-activedescendant={activeIndex >= 0 ? `location-option-${results[activeIndex]?.id}` : undefined}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setIsOpen(true);
              }}
              onFocus={() => setIsOpen(true)}
              onKeyDown={handleInputKeyDown}
              placeholder="Search a city..."
              className={cn(
                "w-full rounded-full border border-surface-border bg-surface/60 py-2 pl-9 pr-9 text-sm transition placeholder:text-muted",
                FOCUS_RING,
              )}
            />
            {isSearching && (
              <Loader2 className="absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 animate-spin text-muted" aria-hidden="true" />
            )}
          </div>

          <AnimatePresence>
            {isDropdownVisible && (
              <motion.ul
                id="location-search-listbox"
                role="listbox"
                aria-label="City search results"
                initial={{ opacity: 0, y: -6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -6 }}
                transition={{ duration: 0.15 }}
                className="glass-card absolute top-full z-50 mt-2 w-full overflow-hidden rounded-xl shadow-xl"
              >
                {results.map((result, index) => (
                  <li key={result.id} role="presentation">
                    <button
                      id={`location-option-${result.id}`}
                      role="option"
                      aria-selected={index === activeIndex}
                      type="button"
                      tabIndex={-1}
                      onClick={() => handleSelect(result)}
                      onMouseEnter={() => setActiveIndex(index)}
                      className={cn(
                        "flex w-full items-center gap-2.5 px-4 py-2.5 text-left text-sm transition",
                        index === activeIndex ? "bg-orange-500/10" : "hover:bg-orange-500/10",
                      )}
                    >
                      <MapPin className="h-3.5 w-3.5 shrink-0 text-orange-500" aria-hidden="true" />
                      <span className="truncate">
                        <span className="font-medium">{result.name}</span>
                        <span className="text-muted">
                          {result.admin1 ? `, ${result.admin1}` : ""}, {result.country}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </motion.ul>
            )}
          </AnimatePresence>
        </div>

        <button
          type="button"
          onClick={onUseMyLocation}
          disabled={isLocating}
          aria-label="Use my current location"
          className={cn(
            "flex h-9 w-9 items-center justify-center rounded-full border border-surface-border bg-surface/60 text-foreground transition hover:bg-surface disabled:opacity-50",
            FOCUS_RING,
          )}
        >
          {isLocating ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <LocateFixed className="h-4 w-4" aria-hidden="true" />}
        </button>

        <div className="flex items-center rounded-full border border-surface-border bg-surface/60 p-0.5 text-xs font-semibold" role="group" aria-label="Temperature unit">
          {(["C", "F"] as const).map((u) => (
            <button
              key={u}
              type="button"
              onClick={() => onUnitChange(u)}
              aria-pressed={unit === u}
              className={cn(
                "rounded-full px-2.5 py-1.5 transition",
                unit === u ? "bg-orange-600 text-white shadow-sm" : "text-muted hover:text-foreground",
                FOCUS_RING,
              )}
            >
              °{u}
            </button>
          ))}
        </div>

        <ThemeToggle />

        <div className="hidden flex-col items-end leading-tight lg:flex">
          <span className="flex items-center gap-1.5 text-xs font-medium">
            <MapPin className="h-3 w-3 text-orange-500" />
            {currentLocation ? `${currentLocation.name}${currentLocation.country ? `, ${currentLocation.country}` : ""}` : "Locating..."}
          </span>
          <span className="font-mono text-[11px] text-muted" suppressHydrationWarning>
            {now ? formatClock(now, currentLocation?.timezone) : " "}
          </span>
        </div>
      </div>
    </header>
  );
}
