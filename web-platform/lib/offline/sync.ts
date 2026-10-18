/**
 * The sync engine.
 *
 * # The ordering that matters
 *
 * A capture cannot be uploaded until its report exists on the server, because
 * the upload is addressed to a report id. So sync is two passes: send the
 * drafts, then send the photographs of the drafts that landed.
 *
 * # Why it is safe to interrupt
 *
 * Every step is idempotent or recorded before it is attempted. A draft records
 * its server id the moment it has one, so a crash mid-sync cannot file the same
 * report twice. A capture that is uploaded but whose acknowledgement is lost is
 * re-uploaded, and the server recognises the identical bytes and flags it as a
 * duplicate rather than double-counting it.
 *
 * The failure this is built against is not a clean disconnect. It is a farmer
 * on one bar walking between two fields, where every request has a decent
 * chance of dying halfway.
 */

import { API_BASE } from "@/lib/api";
import { getAccessToken } from "@/lib/session";
import { reportNetworkFailure, reportNetworkSuccess } from "./reachable";
import {
  type DraftReport,
  type QueuedBoundary,
  type QueuedCapture,
  getDraft,
  pendingBoundaries,
  pendingCaptures,
  pendingDrafts,
  putBoundary,
  putCapture,
  putDraft,
} from "./db";

export interface SyncResult {
  boundariesSent: number;
  draftsSent: number;
  capturesSent: number;
  failed: number;
  /** True when something is still waiting, so the caller keeps the badge up. */
  outstanding: boolean;
}

/**
 * Attempts before a queued item is left alone until the farmer retries.
 *
 * Only *refusals* count against this. A network failure does not: the office
 * never saw the request, so there is nothing to give up on, and a farmer
 * walking between two fields would otherwise exhaust the budget on a signal
 * that was about to come back.
 */
const MAX_ATTEMPTS = 6;

/**
 * Backoff between automatic retries.
 *
 * Deliberately short at first and capped low. This is not a server under load
 * being protected; it is a handset drifting in and out of a single bar of
 * signal, and the right behaviour is to keep trying without draining a battery
 * the farmer needs for the walk home.
 */
function backoffMs(attempts: number): number {
  return Math.min(2000 * 2 ** attempts, 60_000);
}

let running = false;

/** Whether a sync pass is in flight, so the UI does not start a second. */
export function syncing(): boolean {
  return running;
}

export async function sync(): Promise<SyncResult> {
  const result: SyncResult = {
    boundariesSent: 0, draftsSent: 0, capturesSent: 0,
    failed: 0, outstanding: false,
  };

  if (running) return result;
  // navigator.onLine, not the probe: it is only trustworthy when it says
  // "down", and that is exactly the case worth skipping a pass for. A probe
  // that says down should still let one real attempt through, because the
  // upload is the better test of whether anything can get out.
  if (typeof navigator !== "undefined" && !navigator.onLine) {
    result.outstanding = (await pendingDrafts()).length > 0
      || (await pendingCaptures()).length > 0
      || (await pendingBoundaries()).length > 0;
    return result;
  }
  if (!getAccessToken() && !hasRefresh()) {
    // Signed out. Nothing is lost — it stays queued until they sign in again.
    result.outstanding = true;
    return result;
  }

  running = true;
  try {
    // Boundaries first. A report's photographs are geofenced against the
    // parcel boundary, so a walk that lands after its captures leaves them
    // checked against nothing -- and the gate result is recorded at upload
    // time, not re-evaluated later.
    for (const boundary of await pendingBoundaries()) {
      if (boundary.attempts >= MAX_ATTEMPTS) continue;
      if (await sendBoundary(boundary)) result.boundariesSent++;
      else result.failed++;
    }

    for (const draft of await pendingDrafts()) {
      if (draft.serverId) continue;
      if (draft.attempts >= MAX_ATTEMPTS) continue;
      if (await sendDraft(draft)) result.draftsSent++;
      else result.failed++;
    }

    for (const capture of await pendingCaptures()) {
      if (capture.attempts >= MAX_ATTEMPTS) continue;
      const draft = await getDraft(capture.draftLocalId);
      // Its report has not landed yet; it will go on the next pass.
      if (!draft?.serverId) continue;
      if (await sendCapture(capture, draft.serverId)) result.capturesSent++;
      else result.failed++;
    }

    result.outstanding =
      (await pendingDrafts()).some((d) => !d.serverId) ||
      (await pendingCaptures()).length > 0 ||
      (await pendingBoundaries()).length > 0;
  } finally {
    running = false;
  }

  return result;
}

