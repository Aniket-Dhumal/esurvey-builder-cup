/**
 * Offline storage for the farmer app.
 *
 * # Why this exists
 *
 * A farmer photographs a damaged field standing in that field. Village
 * connectivity is exactly where it is worst, and the moment the evidence is
 * most perishable — the crop is going to be cleared, and a photograph taken
 * next week is a photograph of something else.
 *
 * So nothing in this app waits for a network. Every capture is written to disk
 * on the handset the instant it is taken, and the network is a background
 * concern that catches up later. A farmer who walks their field with no signal
 * and gets home to two bars has lost nothing.
 *
 * # Why raw IndexedDB
 *
 * It is the only browser store that holds Blobs of a few megabytes reliably.
 * localStorage is strings and a 5 MB cap; the Cache API is for responses, not
 * user data. A wrapper library would be one more dependency for about a
 * hundred lines of adapter, and this file is that adapter.
 */

const DB_NAME = "esurvey";
const DB_VERSION = 2;

export const STORE_DRAFTS = "drafts";
export const STORE_CAPTURES = "captures";
export const STORE_META = "meta";
export const STORE_BOUNDARIES = "boundaries";

/** A report the farmer has filled in but that may not have reached the server. */
export interface DraftReport {
  localId: string;
  /** Set once the server has accepted it. Until then this draft is local only. */
  serverId?: string;
  reference?: string;

  surveyEventId: string;
  farmerId: string;
  season: string;
  cropYear: string;
  lossCategory: string;
  calamityType: string;
  occurredAt: string;

  farmId: string;
  surveyNumber: string;
  cropName: string;
  areaSownHa: number;
  areaAffectedHa: number;
  damagePercent: number;

  declarationAccepted: boolean;
  signatureKind: string;

  status: "pending" | "sent" | "failed";
  attempts: number;
  lastError?: string;
  createdAt: number;
  updatedAt: number;
}

/**
 * A plot boundary the farmer walked.
 *
 * Keyed by parcel rather than by a local id: walking the same field again is
 * a correction, not a second boundary, and keeping both would mean the
 * correction races the original on the next sync.
 */
export interface QueuedBoundary {
  farmId: string;
  surveyNumber: string;
  points: { lat: number; lon: number; accuracy_m: number; at: string }[];

  /** What the walk enclosed, so the farmer sees it before it has been sent. */
  areaHa: number;
  worstAccuracyM: number;

  status: "pending" | "sent" | "failed";
  attempts: number;
  lastError?: string;
  createdAt: number;
  updatedAt: number;
}

/**
 * A draft as a screen builds it, before the store stamps it.
 *
 * Named so a form does not have to invent timestamps it has no business
 * knowing -- see putDraft.
 */
export type NewDraft = Omit<DraftReport, "createdAt" | "updatedAt"> &
  Partial<Pick<DraftReport, "createdAt" | "updatedAt">>;

/** One photograph, held on the handset until it has been delivered. */
export interface QueuedCapture {
  localId: string;
  draftLocalId: string;

  kind: string;
  caption: string;

  /** The image itself. Stored as a Blob so nothing is base64-inflated on disk. */
  blob: Blob;
  contentType: string;

  capturedAt: string;
  lat: number;
  lon: number;
  accuracyM: number;
  headingDeg?: number;
  /** Tilt at the moment of capture, which says whether a wide shot was level. */
  tiltDeg?: number;
  device: string;

  status: "pending" | "sent" | "failed";
  attempts: number;
  lastError?: string;
  /** What the server's gates made of it, once it has been delivered. */
  gatesFailed?: string[];
  insideParcel?: boolean | null;
  distanceM?: number | null;
  createdAt: number;
}

let dbPromise: Promise<IDBDatabase> | null = null;

function open(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise;

  dbPromise = new Promise((resolve, reject) => {
    if (typeof indexedDB === "undefined") {
      reject(new Error("This browser cannot store anything offline."));
      return;
    }

    const req = indexedDB.open(DB_NAME, DB_VERSION);

    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE_DRAFTS)) {
        const s = db.createObjectStore(STORE_DRAFTS, { keyPath: "localId" });
        s.createIndex("status", "status");
      }
      if (!db.objectStoreNames.contains(STORE_CAPTURES)) {
        const s = db.createObjectStore(STORE_CAPTURES, { keyPath: "localId" });
        s.createIndex("draftLocalId", "draftLocalId");
        s.createIndex("status", "status");
      }
      if (!db.objectStoreNames.contains(STORE_META)) {
        db.createObjectStore(STORE_META, { keyPath: "key" });
      }
      // Added in version 2. A farmer walks their boundary standing in the
      // field, which is exactly where there is no signal, so the walk has to
      // survive the same way a photograph does.
      if (!db.objectStoreNames.contains(STORE_BOUNDARIES)) {
        const s = db.createObjectStore(STORE_BOUNDARIES, { keyPath: "farmId" });
        s.createIndex("status", "status");
      }
    };

    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("Could not open storage."));
  });

  return dbPromise;
}

function tx<T>(
  store: string,
  mode: IDBTransactionMode,
  run: (s: IDBObjectStore) => IDBRequest<T>,
): Promise<T> {
  return open().then(
    (db) =>
      new Promise<T>((resolve, reject) => {
        const t = db.transaction(store, mode);
        const req = run(t.objectStore(store));
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => reject(req.error);
        // A transaction that aborts after the request succeeded would leave the
        // caller believing a write landed when it did not.
        t.onabort = () => reject(t.error ?? new Error("Storage write aborted."));
      }),
  );
}

