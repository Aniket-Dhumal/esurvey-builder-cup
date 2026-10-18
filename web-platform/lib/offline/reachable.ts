"use client";

/**
 * Whether the office can actually be reached.
 *
 * # Why navigator.onLine is not enough
 *
 * It reports whether the device has a network interface, not whether anything
 * is reachable over it. A handset attached to a village tower with no working
 * backhaul, a captive portal at a taluka office, a dead DNS resolver — all of
 * these report `onLine === true` while every request fails. That is the common
 * case in the places this app is for, not an edge case.
 *
 * Next.js documents the same problem for its own offline handling and solves it
 * by probing (`app/guides/offline-support`, "How retry works"). This app cannot
 * use that machinery — it is a static export with no RSC navigations or Server
 * Actions to retry, and its own uploads are, in the framework's words, under
 * "that library's own retry policy". So the technique is borrowed and pointed
 * at the API instead.
 *
 * # The 200 ms trick, and why it works
 *
 * A probe that is aborted at 200 ms counts as *online*. A genuinely
 * unreachable host fails almost immediately with a DNS or TCP error; a request
 * still in flight at 200 ms has already completed its handshake. This is much
 * faster than waiting for a response, which matters when the answer decides
 * whether to show a farmer "saved on your phone" or "sent".
 */

import { API_BASE } from "@/lib/api";

/** Stepped, not exponential, and capped — a handset battery has to last. */
const BACKOFF_MS = [500, 1000, 2000, 3000];

/** A probe still pending at this point has reached the server. */
const PROBE_TIMEOUT_MS = 200;

type Listener = (reachable: boolean) => void;

let reachable = true;
let probing = false;
let attempt = 0;
let timer: ReturnType<typeof setTimeout> | null = null;
const listeners = new Set<Listener>();
let started = false;

/** The last known answer, without waiting for a probe. */
export function isReachable(): boolean {
  return reachable;
}

function set(next: boolean) {
  if (next === reachable) return;
  reachable = next;
  listeners.forEach((fn) => fn(next));
}

/**
 * One probe against the API's liveness endpoint.
 *
 * /healthz is public and does no work, so probing it costs the server nothing
 * and needs no credentials — which matters, because this runs while signed out
 * too.
 */
async function probe(): Promise<boolean> {
  const controller = new AbortController();
  const t = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    await fetch(`${API_BASE}/healthz`, {
      method: "HEAD",
      cache: "no-store",
      signal: controller.signal,
    });
    return true;
  } catch (err) {
    // An abort means the request was still in flight at the deadline, which
    // means it got somewhere. Anything else is a real failure.
    return err instanceof DOMException && err.name === "AbortError";
  } finally {
    clearTimeout(t);
  }
}

function scheduleProbe() {
  if (timer) clearTimeout(timer);
  const delay = BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)];
  timer = setTimeout(runProbe, delay);
}

async function runProbe() {
  if (probing) return;
  probing = true;
  try {
    const ok = await probe();
    if (ok) {
      attempt = 0;
      set(true);
      // Reachable: stop polling. The next failed request, or an offline
      // event, will start it again.
      if (timer) clearTimeout(timer);
      timer = null;
    } else {
      attempt++;
      set(false);
      scheduleProbe();
    }
  } finally {
    probing = false;
  }
}

/**
 * Report that a real request failed at the network layer.
 *
 * This is the second detection path, and the one that catches the captive
 * portal: the browser insists it is online, the upload disagrees, and the
 * upload is the one that knows.
 */
export function reportNetworkFailure(): void {
  attempt = 0;
  set(false);
  void runProbe();
}

/** Report that a real request succeeded, which settles the question. */
export function reportNetworkSuccess(): void {
  attempt = 0;
  set(true);
  if (timer) clearTimeout(timer);
  timer = null;
}

/** Begin watching. Idempotent; safe to call from every component that cares. */
export function startReachability(): void {
  if (started || typeof window === "undefined") return;
  started = true;

  window.addEventListener("offline", () => {
    // The OS saying the interface is down is trustworthy in that direction:
    // there is definitely nothing there.
    attempt = 0;
    set(false);
    scheduleProbe();
  });

  window.addEventListener("online", () => {
    // The OS saying the interface is up is not trustworthy on its own, so
    // this only short-circuits the wait rather than declaring success.
    attempt = 0;
    void runProbe();
  });

  if (!navigator.onLine) {
    set(false);
    scheduleProbe();
  }
}

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
