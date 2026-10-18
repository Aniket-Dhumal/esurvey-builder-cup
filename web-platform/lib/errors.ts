/**
 * What a person is told when something fails.
 *
 * # Why the API's own wording is not used
 *
 * The console used to show `error.message` from the API verbatim. That was a
 * reasonable-sounding decision — the API's messages are written prose, not
 * codes — and it was wrong for two reasons.
 *
 * The API is an interface for programs, so its wording names the things a
 * program sends: "farmer_id is required", "refresh_token is required",
 * "attach the photograph as the 'image' field", "the access token has expired;
 * refresh it". An officer has never seen a field called farmer_id and cannot
 * refresh a token. Worse, anything the API failed to sanitise arrived intact:
 * a malformed request body produced "json: cannot unmarshal number into Go
 * struct field officerLoginRequest.username of type string" as the headline of
 * a red banner.
 *
 * So the API keeps its wording, which is right for its audience, and this file
 * holds a second wording for ours. The console never echoes the server.
 *
 * # The shape of a good failure message
 *
 * Say what happened, in the second person, and say what to do next. A message
 * that only names the fault ("Invalid state") leaves the reader with nowhere to
 * go. Where the remedy needs more than a sentence it goes in `hintFor`, which
 * the error panel prints underneath.
 *
 * # Why there is a fallback by status
 *
 * The API emits 122 distinct codes and will emit more. Enumerating them here
 * would rot, and a missing entry must not degrade to something useless, so
 * anything unrecognised falls back on the HTTP status — which always carries
 * enough meaning for one honest sentence.
 */

