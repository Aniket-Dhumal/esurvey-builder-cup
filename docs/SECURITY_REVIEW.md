# Security and consistency review, 2026-09-17

A sweep for gaps, inconsistencies and dead paths, then an end-to-end security
pass. Every finding below is fixed and carries a test that fails without the
fix.

The recurring shape of the defects is worth naming, because it is the one to
keep looking for: **something implemented, wired, tested, and reachable from
nothing.** Four of the findings are that exact shape. A permission check, a
button, a column and a wipe, each written correctly and each connected to no
caller.

---

## Findings

### 1. The appeal was a dead end (critical)

An officer could *take* a grievance and there was no way to *decide* one.
`api.decideGrievance` was implemented, routed, server-tested and called from
no screen. So every appeal a farmer filed could be assigned and then never
answered — on the one route a farmer has to contest a decision.

**Fixed:** `DecideAppealDialog`, with the three outcomes the server accepts and
the revised figure appearing only where the server requires one.

### 2. Any farmer could read any parcel's position (critical)

`parcel:read` is held by every farmer and carries no jurisdiction, so
`GET /parcels/{farmID}/boundary` and `/stations` returned the outline and
sampling coordinates of **any** parcel to **any** signed-in farmer. A farm id
is an AgriStack identifier printed on documents, so "you need the id" was never
a control. The handler said so in a comment and deferred it.

**Fixed:** `mayReadParcel`, applying the rule the write path already used — a
farmer reaches the parcels of the farmers they may act for, an officer reaches
the villages they are posted to. Refusals answer 404, so the endpoint cannot
confirm which farm ids exist.

### 3. Automating the receipt silently removed it (high, self-inflicted)

The claim pipeline acknowledged claims with a raw UPDATE, bypassing
`Transition` — and with it `notify.EventReportAcknowledged`, "the one message a
farmer is owed by the act of filing". Automating the acknowledgement to make
the receipt arrive *sooner* had removed it.

**Fixed:** the pipeline queues the same message in the same transaction as the
state it announces.

### 4. A second farmer could file a claim and never upload its evidence (high, self-inflicted)

Multi-farmer support taught filing and listing to follow the grant, and left
every route that resolves a claim *by id* using the raw scope — which is the
account's own farmer and nobody else's. The symptom is not a refusal anybody
could understand: the claim is filed, the field is walked, and every photograph
comes back "no such report", from a field with no signal.

**Fixed:** `actingScope`, used by every farmer-reachable report route, and the
grant threaded through the app's multipart and chunked uploads.

### 5. The borrowed handset kept the previous household (high, self-inflicted)

The `farmers` table — names, Farmer IDs and grant tokens valid ninety days —
was wiped only by `clearPersonalData`, which the app calls from nowhere. The
test that covered it called the method directly, so it passed.

**Fixed:** the list is purged with its owner's rows in `purgeOwnerIfDone`, which
is the path sign-out actually uses. A farmer with work still sending keeps
their grant, because the queue needs it.

### 6. Metrics were readable by anyone who could reach the port (medium)

`/metrics` was unauthenticated. Its labels are route patterns and never paths,
so no farmer data leaked — but how many requests a district handles, which
routes fail and how slowly is reconnaissance.

**Fixed:** a token when one is configured, loopback otherwise, and the check
reads the connection's address rather than `X-Forwarded-For` — an address a
caller supplies is not an address. Behind a proxy that means a token is
required, which is the intended consequence.

### 7. Officers could not amend a verification cross-origin (medium)

CORS allowed `GET, POST, OPTIONS`. `amendVerification` is a **PUT** — an officer
revising their finding after reading their colleague's. The browser refused it
at the preflight. No handler test could see this: handler tests call the
handler, and the browser never got that far.

**Fixed:** PUT allowed, and the headers this API defined for itself
(`Idempotency-Key`, `X-App-Version`, `X-Farmer-Grant`) permitted.

