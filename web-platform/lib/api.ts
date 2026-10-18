import type {
  Adjudication,
  FarmerSummary,
  Grievance,
  GrievanceGrounds,
  Paged,
  ParcelRow,
  RegistrySummary,
  AdjudicationEntry,
  Capture,
  ParcelBoundary,
  ApiErrorBody,
  Assessment,
  BoundaryTrace,
  FieldMeasurement,
  FieldMeasurementSummary,
  AssessmentSummary,
  AuditEntry,
  Consensus,
  CreateAssessmentInput,
  AssessmentJob,
  CropLossReport,
  FarmerRecord,
  Finding,
  Jurisdiction,
  OfficerAccount,
  Overview,
  ApprovalLevel,
  ApprovalRecord,
  BulkApprovalResult,
  ReportSummary,
  ClaimProgress,
  StuckVillage,
  SurveyEvent,
  VerificationBrief,
  VillageAuthorities,
  WorklistEntry,
  WaitingEntry,
  Integration,
  CircleCoverage,
  Statewide,
  Notification,
} from "./types";
import { messageFor } from "./errors";
import {
  clearTokens,
  endSession,
  getAccessToken,
  getRefreshToken,
  storeTokens,
  type CurrentUser,
  type TokenPair,
} from "./session";

/**
 * Where the platform is.
 *
 * # Why a missing value fails the build rather than falling back
 *
 * Because it has twice shipped. This console is a static export, so whatever is
 * here at build time is baked into every chunk and cannot be changed afterwards;
 * a build run without NEXT_PUBLIC_API_URL produced a console that asked every
 * officer's browser for localhost:8090 and told them the service was unreachable.
 * That was field-trial fault #15, and then it happened again.
 *
 * The localhost default is genuinely useful -- it is how a developer runs the
 * console against a local API with nothing configured -- so it is kept for exactly
 * that case and refused for the one where it is a live outage. NODE_ENV is known
 * statically at build time, which is what makes this a failed build rather than a
 * runtime surprise: `next build` prerenders these pages, the throw happens there,
 * and nobody deploys anything.
 *
 * There is a grep in the deploy that refuses a bundle with the literal in it. This
 * is the same rule moved to the earliest moment it can be enforced, because a guard
 * at the last step only works for whoever remembers to run it.
 */
export const API_BASE =
  process.env.NEXT_PUBLIC_API_URL ??
  (process.env.NODE_ENV === "production"
    ? (() => {
        throw new Error(
          "NEXT_PUBLIC_API_URL must be set for a production build. " +
            "Without it this console is compiled to ask every browser for " +
            "http://localhost:8090, which has taken the live console down twice. " +
            "Build with NEXT_PUBLIC_API_URL=https://esurvey.mindstrix.com",
        );
      })()
    : "http://localhost:8090");

/**
 * Where a WebSocket to this platform goes.
 *
 * Derived from API_BASE rather than configured beside it, and that is not tidiness.
 * The line above has a localhost fallback, which exists so a developer can run the
 * console without an environment file — and which has twice been built into a
 * production bundle and taken the console down. One such line is one too many; a
 * second, for sockets, would be a second outage waiting for the same mistake.
 *
 * So there is exactly one place this deployment's address is written, and this
 * changes its scheme: https becomes wss, http becomes ws, and a base that was
 * wrong is visibly wrong everywhere at once rather than in half the features.
 */
function socketBase(): string {
  return API_BASE.replace(/^http/i, "ws");
}