async function sendDraft(draft: DraftReport): Promise<boolean> {
  try {
    const res = await authedFetch(`${API_BASE}/api/v1/reports`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        survey_event_id: draft.surveyEventId,
        farmer_id: draft.farmerId,
        season: draft.season,
        crop_year: draft.cropYear,
        loss_category: draft.lossCategory,
        calamity_type: draft.calamityType,
        occurred_at: draft.occurredAt,
        declaration_accepted: draft.declarationAccepted,
        farmer_signature_kind: draft.signatureKind,
        documents_promised: ["7_12"],
        items: [
          {
            survey_number: draft.surveyNumber,
            farm_id: draft.farmId,
            crop_name: draft.cropName,
            area_sown_ha: draft.areaSownHa,
            area_affected_ha: draft.areaAffectedHa,
            damage_percent: draft.damagePercent,
          },
        ],
      }),
    });

    const body = await res.json().catch(() => null);

    if (!res.ok) {
      // A refusal the server will give every time — a closed event, a bad
      // figure — is not worth retrying until the farmer changes something.
      const permanent = res.status >= 400 && res.status < 500 && res.status !== 429;
      await putDraft({
        ...draft,
        attempts: permanent ? MAX_ATTEMPTS : draft.attempts + 1,
        status: permanent ? "failed" : "pending",
        lastError: body?.error?.message ?? `The office refused it (${res.status}).`,
      });
      return false;
    }

    reportNetworkSuccess();
    await putDraft({
      ...draft,
      serverId: body.id,
      reference: body.reference,
      status: "sent",
      lastError: undefined,
    });
    return true;
  } catch {
    // A network failure is not a strike against the report. Counting it would
    // let a farmer on one bar of signal burn the whole retry budget in a
    // minute and then need to press a button they cannot see the reason for.
    reportNetworkFailure();
    await putDraft({ ...draft, lastError: "Could not reach the office." });
    return false;
  }
}

async function sendCapture(capture: QueuedCapture, reportId: string): Promise<boolean> {
  try {
    const form = new FormData();
    form.set("image", capture.blob, "capture.jpg");
    form.set("kind", capture.kind);
    form.set("caption", capture.caption);
    form.set("lat", String(capture.lat));
    form.set("lon", String(capture.lon));
    form.set("accuracy_m", String(capture.accuracyM));
    form.set("captured_at", capture.capturedAt);
    form.set("device", capture.device);
    if (capture.headingDeg !== undefined) {
      form.set("heading_deg", String(capture.headingDeg));
    }

    const res = await authedFetch(
      `${API_BASE}/api/v1/reports/${encodeURIComponent(reportId)}/captures`,
      { method: "POST", body: form },
    );
    const body = await res.json().catch(() => null);

    if (!res.ok) {
      const permanent = res.status >= 400 && res.status < 500 && res.status !== 429;
      await putCapture({
        ...capture,
        attempts: permanent ? MAX_ATTEMPTS : capture.attempts + 1,
        status: permanent ? "failed" : "pending",
        lastError: body?.error?.message ?? `Could not be sent (${res.status}).`,
      });
      return false;
    }

    reportNetworkSuccess();
    await putCapture({
      ...capture,
      status: "sent",
      lastError: undefined,
      gatesFailed: body?.capture?.gates_failed ?? [],
      insideParcel: body?.capture?.inside_parcel ?? null,
      distanceM: body?.capture?.distance_from_parcel_m ?? null,
    });
    return true;
  } catch {
    // As above: the photograph is on the handset and the network is the thing
    // that failed. It keeps its attempts.
    reportNetworkFailure();
    await putCapture({ ...capture, lastError: "Could not reach the office." });
    return false;
  }
}

