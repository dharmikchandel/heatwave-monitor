"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { accountsUnavailable, fetchSession, login, logout, register, type AccountUser, type ApiError } from "./auth";

/**
 * "loading": asking who this is. "signed-in" / "anonymous": the usual two. "unavailable":
 * this deployment has no backend, so there are no accounts and no account UI.
 */
export type AuthStatus = "loading" | "signed-in" | "anonymous" | "unavailable";

export type AuthResult = { ok: true; user: AccountUser } | { ok: false; error: ApiError };

export interface AuthContextValue {
  status: AuthStatus;
  user: AccountUser | null;
  /** Counts sign-ins, sign-ups and sign-outs, so what depends on who is looking can reload. */
  version: number;
  signIn: (email: string, password: string) => Promise<AuthResult>;
  signUp: (input: { email: string; password: string; displayName: string }) => Promise<AuthResult>;
  signOut: () => Promise<void>;
  /** Called when the server says the session is over (a 401 from a page), without a round trip. */
  sessionEnded: () => void;
}

export const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus>("loading");
  const [user, setUser] = useState<AccountUser | null>(null);
  const [version, setVersion] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    void fetchSession({ signal: controller.signal }).then((res) => {
      if (controller.signal.aborted) return;
      if (res.ok) {
        setUser(res.data.user);
        setStatus(res.data.user ? "signed-in" : "anonymous");
      } else {
        // No backend means no accounts. A backend that is merely down still offers sign-in.
        setStatus(accountsUnavailable(res.error) ? "unavailable" : "anonymous");
      }
    });
    return () => controller.abort();
  }, []);

  const begin = useCallback((u: AccountUser) => {
    setUser(u);
    setStatus("signed-in");
    setVersion((v) => v + 1);
  }, []);

  const signIn = useCallback(
    async (email: string, password: string): Promise<AuthResult> => {
      const res = await login({ email, password });
      if (!res.ok) return res;
      begin(res.data.user);
      return { ok: true, user: res.data.user };
    },
    [begin],
  );

  const signUp = useCallback(
    async (input: { email: string; password: string; displayName: string }): Promise<AuthResult> => {
      const res = await register(input);
      if (!res.ok) return res;
      begin(res.data.user);
      return { ok: true, user: res.data.user };
    },
    [begin],
  );

  const sessionEnded = useCallback(() => {
    setUser(null);
    setStatus((s) => (s === "unavailable" ? s : "anonymous"));
    setVersion((v) => v + 1);
  }, []);

  const signOut = useCallback(async () => {
    await logout(); // best effort: the local state is cleared either way
    sessionEnded();
  }, [sessionEnded]);

  const value = useMemo<AuthContextValue>(
    () => ({ status, user, version, signIn, signUp, signOut, sessionEnded }),
    [status, user, version, signIn, signUp, signOut, sessionEnded],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within an AuthProvider");
  return ctx;
}
