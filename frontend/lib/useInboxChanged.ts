"use client";

import { useEffect } from "react";
import { INBOX_CHANGED } from "./auth";

/** Calls `onChange` whenever something in this tab changes the inbox (marking read), so every view of it stays in step. */
export function useInboxChanged(onChange: () => void) {
  useEffect(() => {
    window.addEventListener(INBOX_CHANGED, onChange);
    return () => window.removeEventListener(INBOX_CHANGED, onChange);
  }, [onChange]);
}