/**
 * An error the API reported, carrying its stable code so callers can branch on
 * it rather than on message text.
 *
 * NO_GEOMETRY and NO_OBSERVATIONS in particular are expected outcomes with
 * specific remedies, not faults — the UI explains them rather than showing a
 * generic failure.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;

  /**
   * What the API said, kept for the console log and never for the screen.
   *
   * The API writes for programs — "farmer_id is required", "the access token
   * has expired; refresh it" — and anything it failed to sanitise arrives
   * intact. It is worth having when debugging and worth nobody's time on a
   * red banner.
   */
  readonly detail: string;

  /**
   * `message` is the sentence a person reads.
   *
   * Deriving it here rather than at each call site is the point: every screen
   * in the console already renders `error.message`, so the twenty-odd places
   * that used to echo the server became correct the moment this line changed,
   * and a new screen gets it right without knowing this problem exists.
   */
  constructor(status: number, code: string, detail: string) {
    super(messageFor(status, code));
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

/**
 * A single in-flight refresh, shared by every caller.
 *
 * Without this, a page that fires four requests on mount and gets four 401s
 * would run four rotations. The backend treats a replayed refresh token as a
 * theft and revokes the whole family, so the concurrent case is not merely
 * wasteful — it would sign the user out.
 */
let refreshing: Promise<boolean> | null = null;

async function refreshAccessToken(): Promise<boolean> {
  if (refreshing) return refreshing;

  refreshing = (async () => {
    const token = getRefreshToken();
    if (!token) return false;

    try {
      const res = await fetch(`${API_BASE}/api/v1/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refresh_token: token }),
      });
      if (!res.ok) {
        // Expired, revoked, or replayed. All three mean the same thing to
        // the user: sign in again.
        endSession();
        return false;
      }
      storeTokens((await res.json()) as TokenPair);
      return true;
    } catch {
      // A network failure is not a revoked session; keep what we have and
      // let the caller surface the outage.
      return false;
    } finally {
      refreshing = null;
    }
  })();

  return refreshing;
}

/**
 * Send a request and hand back the raw Response.
 *
 * For the replies that are not JSON: a filled workbook comes back as a file, and
 * parsing it as a body would throw away the bytes. Shares the token handling with
 * [request] rather than duplicating it, because a second copy of the refresh logic
 * is a second place for a sign-out loop to live.
 */
async function rawRequest(
  path: string,
  init?: RequestInit,
  opts: { retry?: boolean } = {},
): Promise<Response> {
  if (!getAccessToken() && getRefreshToken()) {
    await refreshAccessToken();
  }
  const access = getAccessToken();

  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      ...init,
      headers: {
        // No Content-Type: this is used for multipart uploads, where the browser
        // sets it with the boundary and overriding it breaks the parse.
        ...(access ? { Authorization: `Bearer ${access}` } : {}),
        ...init?.headers,
      },
    });
  } catch {
    throw new ApiError(0, "NETWORK", `no response from ${API_BASE}`);
  }

  if (res.status === 401 && !opts.retry) {
    if (await refreshAccessToken()) {
      return rawRequest(path, init, { retry: true });
    }
    endSession();
  }

  if (!res.ok) {
    // The body may be JSON even when the success case is not.
    let code = "UNKNOWN";
    let message = `request failed (${res.status})`;
    try {
      const body = (await res.json()) as ApiErrorBody;
      code = body?.error?.code ?? code;
      message = body?.error?.message ?? message;
    } catch {
      // A gateway's HTML, or an empty body. The status is enough.
    }
    throw new ApiError(res.status, code, message);
  }
  return res;
}

async function request<T>(
  path: string,
  init?: RequestInit,
  opts: { anonymous?: boolean; retry?: boolean } = {},
): Promise<T> {
  // Refresh before sending rather than after a 401 where possible: it saves a
  // round trip and keeps the retry path for the cases that genuinely surprise
  // us, such as a grant revoked mid-session.
  if (!opts.anonymous && !getAccessToken() && getRefreshToken()) {
    await refreshAccessToken();
  }

  const access = getAccessToken();

  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      ...init,
      headers: {
        // JSON unless the body is a form.
        //
        // A FormData body must carry no Content-Type from us: the browser writes one
        // with the multipart boundary in it, and a header set here overwrites that
        // with a type whose boundary is missing -- so the server reads a well-formed
        // upload as a malformed one. Worth stating, because the failure is a 400 on
        // the far side with nothing wrong on this one.
        ...(init?.body && !(init.body instanceof FormData)
          ? { "Content-Type": "application/json" }
          : {}),
        ...(!opts.anonymous && access
          ? { Authorization: `Bearer ${access}` }
          : {}),
        ...init?.headers,
      },
    });
  } catch {
    // No HTTP status at all: the request never got an answer. In development
    // that is usually the Go server being down or a CORS origin that does not
    // list this port; to anyone else it is simply the network. The detail
    // names the base URL for the console log, and ApiError turns the code into
    // something a person can act on.
    throw new ApiError(0, "NETWORK", `no response from ${API_BASE}`);
  }

  const text = await res.text();
  let body: unknown = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }

  if (!res.ok) {
    const err = body as ApiErrorBody | null;
    const code = err?.error?.code ?? "UNKNOWN";

    // One retry, and only for an expired access token. Retrying a genuine
    // refusal would turn a forbidden request into a sign-out loop.
    if (
      res.status === 401 &&
      code === "TOKEN_EXPIRED" &&
      !opts.anonymous &&
      !opts.retry
    ) {
      if (await refreshAccessToken()) {
        return request<T>(path, init, { ...opts, retry: true });
      }
    }

    if (res.status === 401 && !opts.anonymous) {
      endSession();
    }

    throw new ApiError(
      res.status,
      code,
      err?.error?.message ?? `HTTP ${res.status}`,
    );
  }

  return body as T;
}

/** limit, offset and q, for any listing. */
export interface PageArgs {
  limit?: number;
  offset?: number;
  q?: string;
}

function pageQuery(a: PageArgs = {}, extra: Record<string, string | undefined> = {}) {
  const p = new URLSearchParams();
  if (a.limit) p.set("limit", String(a.limit));
  if (a.offset) p.set("offset", String(a.offset));
  if (a.q) p.set("q", a.q);
  for (const [k, v] of Object.entries(extra)) if (v) p.set(k, v);
  const s = p.toString();
  return s ? `?${s}` : "";
}

/**
 * Fetching a document that is HTML rather than JSON.
 *
 * # Why these are fetched and not linked
 *
 * They were links — `<a href={...} target="_blank">` — and they had never
 * worked. The access token lives in memory in this tab; a browser opening a
 * new one sends no Authorization header and gets no cookie, so the endpoint
 * answered 401 and the officer was shown a JSON error object in a fresh tab.
 * The endpoints themselves were always fine.
 *
 * Fetched with the token like every other call, which also means it goes
 * through the refresh path: a print at the end of a long session is exactly
 * when the access token has expired.
 */
async function printableHtml(path: string): Promise<string> {
  if (!getAccessToken() && getRefreshToken()) {
    await refreshAccessToken();
  }
  const get = () =>
    fetch(`${API_BASE}${path}`, {
      headers: { Authorization: `Bearer ${getAccessToken() ?? ""}` },
    });

  let res = await get();
  if (res.status === 401 && (await refreshAccessToken())) {
    res = await get();
  }
  if (!res.ok) throw new ApiError(res.status, "PRINT_FAILED", "");
  return res.text();
}

export const api = {
  // --- the console's bell ------------------------------------------------
  //
  // Scoped to the caller by their own token; there is deliberately no way to
  // ask for another officer's, so none of these takes a user id.
  listNotifications: (unread = false, limit = 30) =>
    request<{ items: Notification[]; unread: number }>(
      `/api/v1/notifications?unread=${unread}&limit=${limit}`,
    ),
  markNotificationRead: (id: string) =>
    request<{ unread: number }>(`/api/v1/notifications/${id}/read`, { method: "POST" }),
  markAllNotificationsRead: () =>
    request<{ cleared: number; unread: number }>(
      "/api/v1/notifications/read-all",
      { method: "POST" },
    ),

  // --- claiming an invited account ---------------------------------------
  //
  // Anonymous by necessity: the holder of the link has no credential yet, and
  // the invitation token is the credential. Single-use, expiring, and stored
  // on the server only as a hash.
  checkInvite: (token: string) =>
    request<{
      full_name: string;
      email: string;
      expires_at: string;
      /** "invite" claims a new account; "reset" replaces a password. */
      purpose: "invite" | "reset";
    }>(
      `/api/v1/auth/invite?token=${encodeURIComponent(token)}`,
      undefined,
      { anonymous: true },
    ),

  /**
   * Ask for a link to set a new password.
   *
   * Answers identically whether or not the address belongs to an account —
   * deliberately, because a reset form that says "no such account" is an
   * account enumerator with a friendly face. So the message that comes back
   * is shown as it is, and never turned into "sent" or "not found".
   */
  requestPasswordReset: (email: string) =>
    request<{ status: string; message: string }>(
      "/api/v1/auth/password/reset",
      { method: "POST", body: JSON.stringify({ email }) },
      { anonymous: true },
    ),

  acceptPasswordReset: (token: string, password: string) =>
    request<{ status: string; email: string }>(
      "/api/v1/auth/password/reset/accept",
      { method: "POST", body: JSON.stringify({ token, password }) },
      { anonymous: true },
    ),

  acceptInvite: (token: string, password: string) =>
    request<{ status: string; email: string }>(
      "/api/v1/auth/invite/accept",
      { method: "POST", body: JSON.stringify({ token, password }) },
      { anonymous: true },
    ),

  /**
   * What the platform is and is not connected to.
   *
   * platform:read, which is held by whoever runs this and nobody who uses it.
   * Never returns a credential — only whether one is present and what its
   * variable is called.
   */
  integrations: () =>
    request<{ integrations: Integration[]; environment: string }>(
      "/api/v1/platform/integrations",
    ),

  /** Claim stage and account counts per state and district; no records. */
  platformOverview: () =>
    request<PlatformOverview>("/api/v1/platform/overview"),

  /** Live service health, database, queues and request load. */
  platformHealth: () => request<PlatformHealth>("/api/v1/platform/health"),

  /** How much of the circle tier exists, per taluka. */
  circleCoverage: () =>
    request<CircleCoverage>("/api/v1/platform/circle-coverage"),

  /**
   * Oversight above a district.
   *
   * Scoped like every other read: a State Officer gets the state, a Collector
   * gets their district broken down by taluka.
   */
  statewide: (eventId?: string) =>
    request<Statewide>(
      `/api/v1/statewide${eventId ? `?event_id=${encodeURIComponent(eventId)}` : ""}`,
    ),

  // Liveness only. readyz is the one that reports the database, Redis and the
  // agent, and it is what the sidebar indicator should grow into.
  health: () =>
    request<{ status: string; env: string }>("/healthz", undefined, {
      anonymous: true,
    }),

  ready: () =>
    request<{ status: string; checks: Record<string, unknown> }>(
      "/readyz",
      undefined,
      { anonymous: true },
    ),

  // --- authentication ---------------------------------------------------

  /**
   * The first half of an officer's sign-in.
   *
   * A password alone is a sign-in only for an account with no mobile number
   * on file; everywhere else it earns a challenge, and the code sent to that
   * handset finishes the job. So this returns which of the two happened
   * rather than assuming it has tokens to store.
   */
  loginOfficer: async (
    username: string,
    password: string,
  ): Promise<
    { kind: "signed-in" } | { kind: "code-required"; challenge: string; mobileHint: string }
  > => {
    const res = await request<
      Partial<TokenPair> & { challenge?: string; mobile_hint?: string }
    >(
      "/api/v1/auth/officer/login",
      { method: "POST", body: JSON.stringify({ username, password }) },
      { anonymous: true },
    );
    if (res.challenge) {
      return {
        kind: "code-required",
        challenge: res.challenge,
        mobileHint: res.mobile_hint ?? "",
      };
    }
    storeTokens(res as TokenPair);
    return { kind: "signed-in" };
  },

  // --- your own handset, which is your second factor --------------------

  /** Sends a code to the number, and returns the challenge that proves it. */
  startMobileChange: (mobile: string) =>
    request<{ challenge: string; mobile_hint: string; expires_at: string }>(
      "/api/v1/me/mobile",
      { method: "POST", body: JSON.stringify({ mobile }) },
    ),

  confirmMobileChange: (challenge: string, code: string) =>
    request<{ status: string; message: string }>(
      "/api/v1/me/mobile/verify",
      { method: "POST", body: JSON.stringify({ challenge, code }) },
    ),

  removeMobile: () =>
    request<{ status: string }>("/api/v1/me/mobile", { method: "DELETE" }),

  /** The second half: the code from the officer's handset. */
  verifyOfficerCode: async (challenge: string, code: string) => {
    const res = await request<TokenPair & { user: unknown }>(
      "/api/v1/auth/officer/verify",
      { method: "POST", body: JSON.stringify({ challenge, code }) },
      { anonymous: true },
    );
    storeTokens(res);
    return res;
  },

  requestFarmerOtp: (mobile: string) =>
    request<{ status: string; message: string }>(
      "/api/v1/auth/farmer/otp",
      { method: "POST", body: JSON.stringify({ mobile }) },
      { anonymous: true },
    ),

  verifyFarmerOtp: async (mobile: string, code: string) => {
    const res = await request<TokenPair & { user: unknown }>(
      "/api/v1/auth/farmer/verify",
      { method: "POST", body: JSON.stringify({ mobile, code }) },
      { anonymous: true },
    );
    storeTokens(res);
    return res;
  },

  me: () => request<CurrentUser>("/api/v1/auth/me"),

  changePassword: (current_password: string, new_password: string) =>
    request<{ status: string; message: string }>("/api/v1/auth/password", {
      method: "POST",
      body: JSON.stringify({ current_password, new_password }),
    }),

  logout: async () => {
    const refresh_token = getRefreshToken();
    try {
      if (refresh_token) {
        await request<void>(
          "/api/v1/auth/logout",
          { method: "POST", body: JSON.stringify({ refresh_token }) },
          { anonymous: true },
        );
      }
    } finally {
      // Local state is cleared whatever the server said. A sign-out that
      // fails silently and leaves the user signed in is the worse failure.
      clearTokens();
    }
  },

  // Rows, not identifiers. This used to return every farmer id and the
  // console then fetched each record to render a name.
  // One call instead of loading every record to add them up in the browser.
  grievanceGrounds: () =>
    request<GrievanceGrounds>("/api/v1/grievance-grounds"),

  listGrievances: (
    a?: PageArgs,
    params: { report_id?: string; state?: string; open?: string } = {},
  ) => request<Paged<Grievance>>(`/api/v1/grievances${pageQuery(a, params)}`),

  getGrievance: (id: string) =>
    request<Grievance>(`/api/v1/grievances/${encodeURIComponent(id)}`),

  fileGrievance: (body: {
    report_id: string;
    ground: string;
    statement: string;
    claimed_percent?: number;
    channel?: string;
  }) =>
    request<Grievance>("/api/v1/grievances", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  assignGrievance: (id: string, officerId?: string) =>
    request<Grievance>(`/api/v1/grievances/${encodeURIComponent(id)}/assign`, {
      method: "POST",
      body: JSON.stringify({ officer_id: officerId ?? "" }),
    }),

  decideGrievance: (
    id: string,
    body: { state: string; reason: string; revised_percent?: number | null },
  ) =>
    request<Grievance>(`/api/v1/grievances/${encodeURIComponent(id)}/decide`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  withdrawGrievance: (id: string) =>
    request<Grievance>(`/api/v1/grievances/${encodeURIComponent(id)}/withdraw`, {
      method: "POST",
      body: "{}",
    }),

  /**
   * `boundary` is "with" or "without". A parcel with no boundary cannot be
   * assessed — there is no polygon to run satellite or radar against — so
   * "without" is the list somebody works through when chasing AgriStack for
   * missing geometry.
   *
   * `crop` is a crop name, or "undeclared" for the parcels AgriStack returns
   * no crop registry for at all.
   */
  listParcels: (
    a?: PageArgs,
    params: { boundary?: string; crop?: string } = {},
  ) => request<Paged<ParcelRow>>(`/api/v1/parcels${pageQuery(a, params)}`),

  registrySummary: () => request<RegistrySummary>("/api/v1/farmers/summary"),

  /**
   * `boundary` is "missing" or "complete": whether every parcel this farmer
   * holds has a boundary. A farmer with one unmapped parcel is a farmer whose
   * claim will stall, and finding them is what this page is opened for.
   */
  listFarmers: (a?: PageArgs, params: { boundary?: string } = {}) =>
    request<Paged<FarmerSummary>>(`/api/v1/farmers${pageQuery(a, params)}`),

  getFarmer: (farmerId: string) =>
    request<FarmerRecord>(`/api/v1/farmers/${encodeURIComponent(farmerId)}`),

  adjudicationQueue: (a?: PageArgs) =>
    request<Paged<AdjudicationEntry>>(`/api/v1/adjudications${pageQuery(a)}`),

  overview: (eventId?: string) => {
    const q = eventId ? `?event_id=${encodeURIComponent(eventId)}` : "";
    return request<Overview>(`/api/v1/overview${q}`);
  },

  /**
   * `triage` narrows to one tier. The server has accepted it all along; the
   * console simply never sent it, so an officer looking for the escalated
   * assessments among four hundred had only the search box.
   */
  listAssessments: (
    a?: PageArgs,
    params: { farmer_id?: string; farm_id?: string; triage?: string } | string = {},
  ) => {
    const p = typeof params === "string" ? { farmer_id: params } : params;
    return request<Paged<AssessmentSummary>>(
      `/api/v1/assessments${pageQuery(a, p)}`,
    );
  },

  getAssessment: (id: string) =>
    request<Assessment>(`/api/v1/assessments/${encodeURIComponent(id)}`),

  /**
   * Ask for an assessment. Answers 202 with a job to poll, not the assessment:
   * producing one is minutes of Earth Engine and model work, and the request
   * used to hold a connection open for all of it.
   */
  requestAssessment: (input: CreateAssessmentInput) =>
    request<AssessmentJob>("/api/v1/assessments", {
      method: "POST",
      body: JSON.stringify(input),
    }),

  getAssessmentJob: (id: string) =>
    request<AssessmentJob>(
      `/api/v1/assessment-jobs/${encodeURIComponent(id)}`,
    ),

  /** How much assessment work is waiting, for an operator. */
  assessmentBacklog: () =>
    request<{
      queued: number;
      running: number;
      failed_last_24h: number;
      oldest_queued_at?: string;
    }>("/api/v1/assessment-jobs"),

  // --- survey events ----------------------------------------------------

  listEvents: (a?: PageArgs, state?: string) =>
    request<Paged<SurveyEvent>>(`/api/v1/events${pageQuery(a, { state })}`),

  getEvent: (id: string) =>
    request<SurveyEvent>(`/api/v1/events/${encodeURIComponent(id)}`),

  createEvent: (input: {
    calamity_type: string;
    title: string;
    description?: string;
    notification_ref?: string;
    incident_start: string;
    incident_end: string;
    survey_closes_at: string;
    area_codes: string[];
    /** AgriStack crop codes. Empty covers every crop. */
    crops?: { crop_code: string; crop_name: string }[];
  }) =>
    request<SurveyEvent>("/api/v1/events", {
      method: "POST",
      body: JSON.stringify(input),
    }),

  transitionEvent: (id: string, state: string, reason?: string) =>
    request<SurveyEvent>(`/api/v1/events/${encodeURIComponent(id)}/state`, {
      method: "POST",
      body: JSON.stringify({ state, reason }),
    }),

  /**
   * Add or remove talukas and circles on an event that is already running.
   *
   * A calamity does not respect the moment a notification was signed: a
   * Collector learns on day three that two more circles were hit. Removal is
   * refused by the server if farmers have already filed from the area, and the
   * refusal names how many.
   */
  editEventAreas: (
    id: string,
    op: "add" | "remove",
    area_codes: string[],
    reason?: string,
  ) =>
    request<SurveyEvent>(
      `/api/v1/events/${encodeURIComponent(id)}/areas/${op}`,
      { method: "POST", body: JSON.stringify({ area_codes, reason }) },
    ),

  calamityTypes: () =>
    request<{ calamity_types: string[] }>("/api/v1/calamity-types"),

  // --- crop loss reports ------------------------------------------------

  listReports: (
    a?: PageArgs,
    params: {
      survey_event_id?: string;
      farmer_id?: string;
      state?: string;
      /** Narrows to one rung, which is what makes this an approval queue. */
      awaiting?: ApprovalLevel;
    } = {},
  ) => request<Paged<ReportSummary>>(`/api/v1/reports${pageQuery(a, params)}`),

  /**
   * Approve a report at your own level, optionally revising the figure.
   *
   * A revision needs a reason; so does any move of a contested report at the
   * Circle. The server refuses without one rather than recording a change
   * nobody explained.
   */
  approveReport: (
    id: string,
    body: { damage_percent?: number; area_affected_ha?: number; reason?: string } = {},
  ) =>
    request<ApprovalRecord>(`/api/v1/reports/${encodeURIComponent(id)}/approve`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  /** Send a report back one level, with a reason. */
  returnReport: (id: string, reason: string) =>
    request<ApprovalRecord>(`/api/v1/reports/${encodeURIComponent(id)}/return`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),

  /** A report's ladder history, oldest first, superseded rungs included. */
  reportApprovals: (id: string) =>
    request<{ approvals: ApprovalRecord[] }>(
      `/api/v1/reports/${encodeURIComponent(id)}/approvals`,
    ),

  /**
   * Approve many at once.
   *
   * Answers 200 with a verdict per report rather than failing: a batch where
   * three of two hundred were contested should approve the other hundred and
   * ninety-seven and say which three fell out and why.
   */
  bulkApprove: (ids: string[], reason?: string) =>
    request<BulkApprovalResult>("/api/v1/reports/approvals/bulk", {
      method: "POST",
      body: JSON.stringify({ report_ids: ids, reason }),
    }),

  getReport: (id: string) =>
    request<CropLossReport>(`/api/v1/reports/${encodeURIComponent(id)}`),

  createReport: (input: Record<string, unknown>) =>
    request<CropLossReport>("/api/v1/reports", {
      method: "POST",
      body: JSON.stringify(input),
    }),

  transitionReport: (id: string, state: string, reason?: string) =>
    request<CropLossReport>(`/api/v1/reports/${encodeURIComponent(id)}/state`, {
      method: "POST",
      body: JSON.stringify({ state, reason }),
    }),

  reportForm: () =>
    request<{
      seasons: string[];
      loss_categories: {
        code: string; mr: string; en: string; calamities: string[];
      }[];
      documents: { code: string; mr: string; en: string }[];
      signature_kinds: string[];
      reported_channels: string[];
      filing_window_hours: number;
    }>("/api/v1/report-form"),

  // The printed form is a page, not JSON — it is opened, not fetched.
  /**
   * The printable report, as HTML.
   *
   * # Why this is a fetch and not a link
   *
   * It was a link — `<a href={reportPrintUrl(id)} target="_blank">` — and it
   * had never worked. The access token lives in memory in this tab; a browser
   * opening a new one sends no Authorization header and gets no cookie, so the
   * endpoint answered 401 and the officer was shown a JSON error object in a
   * fresh tab. The endpoint itself was always fine.
   *
   * Fetched with the token like every other call, which also means it goes
   * through the refresh path: a print at the end of a long session is exactly
   * when the access token has expired.
   */
  reportPrintHtml: (id: string) =>
    printableHtml(`/api/v1/reports/${encodeURIComponent(id)}/print`),

  /**
   * The assessment as a document.
   *
   * A different paper from the report above, and the difference matters: the
   * report is the farmer's filing, this is what the evidence and the officers
   * made of it. Rendered by the server from the evidence packet stored with
   * the assessment, so printing it today and printing it at an appeal next
   * year produce the same page.
   */
  assessmentDossierHtml: (id: string) =>
    printableHtml(`/api/v1/assessments/${encodeURIComponent(id)}/dossier`),

  // --- self-survey evidence ---------------------------------------------

  listCaptures: (reportId: string) =>
    request<{
      captures: Capture[];
      total: number;
      flagged: number;
      gate_explanations: Record<string, string>;
    }>(`/api/v1/reports/${encodeURIComponent(reportId)}/captures`),

  // The photograph itself is fetched by the browser as an image, with the
  // bearer token — so this is a URL, not a request.
  captureMediaUrl: (reportId: string, captureId: string) =>
    `${API_BASE}/api/v1/reports/${encodeURIComponent(reportId)}/captures/${encodeURIComponent(captureId)}/media`,

  parcelBoundary: (farmId: string) =>
    request<{
      boundary: ParcelBoundary | null;
      note?: string;
      geometry?: unknown;
      /** How the boundary came to be, where one was walked. */
      trace?: BoundaryTrace;
    }>(
      `/api/v1/parcels/${encodeURIComponent(farmId)}/boundary`,
    ),

  // --- verification -----------------------------------------------------

  /**
   * What is late, in both senses.
   *
   * `assignments` is a named officer past their seven days on a field visit.
   * `rungs` is a report waiting at the Circle, the Tehsil or the District past
   * that rung's allowance, where nobody is individually assigned.
   *
   * Nothing here moves a report. Being late puts it in two bells — the officer
   * holding the rung and the one above them — and leaves it exactly where it
   * is: a report that escalated on a timer would arrive at the next rung with
   * no finding behind it, and would quietly reward whoever ignored it.
   */
  overdue: (a?: PageArgs) =>
    request<{
      assignments: Paged<WorklistEntry>;
      rungs: WaitingEntry[];
    }>(`/api/v1/overdue${pageQuery(a)}`),

  worklist: (a?: PageArgs, includeCompleted = false) =>
    request<Paged<WorklistEntry> & { overdue: number }>(
      `/api/v1/worklist${pageQuery(a, {
        include_completed: includeCompleted ? "true" : undefined,
      })}`,
    ),

  villageAuthorities: (villageCode: string) =>
    request<VillageAuthorities>(
      `/api/v1/villages/${encodeURIComponent(villageCode)}/authorities`,
    ),

  claimProgress: (id: string) =>
    request<ClaimProgress>(
      `/api/v1/reports/${encodeURIComponent(id)}/progress`,
    ),

  /** Villages whose claims have stopped, worst first. */
  stuckClaims: () =>
    request<{ villages: StuckVillage[] }>(`/api/v1/reports/stuck`),

  /** Release one village's stopped claims, having just fixed what stopped them. */
  retryVillage: (villageCode: string) =>
    request<{ released: number }>(
      `/api/v1/reports/stuck/${encodeURIComponent(villageCode)}/retry`,
      { method: "POST" },
    ),

  verificationBrief: (id: string) =>
    request<VerificationBrief>(
      `/api/v1/reports/${encodeURIComponent(id)}/brief`,
    ),

  verifyReport: (id: string, input: Record<string, unknown>) =>
    request<{ finding: Finding; consensus: Consensus; findings?: Finding[] }>(
      `/api/v1/reports/${encodeURIComponent(id)}/verify`,
      { method: "POST", body: JSON.stringify(input) },
    ),

  /**
   * A revised finding, replacing the caller's own.
   *
   * The earlier one is superseded and kept — an officer who moved after
   * reading their colleague has not erased what they thought before, and a
   * dispute turns on exactly that.
   */
  amendVerification: (id: string, input: Record<string, unknown>) =>
    request<{ finding: Finding; consensus: Consensus; findings: Finding[] }>(
      `/api/v1/reports/${encodeURIComponent(id)}/verify`,
      { method: "PUT", body: JSON.stringify(input) },
    ),

  reportFindings: (id: string) =>
    request<{ findings: Finding[]; consensus: Consensus }>(
      `/api/v1/reports/${encodeURIComponent(id)}/findings`,
    ),

  adjudicateReport: (
    id: string,
    input: {
      adjudicated_percent: number;
      adjudicated_area_ha: number;
      reason: string;
    },
  ) =>
    request<{ adjudication: Adjudication; consensus: Consensus }>(
      `/api/v1/reports/${encodeURIComponent(id)}/adjudicate`,
      { method: "POST", body: JSON.stringify(input) },
    ),

  // --- administration ---------------------------------------------------

  listUsers: (
    a?: PageArgs,
    params: { kind?: string; role?: string; status?: string } = {},
  ) =>
    request<Paged<OfficerAccount>>(`/api/v1/admin/users${pageQuery(a, params)}`),

  /**
   * Create an officer, who is then invited by email to set their own password.
   *
   * There is no password field any more: the administrator who fills this form
   * never sees the credential. `postings` is a list because a Talathi or
   * Krushi Sahayak routinely holds several villages.
   *
   * `mail_configured` in the reply says whether the invitation is actually on
   * its way or sitting in a queue, which is the difference between telling the
   * new officer to check their email and passing them the link by hand.
   */
  createOfficer: (input: {
    full_name: string;
    email: string;
    mobile_number?: string;
    role: string;
    postings: { jurisdiction_level: string; jurisdiction_code: string }[];
  }) =>
    request<{ user: OfficerAccount; mail_configured: boolean }>(
      "/api/v1/admin/users",
      { method: "POST", body: JSON.stringify(input) },
    ),

  grantPosting: (
    userID: string,
    body: { role: string; jurisdiction_level: string; jurisdiction_code: string },
  ) =>
    request<OfficerAccount>(`/api/v1/admin/users/${userID}/roles`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  revokePosting: (
    userID: string,
    body: { role: string; jurisdiction_level: string; jurisdiction_code: string },
  ) =>
    request<OfficerAccount>(`/api/v1/admin/users/${userID}/roles/revoke`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  resendInvite: (userID: string) =>
    request<{ status: string; mail_configured: boolean }>(
      `/api/v1/admin/users/${userID}/invite`,
      { method: "POST" },
    ),

  setUserStatus: (id: string, status: string, reason?: string) =>
    request<OfficerAccount>(
      `/api/v1/admin/users/${encodeURIComponent(id)}/status`,
      { method: "POST", body: JSON.stringify({ status, reason }) },
    ),

  /**
   * One page of a node's children.
   *
   * `level` matters: a taluka's children are its villages -- 327 in the
   * largest -- so a picker choosing between talukas and circles asks for the
   * level it wants rather than fetching a page of villages and discarding it.
   *
   * Accepts the old positional parentId so existing callers keep working.
   */
  listJurisdictions: (
    opts?:
      | string
      | {
          parent_id?: string;
          level?: "state" | "district" | "sub_district" | "circle" | "village";
          q?: string;
          limit?: number;
          offset?: number;
        },
  ) => {
    const o = typeof opts === "string" ? { parent_id: opts } : (opts ?? {});
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(o)) {
      if (v !== undefined && v !== "") p.set(k, String(v));
    }
    const qs = p.toString();
    return request<{
      jurisdictions: Jurisdiction[];
      items: Jurisdiction[];
      total: number;
      total_is_lower_bound?: boolean;
      has_more: boolean;
    }>(`/api/v1/jurisdictions${qs ? `?${qs}` : ""}`);
  },

  roles: () =>
    request<{
      roles: {
        role: string; permissions: string[]; assignable: boolean; posted_at: string;
      }[];
    }>("/api/v1/roles"),

  // --- the boundary a farmer walked ---------------------------------------

  traceBoundary: (
    farmId: string,
    points: { lat: number; lon: number; accuracy_m: number; at: string }[],
    farmerId?: string,
  ) =>
    request<{ boundary: ParcelBoundary; trace: BoundaryTrace; message: string }>(
      `/api/v1/parcels/${encodeURIComponent(farmId)}/boundary${
        farmerId ? `?farmer_id=${encodeURIComponent(farmerId)}` : ""
      }`,
      { method: "POST", body: JSON.stringify({ points }) },
    ),

  confirmBoundary: (farmId: string) =>
    request<{ confirmed: boolean; message: string }>(
      `/api/v1/parcels/${encodeURIComponent(farmId)}/boundary/confirm`,
      { method: "POST" },
    ),

  // --- what an officer counted in the field -------------------------------

  listMeasurements: (reportId: string) =>
    request<{
      items: FieldMeasurement[];
      by_line: Record<string, FieldMeasurementSummary>;
      soil_types: { Key: string; Mr: string; En: string }[];
      enabled: boolean;
    }>(`/api/v1/reports/${encodeURIComponent(reportId)}/measurements`),

  recordMeasurement: (reportId: string, input: MeasurementInput) =>
    request<{
      id: string;
      derived: FieldMeasurement["derived"];
      line_summary?: FieldMeasurementSummary;
      quadrats_on_this_line: number;
    }>(`/api/v1/reports/${encodeURIComponent(reportId)}/measurements`, {
      method: "POST",
      body: JSON.stringify(input),
    }),

  expectedStand: (rowCm: number, plantCm: number, quadratM: number) =>
    request<{ plants_expected: number; note: string }>(
      `/api/v1/expected-stand?row_cm=${rowCm}&plant_cm=${plantCm}&quadrat_m=${quadratM}`,
    ),

  audit: (
    a?: PageArgs,
    params: { action?: string; outcome?: string; resource_id?: string } = {},
  ) => request<Paged<AuditEntry>>(`/api/v1/admin/audit${pageQuery(a, params)}`),

  // --- the officer's assistant -------------------------------------------
  //
  // Scoped by the caller's own posting on the server, through the same store
  // calls the listing uses, so there is deliberately nothing here to pass a
  // jurisdiction to.
  askAssistant: (
    question: string,
    history: AssistantMessage[] = [],
    files: File[] = [],
  ) => {
    // No files is the request it has always been, byte for byte.
    //
    // Deliberate rather than tidy: the overwhelming majority of questions carry
    // nothing, and sending them as multipart would put every one of them through a
    // parser it does not need and raise the body cap for all of them.
    if (files.length === 0) {
      return request<AssistantAnswer>("/api/v1/officer/agent/ask", {
        method: "POST",
        body: JSON.stringify({ question, history }),
      });
    }

    // With files, multipart -- base64 in JSON would cost a third on the way up, and
    // the way up is a district office's line.
    const form = new FormData();
    form.append("question", question);
    // History goes as the same JSON array, and carries no attachments. The server
    // strips them from history in either shape, because an attachment belongs to the
    // question being asked now and a file replayed through history would be a file
    // past the size cap, the sniffer and the audit log.
    form.append("history", JSON.stringify(history));
    for (const f of files) form.append("file", f);

    return request<AssistantAnswer>("/api/v1/officer/agent/ask", {
      method: "POST",
      body: form,
    });
  },

  // --- asking it out loud -------------------------------------------------
  //
  // Permission to dictate one question. The relay writes down what was said and
  // the console posts that text to askAssistant above, so a spoken question and a
  // typed one are the same request by the time anything acts on them — which is
  // the point, because every guardrail the assistant has is on that route.
  //
  // Nothing about a claim is sent. The session transcribes speech and is told
  // nothing else; the platform strips the socket's query string down to the ticket
  // and the mode on the way to the relay.
  startOfficerVoice: () =>
    request<OfficerVoiceGrant>("/api/v1/officer/live/session", {
      method: "POST",
    }),

  /**
   * The socket the microphone goes down, for the grant just issued.
   *
   * A URL and not a request, because the browser opens it. The mode is passed
   * through from the grant rather than written here: the platform decides that an
   * officer's session only listens, and a console that named the mode itself would
   * be a console that could ask for one that answers.
   */
  officerVoiceSocketUrl: (grant: OfficerVoiceGrant) =>
    `${socketBase()}/api/v1/officer/live/relay` +
    `?ticket=${encodeURIComponent(grant.ticket)}` +
    `&mode=${encodeURIComponent(grant.mode)}`,

  // An officer's own report format, returned with their district's figures in
  // it. Only the column headings reach the model; the values are written by the
  // server.
  fillReportFormat: async (
    file: File,
    filters: { event_id?: string; state?: string; limit?: number } = {},
  ): Promise<{ blob: Blob; filename: string; columnsFilled: number; rows: number }> => {
    const form = new FormData();
    form.append("format", file);
    for (const [k, v] of Object.entries(filters)) {
      if (v !== undefined && v !== "") form.append(k, String(v));
    }
    const res = await rawRequest("/api/v1/officer/agent/fill-format", {
      method: "POST",
      body: form,
    });
    const disposition = res.headers.get("content-disposition") ?? "";
    const named = /filename="?([^"]+)"?/.exec(disposition);
    return {
      blob: await res.blob(),
      filename: named?.[1] ?? file.name.replace(/\.xlsx$/i, "") + " (filled).xlsx",
      columnsFilled: Number(res.headers.get("x-esurvey-columns-filled") ?? 0),
      rows: Number(res.headers.get("x-esurvey-rows") ?? 0),
    };
  },

  // The same figures, in whichever format the officer asked for.
  //
  // Distinct from fillReportFormat above, and the difference is whose document it
  // is. That one takes the office's own format and writes into it, so the result is
  // their layout with our figures; this one produces a document from scratch, which
  // is what somebody wants when they have no format to hand and just need the data
  // in a shape they can use.
  //
  // Nothing about a claim is sent. The request carries a format, a choice of
  // columns from the server's own closed vocabulary, and the same filters the claim
  // list already takes -- so there is no parameter through which a claim the officer
  // cannot already read could arrive.
  // Fetch a document the assistant prepared during a conversation.
  //
  // Downloadable more than once on purpose: a few hundred kilobytes over a taluka
  // line gets interrupted and retried, antivirus fetches before the browser does,
  // and officers double-click. In each case the second request is the one that
  // matters, and a one-shot download would turn it into "that export is gone".
  preparedExport: async (
    id: string,
  ): Promise<{ blob: Blob; filename: string }> => {
    const res = await rawRequest(
      `/api/v1/officer/agent/export/${encodeURIComponent(id)}`,
    );
    return { blob: await res.blob(), filename: filenameFrom(res, "claims") };
  },

  exportAs: async (
    req: ExportRequest,
  ): Promise<{ blob: Blob; filename: string; rows: number; format: string }> => {
    const res = await rawRequest("/api/v1/officer/agent/export", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
    });
    return {
      blob: await res.blob(),
      filename: filenameFrom(res, `claims.${req.format}`),
      rows: Number(res.headers.get("x-esurvey-rows") ?? 0),
      format: res.headers.get("x-esurvey-format") ?? req.format,
    };
  },
};

/// The name the server gave the file, preferring the one that survives Marathi.
///
/// Content-Disposition carries two: a plain ASCII filename for software that
/// predates the extension, and filename*=UTF-8''… which is the real one. Reading
/// only the plain form turns "बार्शी तालुका.xlsx" into a row of underscores, which
/// is the name the officer then has to live with in their downloads folder.
function filenameFrom(res: Response, fallback: string): string {
  const disposition = res.headers.get("content-disposition") ?? "";
  const utf8 = /filename\*=UTF-8''([^;]+)/i.exec(disposition);
  if (utf8) {
    try {
      return decodeURIComponent(utf8[1]);
    } catch {
      // A malformed encoding is not worth failing a download over.
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(disposition);
  return plain?.[1] ?? fallback;
}

/** One turn of a conversation with the assistant. */
/// The formats the assistant can produce, as the server names them.
///
/// Kept as a union rather than a string so a typo is a compile error here instead of
/// a 400 an officer meets after waiting for a document. The server refuses anything
/// outside its own list and names what it supports, so the two cannot silently
/// disagree about what exists -- but there is no reason to find that out at runtime.
export type ExportFormat = "xlsx" | "csv" | "pdf" | "docx" | "pptx";

/// What an export asks for.
///
/// Deliberately thin. There is no field here for a claim, a row or a scope, because
/// the server resolves the rows itself from the caller's own posting -- so an export
/// cannot be made to contain a claim the officer could not already open. The filters
/// are the same three the claim list takes, which is how an officer narrows a
/// thousand-row refusal into the statement they actually wanted.
export interface ExportRequest {
  format: ExportFormat;
  /// Column names from the server's vocabulary. Omitted means its own default set.
  fields?: string[];
  title?: string;
  /// A line the assistant drafted, shown above the table. Carried only by the
  /// formats that have somewhere to put prose: a sentence above a header row breaks
  /// every tool that reads a spreadsheet.
  note?: string;
  event_id?: string;
  state?: string;
  awaiting_level?: string;
  search?: string;
}

/// A document the conversation prepared, waiting to be downloaded.
///
/// The id is not a capability. It is composed from the officer's own account, the
/// route wants their session, and another officer holding it gets a 404 rather than
/// the file -- which is why it is safe for this to sit in a page.
export interface PreparedExport {
  id: string;
  filename: string;
  format: ExportFormat;
  rows: number;
  bytes: number;
}

export interface AssistantMessage {
  role: "user" | "assistant" | "tool";
  text?: string;
  tool_name?: string;
  tool_result?: unknown;
}

/** What the assistant did to answer, named tool by tool. */
export interface AssistantStep {
  tool: string;
  summary?: string;
  failed?: string;
  duration_ms: number;
}

/**
 * An action the assistant suggests.
 *
 * Never performed by it. The console renders this beside a button that calls the
 * ordinary endpoint, so the action is taken by the officer through the path that
 * records who took it.
 */
export interface AssistantProposal {
  action: string;
  report_id?: string;
  reason: string;
}

/**
 * Permission to speak one question, and for how long.
 *
 * Carries no URL. The console already knows the one origin it talks to, and a second
 * copy of a base address in a browser bundle is how a production build ends up
 * pointed somewhere else — see socketBase.
 */
export interface OfficerVoiceGrant {
  /** The signed permission the relay checks. It names nobody. */
  ticket: string;
  max_seconds: number;
  /** The relay's name for a session that only listens. */
  mode: string;
}

export interface AssistantAnswer {
  text: string;
  /**
   * What was consulted, shown rather than hidden.
   *
   * An officer acting on a figure needs to know where it came from, and "I read
   * these four claims" is the difference between an answer they can check and one
   * they must take on trust.
   */
  steps: AssistantStep[];
  /**
   * Documents the conversation prepared, in the order prepared.
   *
   * Absent when it prepared none, which is most answers. A conversation that
   * corrected itself leaves more than one -- "make that Excel" prepares a second
   * rather than replacing the first -- so the last is the one the officer meant and
   * the earlier ones are the ones they abandoned.
   */
  prepared_exports?: PreparedExport[];
  proposed?: AssistantProposal;
  turns: number;
}

/** One quadrat, as the form submits it. */
export interface MeasurementInput {
  line_no?: number;
  quadrat_side_m: number;
  plants_expected?: number;
  plants_present?: number;
  plants_lodged?: number;
  organ_sampled?: number;
  organ_damaged?: number;
  soil_type?: string;
  notes?: string;
  capture_id?: string;
}

// --- product administrator ---------------------------------------------------

export interface ClaimStageCounts {
  received: number;
  checking: number;
  with_officers: number;
  decided: number;
  closed: number;
  withdrawn: number;
  total: number;
}

export interface PlatformOverview {
  generated_at: string;
  states: (ClaimStageCounts & { state_code: string; state_name: string })[];
  districts: (ClaimStageCounts & {
    state_code: string;
    district_code: string;
    district_name: string;
  })[];
  accounts: {
    state_code: string;
    state_name: string;
    by_level: Record<string, number>;
    active: number;
    pending_invites: number;
  }[];
  trend: { day: string; state_code: string; filed: number }[];
}

export interface HealthComponent {
  key: string;
  name: string;
  status: "up" | "degraded" | "down" | "unconfigured";
  latency_ms: number;
  detail?: string;
}

export interface LoadPoint {
  at: string;
  requests: number;
  errors: number;
  avg_ms: number;
  max_ms: number;
}

/** One of the API instance's connection pools. */
export interface DbPool {
  name: string;
  configured: boolean;
  max?: number;
  in_use?: number;
  idle?: number;
  waits?: number;
  wait_ms?: number;
}

/** Where staleness-tolerant reads are going, and why. */
export interface ReplicaStatus {
  replica_configured: boolean;
  reading_from: "replica" | "primary";
  lag_ms: number;
  max_lag_ms: number;
  reason?: string;
  checked_at?: string;
}

/** One upstream's concurrency limit, counted across every API instance. */
export interface GateStats {
  name: string;
  limit: number;
  /** Slots held across every instance; -1 when the shared count could not be read. */
  in_use: number;
  held_here: number;
  waiting_here: number;
  avg_wait_ms: number;
  acquired: number;
  /** False while the gate is counting this instance only (Redis unavailable). */
  shared: boolean;
}

export interface PlatformHealth {
  checked_at: string;
  env: string;
  components: HealthComponent[];
  database: {
    pool_max: number;
    pool_total: number;
    pool_in_use: number;
    pool_idle: number;
    pool_waits: number;
    pool_wait_ms: number;
    server_active?: number;
    server_connections?: number;
    size_bytes?: number;
    pools?: DbPool[];
    replica?: ReplicaStatus;
  };
  queues: {
    assessments?: { queued: number; running: number; failed: number; oldest?: string | null };
    sms?: { queued: number; failed: number; abandoned: number; sent: number; provider: string };
    email_pending?: number;
    claims_stuck_in_pipeline?: number;
    claims_waiting_on_assessment?: number;
    oldest_receipt_owed_seconds?: number;
    assessment_throughput?: {
      done_last_hour: number;
      failed_last_hour: number;
      run_p50_ms: number;
      run_p95_ms: number;
      per_day_at_this_rate: number;
    };
  };
  gates?: GateStats[];
  runtime: {
    goroutines: number;
    cpus: number;
    heap_in_use: number;
    memory_from_os: number;
    gc_cycles: number;
    go_version: string;
  };
  load?: {
    window_seconds: number;
    rps_1m: number;
    rps_5m: number;
    error_rate_5m: number;
    avg_ms_1m: number;
    max_ms_5m: number;
    requests_5m: number;
    uptime_seconds: number;
    series: LoadPoint[];
  };
}
