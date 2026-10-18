"use client";

/**
 * Handset sensors: where the farmer is, which way they are facing, and how the
 * phone is held.
 *
 * # What each one is for
 *
 * **Position** is the evidence. It is what matches a photograph to a plot, and
 * it is read live at the moment of capture rather than from the image file,
 * because browsers strip EXIF and EXIF is editable anyway.
 *
 * **Heading** records which way the camera was pointed. Two photographs from
 * the same spot facing opposite ways show different halves of a field, and an
 * officer reading the report later can tell them apart.
 *
 * **Tilt** is guidance, not evidence. A wide shot of a field wants the phone
 * roughly upright; a close-up of a flattened crop wants it pointed down. The
 * app can say "tilt down a bit" instead of a farmer taking four pictures of
 * the sky and finding out a week later that none of them were usable.
 *
 * # What is honest about this
 *
 * Sensor support is uneven. iOS needs an explicit gesture-triggered permission
 * for motion and orientation; some Android browsers report no compass at all;
 * a phone in a metal-framed case gives a heading that is confidently wrong.
 * Everything here degrades to "not available" and the flow continues without
 * it. Only position is required, and only because without it there is nothing
 * to check a photograph against.
 */

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { isReachable, startReachability, subscribe } from "@/lib/offline/reachable";

export interface Fix {
  lat: number;
  lon: number;
  accuracyM: number;
  altitudeM: number | null;
  /** Milliseconds since epoch, from the device. */
  at: number;
}

export type FixState =
  | { status: "idle" }
  | { status: "locating" }
  | { status: "ready"; fix: Fix }
  | { status: "denied" }
  | { status: "unavailable"; reason: string };

/**
 * A continuously updated position.
 *
 * watchPosition rather than a single read: standing still for a few seconds
 * lets a phone settle from a 60 m first guess to a 6 m fix, and the farmer sees
 * the accuracy tighten instead of wondering whether it is working.
 */
export function usePosition(active = true): {
  state: FixState;
  best: Fix | null;
  retry: () => void;
} {
  // What the watch has actually told us, or null before it has said anything.
  // The displayed state is derived from it below rather than assigned here:
  // a state that the effect body has to set on mount is a state the component
  // renders wrongly first and corrects a frame later.
  const [reported, setReported] = useState<FixState | null>(null);
  const [best, setBest] = useState<Fix | null>(null);
  const watchId = useRef<number | null>(null);
  const [nonce, setNonce] = useState(0);

  // Read through the store rather than at render: on a prerendered page
  // navigator does not exist, and assuming "unsupported" there would paint
  // "this device cannot report its position" into the HTML of every phone
  // that can.
  const supported = useSyncExternalStore(
    subscribeToNothing,
    geolocationSupported,
    () => true,
  );

  const retry = useCallback(() => {
    setBest(null);
    setReported(null);
    setNonce((n) => n + 1);
  }, []);

  useEffect(() => {
    if (!active || !supported) return;

    watchId.current = navigator.geolocation.watchPosition(
      (pos) => {
        const fix: Fix = {
          lat: pos.coords.latitude,
          lon: pos.coords.longitude,
          accuracyM: pos.coords.accuracy,
          altitudeM: pos.coords.altitude,
          at: pos.timestamp,
        };
        setReported({ status: "ready", fix });
        // Keep the tightest fix of this session: accuracy wanders, and the
        // best reading is a fairer record than whichever arrived last.
        setBest((prev) => (!prev || fix.accuracyM < prev.accuracyM ? fix : prev));
      },
      (err) => {
        setReported(
          err.code === err.PERMISSION_DENIED
            ? { status: "denied" }
            : {
                status: "unavailable",
                reason:
                  err.code === err.TIMEOUT
                    ? "Could not get a fix. Step into the open and try again."
                    : "Position is not available here.",
              },
        );
      },
      // maximumAge 0: a cached fix from this morning does not say where the
      // photograph was taken.
      { enableHighAccuracy: true, timeout: 30_000, maximumAge: 0 },
    );

    return () => {
      if (watchId.current !== null) {
        navigator.geolocation.clearWatch(watchId.current);
        watchId.current = null;
      }
    };
  }, [active, supported, nonce]);

  const state: FixState = !supported
    ? { status: "unavailable", reason: "This device cannot report its position." }
    : (reported ?? { status: active ? "locating" : "idle" });

  return { state, best, retry };
}

/** Whether this device can report a position at all. */
function geolocationSupported(): boolean {
  return typeof navigator !== "undefined" && !!navigator.geolocation;
}

/**
 * A subscription for something that never changes.
 *
 * Geolocation support is fixed for the life of the page. useSyncExternalStore
 * is used for it anyway because it is the one hook that reads a browser
 * capability without lying about it during prerender.
 */
function subscribeToNothing(): () => void {
  return () => {};
}

/** How good a fix is, in words rather than metres. */
export function accuracyLabel(m: number): { label: string; tone: "good" | "fair" | "poor" } {
  if (m <= 15) return { label: "good", tone: "good" };
  if (m <= 40) return { label: "usable", tone: "fair" };
  return { label: "poor", tone: "poor" };
}

export interface Orientation {
  /** Compass heading in degrees from north, where the device reports one. */
  headingDeg: number | null;
  /** Front-to-back tilt: 0 is flat on its back, 90 is upright. */
  tiltDeg: number | null;
  supported: boolean;
}

