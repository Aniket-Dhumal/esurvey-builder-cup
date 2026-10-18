/**
 * Token storage and refresh for the browser console.
 *
 * # Where the tokens live
 *
 * In memory for the access token, in localStorage for the refresh token.
 *
 * The honest position is that no browser storage resists an XSS on the page
 * that reads it — httpOnly cookies are the only real answer, and they need the
 * API and the console on one origin, which this static export is not. Given
 * that, the split still buys something: the access token, which is the one
 * that opens every endpoint, never touches a place a stray script can read
 * synchronously and never survives a tab close. The refresh token that does
 * persist is single-use and rotates, so a stolen copy is detected the moment
 * the real client uses its own — the backend revokes the whole family.
 *
 * Move both into httpOnly cookies when the console is served from the API's
 * origin. Until then this is a deliberate second-best, not an oversight.
 */

const REFRESH_KEY = "esurvey.refresh";

export interface Grant {
  role: string;
  jurisdiction_id?: string;
  jurisdiction_code?: string;
  jurisdiction_level?: string;
  jurisdiction_name?: string;
}

export interface CurrentUser {
  user_id: string;
  kind: "officer" | "farmer";
  name: string;
  /** The officer's email address, which is also their login. */
  email?: string;
  /** The handset that finishes their sign-in, masked. Empty means the
   *  account is protected by its password alone. */
  mobile?: string;
  farmer_id?: string;
  grants: Grant[];
  primary_role: string;
  permissions: string[];
  must_change_password: boolean;
  jurisdiction_codes: string[];
}

export interface TokenPair {
  token_type: string;
  access_token: string;
  expires_at: string;
  expires_in: number;
  refresh_token: string;
  refresh_expires_at: string;
}

let accessToken: string | null = null;
let accessExpiry = 0;

/** Refresh a little early, so a request never races its own expiry. */
const EARLY_REFRESH_MS = 30_000;

export function getAccessToken(): string | null {
  if (!accessToken) return null;
  return Date.now() < accessExpiry ? accessToken : null;
}

export function getRefreshToken(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(REFRESH_KEY);
  } catch {
    // Private browsing, or storage disabled. The session then lasts as long
    // as the tab, which is degraded but not broken.
    return null;
  }
}

export function storeTokens(t: TokenPair): void {
  accessToken = t.access_token;
  accessExpiry = new Date(t.expires_at).getTime() - EARLY_REFRESH_MS;
  try {
    window.localStorage.setItem(REFRESH_KEY, t.refresh_token);
  } catch {
    /* nothing to do; see getRefreshToken */
  }
}

export function clearTokens(): void {
  accessToken = null;
  accessExpiry = 0;
  try {
    window.localStorage.removeItem(REFRESH_KEY);
  } catch {
    /* ignore */
  }
}

export function hasSession(): boolean {
  return getAccessToken() !== null || getRefreshToken() !== null;
}

/**
 * Subscribers are notified when the session ends for any reason — a refusal
 * from the API, a replayed refresh token, an explicit sign-out. The React
 * layer uses it to send the user back to the sign-in screen from wherever
 * they were.
 */
type Listener = () => void;
const listeners = new Set<Listener>();

export function onSessionEnded(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function endSession(): void {
  clearTokens();
  listeners.forEach((fn) => fn());
}