const MESSAGES: Record<string, string> = {
  // Reaching the API at all.
  NETWORK: "Cannot reach the eSurvey service. Check your connection and try again.",

  // Signing in.
  INVALID_CREDENTIALS: "That email address or password is incorrect.",
  INVALID_EMAIL: "Enter a valid email address — officers sign in with their email.",
  LOCKED_OUT: "Too many failed attempts. This account is locked for a short while.",
  ACCOUNT_INACTIVE: "This account is no longer active. Ask an administrator to restore it.",
  PASSWORD_CHANGE_REQUIRED: "You need to set your own password before continuing.",
  WEAK_PASSWORD: "That password is too easy to guess. Choose a longer one.",
  AUTH_REQUIRED: "Sign in to continue.",
  TOKEN_EXPIRED: "Your session has ended. Sign in again to continue.",
  TOKEN_INVALID: "Your session is no longer valid. Sign in again to continue.",
  SESSION_REVOKED: "This session was ended by an administrator. Sign in again.",
  REFRESH_INVALID: "Your session has ended. Sign in again to continue.",
  BAD_AUTH_SCHEME: "Your session could not be read. Sign in again to continue.",

  // One-time codes.
  OTP_INVALID: "That code is not correct. Request a new one.",
  OTP_COOLDOWN: "A code was sent moments ago. Wait before asking for another.",
  OTP_ATTEMPTS: "Too many incorrect codes. Request a new one.",
  OTP_DAILY_CAP: "Too many codes requested today. Try again tomorrow.",
  OFFICER_HANDSET:
    "That number belongs to an officer account. Sign in above with your email address and password instead.",
  SMS_UNAVAILABLE:
    "The code could not be sent just now. Wait a couple of minutes and ask for it again.",
  SMS_UNDELIVERABLE:
    "Codes cannot be sent at the moment. This is a fault in the message gateway, not with the number.",
  MOBILE_TAKEN:
    "Another account already signs in with that number. Use a handset of your own.",
  NOT_AN_OFFICER:
    "A farmer's handset comes from the registry and cannot be changed here.",
  CHALLENGE_INVALID:
    "That took too long, or the code was already used. Start again.",
  HANDSET_TAKEN:
    "That mobile number is already signed in as a different farmer. The registry's number for this farmer has to be corrected first.",
  NO_USABLE_MOBILE: "No mobile number is on record for this farmer.",

  // Permission and jurisdiction.
  FORBIDDEN: "You do not have permission to do that.",
  OUT_OF_JURISDICTION: "This record is outside the area you are posted to.",
  FARMER_ONLY: "Only the farmer this belongs to can do that.",
  NOT_YOUR_PARCEL: "This parcel is registered to another farmer.",
  PARCEL_NOT_OWNED: "This parcel is registered to another farmer.",
  NOT_YOUR_REPORT: "This report belongs to another farmer.",
  NOT_THE_ADJUDICATOR: "This case is assigned to another officer.",
  SELF_REVIEW: "You cannot review your own work. Record your finding first.",
  SELF_LOCKOUT: "An account cannot suspend itself. Ask another administrator.",
  ROLE_NOT_ASSIGNABLE: "You cannot grant a role above your own.",
  NO_DISTRICT_POSTING: "You are not posted to a district, so this list is empty.",
  NO_VILLAGE_AUTHORITY: "You are not recorded as a village authority for this area.",

  // Things that are not there.
  NOT_FOUND: "That record could not be found.",
  FARMER_NOT_FOUND: "No farmer is registered with that ID.",
  PARCEL_NOT_FOUND: "That parcel is not on this farmer's record.",
  EVENT_NOT_FOUND: "That survey event no longer exists.",
  REPORT_NOT_FOUND: "That crop loss report no longer exists.",
  ASSESSMENT_NOT_FOUND: "That assessment no longer exists.",
  GRIEVANCE_NOT_FOUND: "That grievance no longer exists.",
  CAPTURE_NOT_FOUND: "That photograph is not on this report.",
  UPLOAD_NOT_FOUND: "That upload has expired. Start it again.",

  // State the record is in.
  CONFLICT: "Someone else changed this while you were working. Reload and try again.",
  INVALID_STATE: "This record cannot be changed at its current stage.",
  ALREADY_RECORDED: "That has already been recorded.",
  GRIEVANCE_ALREADY_OPEN: "A grievance is already open on this report.",
  GRIEVANCE_STATE: "This grievance cannot be changed at its current stage.",
  NOT_APPEALABLE: "This decision is not open to appeal.",
  NOT_ELIGIBLE: "This report does not meet the conditions for that step.",
  NOT_ALLOCATED: "This case has not been assigned to anyone yet.",
  PAIR_INCOMPLETE: "Both village authorities must record a finding first.",
  BLIND_UNTIL_RECORDED: "Record your own finding before you can see the other one.",

  // What the field work produced.
  NO_GEOMETRY: "This parcel has no recorded boundary.",
  NO_BOUNDARY: "This parcel has no recorded boundary.",
  NO_TRACED_BOUNDARY: "Nobody has walked the edge of this parcel yet.",
  NO_PARCEL: "No parcel is on this farmer's record.",
  NO_OBSERVATIONS: "No usable satellite image was found for these dates.",
  NO_CALIBRATION: "This deployment has no calibration, so a score cannot be given.",
  MISSING_SOWING_DATE: "A sowing date is needed before this can be assessed.",
  NOTHING_MEASURED: "Record a plant count, an organ sample, or both.",
  UNKNOWN_SOIL_TYPE: "Choose a soil type from the list.",
  MOCKED_POSITION: "This position came from a mock location and cannot be accepted.",
  POSITION_TOO_COARSE: "The position is not accurate enough. Wait for a better signal.",
  OUTSIDE_PARCEL: "You are standing outside this parcel.",
  NOT_IN_EVENT_AREA: "This parcel is outside the area of the survey event.",
  MISSING_POSITION: "A position is needed, and the handset did not supply one.",
  MISSING_IMAGE: "Attach a photograph before submitting.",
  UNUSABLE_MEDIA: "That file could not be read as a photograph.",
  BAD_MEDIA: "That file could not be read as a photograph.",
  BAD_BOUNDARY: "That boundary could not be read.",
  BAD_QUADRAT: "Those quadrat measurements are not usable.",
  BAD_SPACING: "The row spacing given is not usable.",
  INVALID_AREA: "That area is not a usable number.",
  INVALID_CALAMITY: "Choose a calamity type from the list.",
  INVALID_STATUS: "Choose a status from the list.",

  // Uploads.
  UPLOAD_TOO_LARGE: "That file is too large to upload.",
  TOO_LARGE: "That file is too large to upload.",
  TOO_MANY_UPLOADS: "Too many uploads at once. Wait for the current ones to finish.",
  UPLOAD_CORRUPT: "The upload did not arrive intact. Try sending it again.",
  BAD_CHUNK_INDEX: "The upload lost its place. Start it again.",
  RATE_LIMITED: "Too many requests. Wait a moment and try again.",

  // Services this depends on.
  SATELLITE_UNAVAILABLE: "The satellite service is not responding. Try again shortly.",
  SATELLITE_DOWN: "The satellite service is not responding. Try again shortly.",
  AI_UNAVAILABLE: "The assessment service is not responding. Try again shortly.",
  AI_UNREACHABLE: "The assessment service is not responding. Try again shortly.",
  IDENTITY_UNAVAILABLE: "The account service is not responding. Try again shortly.",
  RETENTION_UNAVAILABLE: "The retention service is not responding. Try again shortly.",
  RETENTION_NOT_ENABLED: "Record deletion is not switched on for this deployment.",
  REGISTRY_UNSUPPORTED: "The farmer registry does not offer this.",
  STATIONS_UNAVAILABLE: "Sampling points could not be worked out for this parcel.",
  NO_MEASUREMENT_STORE: "Field measurements are not switched on for this deployment.",
  NO_DATABASE: "The service is starting up. Try again in a moment.",
};