/**
 * Device orientation, where the handset offers it.
 *
 * iOS gates this behind a permission that can only be requested from a user
 * gesture, so the flow asks once at the start rather than at the moment of
 * capture, where a permission sheet over the camera would be the worst possible
 * time.
 */
export function useOrientation(active = true): Orientation & { request: () => Promise<boolean> } {
  const [heading, setHeading] = useState<number | null>(null);
  const [tilt, setTilt] = useState<number | null>(null);
  const [supported, setSupported] = useState(false);

  const request = useCallback(async (): Promise<boolean> => {
    const anyEvent = DeviceOrientationEvent as unknown as {
      requestPermission?: () => Promise<"granted" | "denied">;
    };
    if (typeof anyEvent?.requestPermission === "function") {
      try {
        return (await anyEvent.requestPermission()) === "granted";
      } catch {
        return false;
      }
    }
    // Android and desktop need no permission; the events either arrive or
    // they do not.
    return true;
  }, []);

  useEffect(() => {
    if (!active || typeof window === "undefined") return;

    const onOrient = (e: DeviceOrientationEvent) => {
      setSupported(true);

      // webkitCompassHeading is the only reading that is actually north-
      // referenced on iOS; alpha elsewhere is relative to an arbitrary origin
      // unless the event is absolute.
      const webkit = (e as unknown as { webkitCompassHeading?: number })
        .webkitCompassHeading;
      if (typeof webkit === "number" && !Number.isNaN(webkit)) {
        setHeading(webkit);
      } else if (e.absolute && e.alpha !== null) {
        setHeading((360 - e.alpha) % 360);
      }

      if (e.beta !== null) setTilt(e.beta);
    };

    window.addEventListener("deviceorientation", onOrient, true);
    return () => window.removeEventListener("deviceorientation", onOrient, true);
  }, [active]);

  return { headingDeg: heading, tiltDeg: tilt, supported, request };
}

/** The eight-point compass name for a heading, which reads better than 237°. */
export function compassPoint(deg: number | null): string {
  if (deg === null) return "";
  const points = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"];
  return points[Math.round(((deg % 360) + 360) % 360 / 45) % 8];
}

/** How a stage wants the phone held. */
export type TiltTarget = "upright" | "downward" | "any";

/**
 * Whether the phone is held the way this stage needs.
 *
 * Bands are wide on purpose. The point is to stop a photograph of the sky or of
 * the farmer's feet, not to make someone stand at a protractor angle in a wet
 * field.
 */
export function tiltGuidance(
  tilt: number | null,
  target: TiltTarget,
): { ok: boolean; hint: string } {
  if (target === "any" || tilt === null) return { ok: true, hint: "" };

  if (target === "upright") {
    if (tilt > 100) return { ok: false, hint: "Tilt the phone down a little" };
    if (tilt < 55) return { ok: false, hint: "Hold the phone more upright" };
    return { ok: true, hint: "Held level" };
  }

  // downward: pointed at the ground for a close-up.
  if (tilt > 70) return { ok: false, hint: "Point the camera down at the crop" };
  if (tilt < 10) return { ok: false, hint: "Lift the phone a little" };
  return { ok: true, hint: "Pointed at the crop" };
}

/**
 * Battery, where the browser exposes it.
 *
 * A survey is four photographs, a GPS lock and a camera. On an old handset at
 * 8% that is a real risk of the phone dying halfway through, and it is better
 * to say so before the walk than to lose the survey during it.
 */
export function useBattery(): { level: number | null; charging: boolean } {
  const [level, setLevel] = useState<number | null>(null);
  const [charging, setCharging] = useState(false);

  useEffect(() => {
    const nav = navigator as Navigator & {
      getBattery?: () => Promise<{
        level: number;
        charging: boolean;
        addEventListener: (t: string, f: () => void) => void;
        removeEventListener: (t: string, f: () => void) => void;
      }>;
    };
    if (!nav.getBattery) return;

    let battery: Awaited<ReturnType<NonNullable<typeof nav.getBattery>>> | null = null;
    let cancelled = false;

    const update = () => {
      if (!battery || cancelled) return;
      setLevel(battery.level);
      setCharging(battery.charging);
    };

    nav.getBattery().then((b) => {
      if (cancelled) return;
      battery = b;
      update();
      b.addEventListener("levelchange", update);
      b.addEventListener("chargingchange", update);
    }).catch(() => {});

    return () => {
      cancelled = true;
      battery?.removeEventListener("levelchange", update);
      battery?.removeEventListener("chargingchange", update);
    };
  }, []);

  return { level, charging };
}

/**
 * Whether the office can actually be reached.
 *
 * Not navigator.onLine, which only says whether the device has a network
 * interface. A handset on a village tower with no working backhaul reports
 * online while every request fails, and that is the common case here rather
 * than an edge case. See lib/offline/reachable.ts.
 */
export function useOnline(): boolean {
  // Reachability is an external store, and this is the hook for reading one.
  // Assumed reachable during prerender: a page that renders "offline" into
  // its HTML shows the banner for a frame on every load.
  return useSyncExternalStore(subscribeToReachability, isReachable, () => true);
}

function subscribeToReachability(onChange: () => void): () => void {
  startReachability();
  return subscribe(onChange);
}