export function newLocalId(): string {
  // crypto.randomUUID is not on every Android WebView still in the field.
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/* --- drafts ------------------------------------------------------------- */

/**
 * Write a draft, stamping it.
 *
 * The timestamps belong here rather than to whichever screen built the
 * record. A component computing Date.now() while rendering is reading a clock
 * during a pure function -- React may redo that render at any time, and the
 * value it captured never changes afterwards. Here it is read at the moment
 * of the write, which is what the field actually means.
 */
export async function putDraft(
  d: Omit<DraftReport, "createdAt" | "updatedAt"> & Partial<DraftReport>,
): Promise<DraftReport> {
  const now = Date.now();
  const stamped: DraftReport = {
    ...d,
    createdAt: d.createdAt ?? now,
    updatedAt: now,
  };
  await tx(STORE_DRAFTS, "readwrite", (s) => s.put(stamped));
  // Returned so the caller carries on with what was actually written, rather
  // than with a copy that is missing the fields the store just assigned.
  return stamped;
}

export async function getDraft(localId: string): Promise<DraftReport | undefined> {
  return tx<DraftReport | undefined>(STORE_DRAFTS, "readonly", (s) => s.get(localId));
}

export async function allDrafts(): Promise<DraftReport[]> {
  const rows = await tx<DraftReport[]>(STORE_DRAFTS, "readonly", (s) => s.getAll());
  return rows.sort((a, b) => b.createdAt - a.createdAt);
}

export async function pendingDrafts(): Promise<DraftReport[]> {
  return (await allDrafts()).filter((d) => d.status !== "sent");
}

export async function deleteDraft(localId: string): Promise<void> {
  await tx(STORE_DRAFTS, "readwrite", (s) => s.delete(localId));
}

/* --- captures ----------------------------------------------------------- */

export async function putCapture(c: QueuedCapture): Promise<void> {
  await tx(STORE_CAPTURES, "readwrite", (s) => s.put(c));
}

export async function capturesFor(draftLocalId: string): Promise<QueuedCapture[]> {
  const all = await tx<QueuedCapture[]>(STORE_CAPTURES, "readonly", (s) => s.getAll());
  return all
    .filter((c) => c.draftLocalId === draftLocalId)
    .sort((a, b) => a.createdAt - b.createdAt);
}

export async function pendingCaptures(): Promise<QueuedCapture[]> {
  const all = await tx<QueuedCapture[]>(STORE_CAPTURES, "readonly", (s) => s.getAll());
  return all.filter((c) => c.status !== "sent").sort((a, b) => a.createdAt - b.createdAt);
}

export async function deleteCapture(localId: string): Promise<void> {
  await tx(STORE_CAPTURES, "readwrite", (s) => s.delete(localId));
}

/* --- meta --------------------------------------------------------------- */

export async function setMeta(key: string, value: unknown): Promise<void> {
  await tx(STORE_META, "readwrite", (s) => s.put({ key, value }));
}

export async function getMeta<T>(key: string): Promise<T | undefined> {
  const row = await tx<{ key: string; value: T } | undefined>(
    STORE_META, "readonly", (s) => s.get(key));
  return row?.value;
}

/**
 * How much room is left, where the browser will say.
 *
 * A survey is four photographs of a couple of megabytes each. A handset with
 * 40 MB free will take one survey and then start failing writes, and the
 * farmer should be told before they walk the field rather than after.
 */
export async function storageEstimate(): Promise<{
  usageMB: number;
  quotaMB: number;
} | null> {
  if (typeof navigator === "undefined" || !navigator.storage?.estimate) return null;
  try {
    const e = await navigator.storage.estimate();
    return {
      usageMB: Math.round((e.usage ?? 0) / 1_048_576),
      quotaMB: Math.round((e.quota ?? 0) / 1_048_576),
    };
  } catch {
    return null;
  }
}

/**
 * Ask the browser not to evict this data under storage pressure.
 *
 * Without it, a phone low on space can silently clear IndexedDB — and the one
 * thing this app must never do is lose a photograph the farmer believes they
 * have already taken.
 */
export async function requestPersistence(): Promise<boolean> {
  if (typeof navigator === "undefined" || !navigator.storage?.persist) return false;
  try {
    if (await navigator.storage.persisted()) return true;
    return await navigator.storage.persist();
  } catch {
    return false;
  }
}

/* --- boundaries ------------------------------------------------------- */

/** Queue a walk. Replaces any earlier walk of the same parcel. */
export async function putBoundary(
  b: Omit<QueuedBoundary, "createdAt" | "updatedAt"> &
    Partial<Pick<QueuedBoundary, "createdAt" | "updatedAt">>,
): Promise<QueuedBoundary> {
  const now = Date.now();
  const stamped: QueuedBoundary = {
    ...b,
    createdAt: b.createdAt ?? now,
    updatedAt: now,
  };
  await tx(STORE_BOUNDARIES, "readwrite", (s) => s.put(stamped));
  return stamped;
}

export async function getBoundary(
  farmId: string,
): Promise<QueuedBoundary | undefined> {
  return tx<QueuedBoundary | undefined>(STORE_BOUNDARIES, "readonly", (s) =>
    s.get(farmId),
  );
}

export async function allBoundaries(): Promise<QueuedBoundary[]> {
  const rows = await tx<QueuedBoundary[]>(STORE_BOUNDARIES, "readonly", (s) =>
    s.getAll(),
  );
  return rows ?? [];
}

export async function pendingBoundaries(): Promise<QueuedBoundary[]> {
  return (await allBoundaries()).filter((b) => b.status !== "sent");
}