/**
 * Longer context, printed under the message.
 *
 * Only where the one-line message genuinely leaves the reader stuck. These say
 * why the refusal is the correct outcome rather than a fault to work around —
 * which matters most for the two that look like bugs and are not.
 */
const HINTS: Record<string, string> = {
  NO_GEOMETRY:
    "The registry holds no boundary for this parcel. Someone has to walk its edge in the app before it can be assessed.",
  NO_TRACED_BOUNDARY:
    "The registry holds no boundary for this parcel. Someone has to walk its edge in the app before it can be assessed.",
  NO_OBSERVATIONS:
    "Every satellite pass in this window was under cloud. That is missing evidence, not evidence of no damage, so the assessment is refused rather than scored as zero.",
  MISSING_SOWING_DATE:
    "The parcel carries no crop declaration, so the growth stage cannot be worked out. Enter the sowing date to continue.",
  BLIND_UNTIL_RECORDED:
    "The two village authorities record their findings independently, so neither can see the other's until both are in.",
  NETWORK:
    "The console reached no answer at all. This is usually a connection problem rather than anything wrong with your account.",
};

/** One clear sentence per HTTP status, for codes not in the table. */
function fallbackFor(status: number): string {
  if (status === 0) return MESSAGES.NETWORK;
  if (status === 401) return "Your session has ended. Sign in again to continue.";
  if (status === 403) return "You do not have permission to do that.";
  if (status === 404) return "That record could not be found.";
  if (status === 409) return "Someone else changed this while you were working. Reload and try again.";
  if (status === 413) return "That file is too large to upload.";
  if (status === 429) return "Too many attempts. Wait a moment and try again.";
  if (status === 502 || status === 503 || status === 504)
    return "A service this depends on is not responding. Try again shortly.";
  if (status >= 500) return "Something went wrong at our end. Try again in a moment.";
  if (status >= 400) return "Some of the details sent were not valid. Check them and try again.";
  return "That request could not be completed.";
}

/** What to show a person for a given API failure. */
export function messageFor(status: number, code: string): string {
  return MESSAGES[code] ?? fallbackFor(status);
}

/**
 * The sentence to show for anything a call threw.
 *
 * Every screen that catches an API failure needs this, and it was written
 * twice before: once in SignIn and once wherever the next screen needed it.
 * An ApiError already carries the sentence messageFor derived from its code,
 * so this is only about the things that are not one -- a dropped connection,
 * a bug -- which must not reach a person as "undefined".
 *
 * Structural rather than `instanceof ApiError`: lib/api imports this file, and
 * importing it back would be a cycle.
 */
export function messageForError(err: unknown): string {
  if (err && typeof err === "object" && "code" in err) {
    const { message } = err as { message?: unknown };
    if (typeof message === "string" && message !== "") return message;
  }
  return "Something went wrong. Try again.";
}

/** The extra paragraph, where one earns its place. */
export function hintFor(code?: string): string | undefined {
  return code ? HINTS[code] : undefined;
}
