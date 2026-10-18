"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";

import { api } from "@/lib/api";
import {
  endSession,
  hasSession,
  onSessionEnded,
  type CurrentUser,
} from "@/lib/session";

/**
 * Who is signed in, for the whole console.
 *
 * The permission list comes from the API rather than being reimplemented
 * here. The matrix has one definition, in the Go backend, and a front end
 * that keeps its own copy drifts from it the first time a role changes — in
 * the direction of showing a button that then returns 403, or hiding one the
 * officer is entitled to.
 *
 * Hiding a control the caller cannot use is a courtesy, not a control. The
 * server checks every request regardless.
 */

interface SessionState {
  user: CurrentUser | null;
  loading: boolean;
  can: (permission: string) => boolean;
  refresh: () => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionContext = createContext<SessionState>({
  user: null,
  loading: true,
  can: () => false,
  refresh: async () => {},
  signOut: async () => {},
});

export function useSession(): SessionState {
  return useContext(SessionContext);
}

export function SessionProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    if (!hasSession()) {
      setUser(null);
      setLoading(false);
      return;
    }
    try {
      setUser(await api.me());
    } catch {
      // The api layer has already ended the session on a 401; anything else
      // is an outage, and either way there is no user to show.
      setUser(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let live = true;

    // Resolved as a promise even when there is no session, so that nothing
    // here sets state while the effect is running. The component renders its
    // loading state once and the answer replaces it, rather than the answer
    // arriving mid-effect and forcing a second render before the first is on
    // screen.
    const settle = hasSession()
      ? api.me().catch(() => null) // the api layer has already ended the
      : Promise.resolve(null); //     session on a 401; anything else is an
    //                                outage, and either way there is no user.

    void settle.then((who) => {
      if (!live) return;
      setUser(who);
      setLoading(false);
    });

    // The api layer ends the session from outside React when a token is
    // refused mid-request. Without this, the user keeps clicking a console
    // that has already been signed out.
    const stop = onSessionEnded(() => setUser(null));

    return () => {
      live = false;
      stop();
    };
  }, []);

  const can = useCallback(
    (permission: string) => user?.permissions.includes(permission) ?? false,
    [user],
  );

  const signOut = useCallback(async () => {
    await api.logout();
    endSession();
    setUser(null);
  }, []);

  return (
    <SessionContext.Provider value={{ user, loading, can, refresh, signOut }}>
      {children}
    </SessionContext.Provider>
  );
}