### 8. Nothing could ever be closed (medium)

The state machine has always allowed `verified` and `adjudicated` to reach
`closed`, and nothing anywhere ever wrote it. Every decided claim was still
open, `TerminalStates` described a state nothing reached, and the farmer's
progress screen ended on a step that could not complete.

**Fixed:** a close action for officers holding `report:review`. The progress
model now shows that last stage as pending rather than perpetually in progress
— closing is an administrative act, not a consequence, and a decided claim may
still be appealed.

### 9. Staffing a village then waited half an hour (low)

The pipeline's backoff reaches thirty minutes, which is right for a bad
afternoon and wrong for the moment a District has just posted the officer those
claims were waiting for.

**Fixed:** `POST /reports/stuck/{village}/retry`, on the card that reports the
problem. The dead `allocateReport` client method — superseded by the pipeline —
was removed rather than left as a second writer for one step.

---

## What was examined and found sound

- **SQL injection.** Every query is parameterised; the only strings
  concatenated into SQL are constants (column lists, scope predicates).
- **Path traversal.** `blob.Dir.path` rejects separators, absolute paths and
  `.`/`..` segments, then re-checks containment under the root.
- **XSS.** No `dangerouslySetInnerHTML` or `innerHTML` anywhere in the console.
- **Credential stuffing.** Officer login and farmer OTP both check a throttle
  *before* verifying, count failures per identifier and per address, and reset
  on success.
- **One-time codes in logs.** The log sender exists for development and the
  config loader refuses to start a hardened environment with it selected.
- **Raw Aadhaar.** Not stored anywhere; hash and last four only.
- **Security headers.** `nosniff`, `DENY`, `no-referrer`, `no-store`, a CSP of
  `default-src 'none'`, and HSTS only on genuinely TLS connections.
- **CORS origins.** An explicit allowlist with `Vary: Origin`, never a
  wildcard, credentials only for listed origins.
- **Metrics labels.** Route patterns, never paths, so no report id has ever
  reached the exposition.

### 10. Pipeline assessments were readable by anybody (high, self-inflicted)

`GET /assessment-jobs/{id}` skipped the jurisdiction check entirely for a job
with no `village_lgd_code` — and the claim pipeline queued every job without
one. Farmers hold `assessment:read`.

**Fixed twice over.** The pipeline now records the village it already knew, and
the read fails closed: a job this server cannot place is in nobody's
jurisdiction, so only the account that requested it may read it.

### 11. A grant had to be repeated in a query parameter (medium, self-inflicted)

`actingFarmer` read the farmer from `?farmer_id=` and used the grant only to
authorise it. Every call site therefore had to send both, and the one that sent
only the header acted as the account's own farmer — resolving somebody else's
claim to "not found" after the field had been walked.

**Fixed:** a grant names exactly one farmer, so presenting one now says which
farmer the request is about. An unusable grant falls back to the account's own
farmer rather than failing the request.

---

## Verification

942 Go tests, 332 Flutter, 140 ai-services, 59 satellite-intelligence — 0
failures. `gofmt`, `go vet`, `flutter analyze`, `tsc`, `eslint` and `ruff`
clean. Debug APK builds. The 21 Go skips are the `ESURVEY_SCALE` probes and
cloud-only tests.

Every finding above has a test that fails without its fix. The ones worth
naming, because they pin a rule rather than a behaviour:

- `TestAFarmerIsNotToldWhereSomebodyElsesParcelIs`
- `TestAForwardedHeaderCannotClaimToBeLoopback`
- `TestEveryMethodTheConsoleUsesIsAllowed` — the CORS contract, which no
  handler test can reach
- `TestAutomaticAcknowledgementStillSendsTheReceipt`
- `TestTheActingScopeFollowsTheGrant`
- `TestAnOfficersScopeIsNotTouchedByAFarmerIdInTheQuery`
- `a departing farmer takes their list off the phone`