/**
 * A fetch that refreshes an expired token once.
 *
 * A queued capture can sit for a day before there is signal, by which time the
 * access token is long gone. Losing the upload to a 401 the client could have
 * fixed itself would be the wrong outcome.
 */
async function authedFetch(url: string, init: RequestInit): Promise<Response> {
  const send = (token: string | null) =>
    fetch(url, {
      ...init,
      headers: { ...init.headers, Authorization: `Bearer ${token ?? ""}` },
    });

  let res = await send(getAccessToken());
  if (res.status === 401 && (await refreshToken())) {
    res = await send(getAccessToken());
  }
  return res;
}

function hasRefresh(): boolean {
  try {
    return !!window.localStorage.getItem("esurvey.refresh");
  } catch {
    return false;
  }
}

async function refreshToken(): Promise<boolean> {
  try {
    const token = window.localStorage.getItem("esurvey.refresh");
    if (!token) return false;
    const res = await fetch(`${API_BASE}/api/v1/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: token }),
    });
    if (!res.ok) return false;
    const body = await res.json();
    const { storeTokens } = await import("@/lib/session");
    storeTokens(body);
    return true;
  } catch {
    return false;
  }
}

/**
 * Keep trying, quietly, for as long as the app is open.
 *
 * Reacts to the browser coming back online rather than only polling, so a
 * farmer who walks into signal sees the queue drain within a second or two
 * instead of waiting out an interval.
 */
export function startAutoSync(onResult: (r: SyncResult) => void): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let attempts = 0;
  let stopped = false;

  const run = async () => {
    if (stopped) return;
    const r = await sync();
    onResult(r);
    attempts = r.failed > 0 ? attempts + 1 : 0;
    // Nothing waiting: check back rarely. Something waiting: back off.
    const next = r.outstanding ? backoffMs(attempts) : 60_000;
    timer = setTimeout(run, next);
  };

  const onOnline = () => {
    if (timer) clearTimeout(timer);
    attempts = 0;
    void run();
  };

  window.addEventListener("online", onOnline);
  void run();

  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    window.removeEventListener("online", onOnline);
  };
}

/**
 * Push one walked boundary.
 *
 * A farmer walks their field standing in it, which is exactly where there is
 * no signal. Sending this at the moment they press finish -- as the first
 * version did -- loses the whole walk, and a walk is five minutes of somebody
 * pacing a field edge in the sun.
 */
async function sendBoundary(boundary: QueuedBoundary): Promise<boolean> {
  try {
    const res = await authedFetch(
      `${API_BASE}/api/v1/parcels/${encodeURIComponent(boundary.farmId)}/boundary`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ points: boundary.points }),
      },
    );
    const body = await res.json().catch(() => null);

    if (!res.ok) {
      // A 4xx is the server saying this walk cannot be accepted -- too few
      // corners, a fix too coarse, a registry boundary already there. No
      // number of retries changes any of those, and retrying forever would
      // hide the reason from the farmer.
      const permanent = res.status >= 400 && res.status < 500 && res.status !== 429;
      await putBoundary({
        ...boundary,
        attempts: permanent ? MAX_ATTEMPTS : boundary.attempts + 1,
        status: permanent ? "failed" : "pending",
        lastError: body?.error?.message ?? `Could not be sent (${res.status}).`,
      });
      return false;
    }

    await putBoundary({ ...boundary, status: "sent", lastError: undefined });
    return true;
  } catch {
    await putBoundary({
      ...boundary,
      attempts: boundary.attempts + 1,
      lastError: "No network.",
    });
    return false;
  }
}
