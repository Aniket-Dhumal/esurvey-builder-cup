# Security assessment

Conducted 2026-08-31 against the full stack: secure code review, dependency
analysis, OWASP Top 10 (2021) mapping, and live penetration testing of a
running deployment.

## On "bulletproof"

No system is proof against any cyber risk, and a document claiming otherwise
would be the least trustworthy thing in this repository. What follows is
narrower and more useful: twenty-one defects found and fixed across seven
passes, with every finding closed and none left open, a
list of what was tested and held, and the residual risks that remain — each
with the reason it remains.

Two further passes went back at the residual risks and closed every one that
could be closed in code — plaintext transport, audit tamper-evidence,
certificate pinning, erasure on request, the dependency invariant and the SDK
pins. What remains is stated at the end, along with why it is not closeable.

---

## Fixed

### 1. Privilege escalation: uploading evidence bypassed the permission to create it — HIGH

The resumable upload routes were registered behind `requireAuth`, while
`POST /reports/{id}/captures` required `survey:capture`. Both end in a row in
`survey_captures`. The upload path was therefore a second way to create
evidence, without the permission that exists to control exactly that.

It mattered because of who holds what. `RoleTehsildar`,
`RoleDistrictMagistrate` and `RoleStateOfficer` hold `survey:read` and
`report:read` but deliberately **not** `survey:capture` — they adjudicate. An
adjudicator who can manufacture evidence on a claim they then decide is the
precise thing that separation of duties exists to prevent, and the scope check
on the route would have admitted them for every report in their jurisdiction.

**Fixed:** all three upload routes now require `survey:capture`, matching
`POST /captures`. The stations route requires `survey:read`.

**Verified live:** a real Tehsildar session receives 403 on
`POST /reports/{id}/uploads`, identical to `POST /captures`.

**Regression:** `internal/api/route_guards_test.go` parses the route table and
asserts the pairing, plus a separate test asserting the adjudicating roles
never gain `survey:capture` — so re-introducing the hole by widening the role
matrix fails too.

### 2. The must-change-password gate did not apply to unpermissioned routes — MEDIUM

`require()` refused a principal still holding an administrator-issued password.
`requireAuth()` did not. An officer on a credential somebody else chose — and
may still have written down — could reach every unpermissioned route while
being correctly refused every permissioned one.

**Fixed:** `requireAuth` now applies the same gate.
`requireAuthDuringPasswordChange` is a narrow exemption for the only two routes
that lead out of a restricted sign-in: `POST /auth/password`, and `GET /auth/me`
which is how a client discovers it must ask. A test asserts no third route ever
uses it.

### 3. Eight reachable vulnerabilities in the Go standard library — MEDIUM

`govulncheck` reported eight, all with call paths from this code:

| | |
|---|---|
| GO-2026-6091 | `html/template` — this API renders HTML reports |
| GO-2026-6089 | `net/http` — `ReadHeaderTimeout` not applied on the unencrypted HTTP/2 check |
| GO-2026-6218 | `net/url` — quadratic complexity in `resolvePath` |
| GO-2026-6090, 5856 | `crypto/tls` |
| GO-2026-6088, 5972 | `encoding/xml`, `encoding/asn1` recursion depth |
| GO-2026-5026 | `net/http` via `x/net/idna` |

`go.mod` pinned `go 1.26.4`, and CI installs from `go.mod`, so every CI build
was producing a vulnerable binary.

**Fixed:** pinned `go 1.26.6`. `govulncheck` now reports **0**.

### 4. Unbounded evidence-store reservations — MEDIUM

Nothing limited how many uploads one report could open. Each reserves up to 60
MB in the evidence store and is reclaimed only after a seven-day expiry. The
rate limiter slows this but does not stop it: 300 requests a minute is 300
reservations a minute, and the store being filled is the bucket holding every
farmer's photographs.

**Fixed:** `MaxOpenUploads = 8` — exactly the four station clips and four
stills a report needs. Completed uploads do not count, so a farmer redoing a
station that failed is never refused. Returns 429, which the client already
treats as retryable.

### 5. No Content-Security-Policy on HTML responses — LOW

`securityHeaders` was written for a JSON API and said so. That stopped being
true: `GET /reports/{id}/print` returns `text/html`, and the assessment dossier
renders HTML. Both are pages served from the API's own origin carrying a named
farmer's land and losses.

Values are escaped at render time (verified — every `template.HTML` call wraps
`template.HTMLEscapeString`), so this is defence in depth.

**Fixed:** `default-src 'none'` with no `script-src` at all, plus
`frame-ancestors 'none'`, `base-uri 'none'`, `form-action 'none'`. HSTS is now
emitted when the connection is genuinely TLS — `r.TLS`, or
`X-Forwarded-Proto: https` **only when a trusted proxy is configured**, so the
header cannot be induced by a caller.

### 6. A leftover bootstrap password only warned — LOW

The bootstrap administrator is created only when the user table is empty, so a
leftover credential is not a second way in. It is worse in a quieter way: it is
the live administrator's password sitting in an environment file, an
orchestrator's secret store and a shell history, long after whoever set it
stopped thinking about it. Startup warned; nobody reads warnings from a service
that started successfully.

**Fixed:** a hardened environment now refuses to start. Development is
unaffected — it needs the credential to have an administrator at all, and a
rule that breaks the laptop gets deleted rather than obeyed. Covered by
`internal/config/hardening_test.go`.

### 7. Chunk object keys built from a raw path parameter — LOW

`partKey()` was called with the `uploadID` path value rather than the uuid the
database returned. Not exploitable today — a non-uuid fails the lookup first —
but the key becomes a filesystem path under `blob.Dir`, and that is one
refactor away from a traversal. Now keyed on `st.ID` throughout.

### 8. Android cleartext posture was implicit — LOW

Android has refused cleartext by default since API 28 and the manifest never
opted in, so the release posture was already correct — but only by default,
and the obvious fix for a developer whose local API is plain HTTP is
`android:usesCleartextTraffic="true"` in the main manifest, which ships.

**Fixed:** `src/debug/res/xml/network_security_config.xml` permits cleartext to
the development host and nowhere else, scoped to debug builds so a release APK
keeps the refusal. Manifest merge verified.

---

## Tested and sound

Each of these was actively probed, not merely read.

**Injection (A03).** Every query is parameterised. The three SQL fragments
built with `+` concatenate compile-time `const` strings. No `os/exec` anywhere.
Injection payloads in path parameters returned 401/404, never 500.

**XSS.** Every `template.HTML` construction escapes its dynamic content first.

**SSRF (A10).** Every outbound URL is `BaseURL + path` from configuration. No
handler accepts a URL, callback or redirect field.

**Path traversal.** `blob.Dir` *rejects* keys containing `..` rather than
normalising them — deliberately, since normalising would silently collide two
keys in a content-addressed store — with a root-prefix check behind it.

**Cryptography (A02).** argon2id at 64 MiB / t=3 / p=2, PHC-encoded,
version-checked, with a rehash path when parameters are raised.
`subtle.ConstantTimeCompare` for both passwords and one-time codes. OTPs stored
hashed and bound to the number they were issued for. Ed25519 seeds must decode
to exactly 32 bytes.

**Authentication (A07).** Forged signature, `alg=none`, and garbage tokens all
returned 401. Sign-in throttling is two-tier (per identifier and per address,
the latter far higher because a taluka office is one NAT address).

**Enumeration.** `POST /auth/farmer/otp` returns an identical 202 and identical
body for registered and unregistered numbers — verified with throttle state
cleared, after an earlier reading that appeared to differ turned out to be an
artefact of the test itself. `GET /me/registry/{id}` returns a **byte-identical**
404 for "not your record" and "no such record".

**Access control (A01).** The farmer registry rule — you may load only the
record whose registered mobile matches the number you verified — was tested
with two live farmer sessions: owner 200, stranger 404, officer 403. The
projection leaks none of `aadhaar`, `caste`, `dob`, `gender`, `khata`,
`joint_owner`, `identifier_name`, `mobile`. Jurisdiction scoping holds: a
district Tehsildar listing users saw 2 of 10.

**Aadhaar.** Only `aadhaar_hash` and `aadhaar_last4` exist in the schema, both
under CHECK constraints. The raw value is never persisted.

**Audit integrity (A08/A09).** `audit_log`, `report_verifications`,
`report_adjudications` and `survey_captures` are append-only, enforced by
database triggers. This was proven the hard way: the trigger **refused this
assessment's own cleanup**, because deleting a user would have nulled
`actor_user_id` on their audit rows. The audit trail wins over deletion, which
is the correct priority.

**Rate limiting.** Per signed-in identity rather than per address, so an office
behind one NAT is not throttled as a single caller. `RateLimit-*` headers
correct under load.

**Error handling.** Failures return generic codes. No stack traces, driver
messages or SQL fragments reach a client.

**Token storage.** `flutter_secure_storage` v11, Android Keystore-backed.

---

## Residual risks, and what was done about them

A second pass closed what could be closed in code. What remains is listed
honestly below, with the reason it remains.

### Closed: plaintext transport is now a startup refusal

Previously the largest open risk. A hardened environment now refuses to start
unless one of two things is true: TLS terminates in this process
(`ESURVEY_TLS_CERT_FILE` / `ESURVEY_TLS_KEY_FILE`), or a proxy terminates in
front and `ESURVEY_TRUSTED_PROXY_HOPS` says how far away it is. There is no
third configuration.

The server now terminates TLS when configured, with TLS 1.2 as the floor and
forward-secret cipher suites only. 1.2 rather than 1.3 because Android 6
handsets — which `minSdk` deliberately supports — have no TLS 1.3, and a farmer
with an old phone unable to file is a worse outcome than a modern suite.

`docker-compose.yml` now binds the API to **127.0.0.1** by default, matching
Postgres and Redis. Device testing sets `API_BIND=0.0.0.0` for an afternoon,
which is the point: exposure becomes a decision rather than a default.

### Closed: the audit log is now tamper-evident, not just append-only

Migration 0020 chains every audit entry to the one before it: SHA-256 over the
row's contents, the previous entry's hash, and a seed
(`ESURVEY_AUDIT_CHAIN_SEED`, required in hardened environments) that is not in
the database. `audit_log_verify()` returns no rows when the log is intact, or
names the first entry that fails.

This addresses the case append-only cannot: the people holding database
credentials are the people the log exists to hold accountable, and a superuser
can drop a trigger, edit a row, and put the trigger back. Proven against all
three attacks — editing an entry, deleting one, and verifying under the wrong
seed — in `internal/audit/chain_test.go`, which performs the tampering *with
the append-only trigger disabled*, exactly as a DBA would.

### Closed: the unmaintained dependency is now a checked invariant

`golang.org/x/crypto/openpgp` cannot be removed — `argon2` lives in the same
module and a module is the unit you can require. What changed is that its
unreachability is now enforced rather than observed:
`TestNothingReachesTheUnmaintainedCrypto` fails if it ever enters the server's
transitive graph, and `TestThisCodeUsesNoWeakPrimitives` fails if any package
in this repository imports `crypto/md5`, `crypto/sha1` or `math/rand`.

A first version of that test also forbade those three *transitively* and failed
at once — TLS needs sha1 for legacy signatures, protobuf uses math/rand. Their
presence in the binary is not a weakness; using them here would be. The test
was wrong, not the code, and it now asks the right question.

### Closed: the minSdk mismatch

`build.gradle.kts` carried a paragraph explaining that `minSdk` is "API 23,
explicitly, rather than inheriting `flutter.minSdkVersion`" directly above a
line that inherited it. Now stated as the literal, with the trade-off written
down: Android 6 receives no security patches, so supporting it means running on
platforms with known unfixed flaws — accepted because the alternative is that
those farmers cannot file at all, and carried by keystore-backed tokens, no
cleartext in release builds, and server-side geofencing that trusts nothing the
handset asserts.

### Closed: certificate pinning is enforced on every connection

`PinnedClient` checks the server's certificate once the connection exists and
before any of the response reaches the caller, and `Api` uses it whenever a
build supplies `ESURVEY_PINS`.

This took two attempts and the first one is worth recording. It wrapped
`HttpClient` and delegated through `noSuchMethod`; it compiled, satisfied the
interface, and would have thrown on the first header set — an app unable to
make any request at all, which would have been discovered on a handset rather
than in a test. It was backed out and replaced by writing the request path out
in full.

Verified against a real TLS server with two generated certificates, in
`test/pinning_transport_test.dart`. The test that matters: the client is
configured to **trust** the server's certificate — as a handset trusts a
certificate authority somebody installed — and the request is refused anyway,
on the strength of the pin alone. That is the whole difference between "TLS
says this is fine" and "this is ours".

Pinning stays off unless a build supplies pins, and refuses to enable on fewer
than two: one pin plus a lost key is an app that can never reach its server
again, on every handset, permanently.

### Closed: erasure on request is implemented

The retention sweep is time-based — data reaches an age and a policy applies.
That covers not keeping things forever. It did not cover the other obligation:
a named person asking to be erased now, years before any policy reaches them.
There was no answer to that request, and the account could not simply be
deleted, because the audit log references it `ON DELETE SET NULL` — an UPDATE,
which the append-only trigger refuses.

`Sweeper.ErasePerson` now does it, with the same safety rails as the sweep: dry
run by default, a second call to confirm. It removes the person and keeps the
decision — the area and the figures survive for audit; the name, mobile,
address, Aadhaar reference, photographs and GPS fixes do not.

Three things the tests forced out that the first implementation missed:

- **Objections carry the person twice.** `grievances.farmer_name`, and a
  statement the farmer wrote themselves. The statement is the one a name-only
  redaction misses and is some of the most re-identifying text here — "the plot
  below Ramrao's well" names somebody without naming them. Both are redacted;
  the coded ground and the decision survive, so the appeal stays auditable.
- **The schema refused the erasure.** `farmer_has_handset` required every
  farmer account to have a mobile number — correct for an account somebody
  uses, and exactly the operation the DPDP Act requires. Migration 0021 scopes
  the exception to deactivated accounts, which cannot sign in and are never
  messaged. A dedicated `erased` status was rejected: it would turn "who asked
  to be forgotten" into a column any query can group by.
- **Erasure is refused while an objection is open.** Erasing somebody
  mid-appeal destroys the evidence their own appeal rests on, and a request
  arriving during an appeal is likelier to be somebody acting in that person's
  name than the person abandoning their own claim.

The audit log is untouched and cannot be otherwise — it is append-only and
hash-chained precisely so that nobody, including someone acting on a legitimate
erasure request and including someone claiming to be, can rewrite who did what.
What it retains is an opaque uuid and, on authentication events, a mobile masked
to its last two digits. That is the reason actor labels are masked when written
rather than when somebody asks.

### Closed: the SDK pin, and a correction to the previous pass

`compileSdk` is now 37, matching what `flutter_secure_storage` — the plugin
holding this app's refresh tokens — compiles against. The platform installs as
`android-37.0` and Gradle matches the name literally, so a directory junction
bridges the two; the build fails loudly without it, which is better than a
silent version skew inside the credential store.

The previous pass also "fixed" a comment/code mismatch by setting `minSdk = 23`,
matching a paragraph claiming Android 6 support. **That was wrong and the build
proved it:** `flutter_tts` declares minSdkVersion 24 and the manifest merger
refuses anything lower. The voice guidance is not optional on this app — it is
how a farmer who cannot read the screen is walked across their own field — so
24 is what the app costs. `minSdk` is now the literal 24, with the real
constraint written down. Android 6 is not supported and cannot be.

### Closed: the database connection was the forgotten half of transport

Requiring TLS on the API while every farmer record, Aadhaar hash and assessment
crossed to Postgres in the clear is securing the door and leaving the delivery
entrance open — and a managed database is reached over a network somebody else
operates.

A hardened environment now refuses `sslmode=disable`, and also `allow` and
`prefer`, which fall back to plaintext silently: the connection succeeds,
nothing is logged, and the only way to find out is to look at packets. An
absent `sslmode` is treated as `prefer`, because that is what libpq does — an
unset value is not a neutral one. `require` is accepted with a note that it
encrypts without verifying the server; `verify-full` is the target.

### Closed: the checks now run without anybody remembering to run them

An assessment finds what is vulnerable on the day it runs. Two additions make
that continuous:

- **`govulncheck` is a CI job**, and the workflow runs daily as well as on
  push. It reports only what this code can actually reach, which is what makes
  failing a build on it reasonable rather than something people learn to
  override. It is exactly the check that would have caught the eight
  standard-library vulnerabilities months before a person did — and because it
  reads the toolchain from `go.mod`, a vulnerable Go release is itself a
  failure.
- **Dependabot** covers the Go modules, the workflows themselves, the base
  images, the Flutter packages and the Python dependencies. Nobody had raised
  the pinned Go version because nothing asked them to.

Base images stay on floating tags deliberately. Digest pinning is the stronger
position, but only alongside something that raises the digests: pinned without
that, a base image freezes at whatever was current the day somebody pinned it,
and the patches that were arriving for free stop arriving — which is how
"pinned for supply-chain integrity" becomes a two-year-old OpenSSL. Dependabot
is that something; pin the digests once it is running reliably.

### Closed: the web console kept the previous user's claims and photographs

Found by reviewing the one tier the earlier passes had skipped. The console has
its own offline store — IndexedDB holding draft claims and capture blobs — so a
farmer can fill in a report where connectivity is worst.

`endSession()` cleared the tokens and notified its listeners. The only listener
set the React user to null. **Nothing cleared IndexedDB**, so signing out left
the previous person's draft claim and their photographs in the browser for
whoever signed in next — on precisely the devices a web console is used from in
this context: a common service centre, a village kiosk, a shared office
machine.

The handset app had cleared its store on sign-out since it was written, with a
comment explaining that a phone in a village is borrowed. The same reasoning
applies to a shared browser and had never been followed through.

`clearPersonalData()` now empties the drafts, captures and metadata in one
transaction — separately would risk leaving photographs behind once the drafts
referencing them were gone: unreachable through the UI and still on the disk.
Parcel boundaries stay, because a polygon is a fact about a survey number
rather than about a person, is already public in the record of rights, and
costs a round trip to refetch in the place least likely to have one.

Wired to `onSessionEnded` rather than to the sign-out button, because both
paths end a session: a token refused mid-request is a sign-out nobody chose,
and the data underneath it is just as much the previous person's.

The rest of the console held up: no `dangerouslySetInnerHTML`, `innerHTML` or
`eval` anywhere; `npm audit` reports zero vulnerabilities; the access token is
held in memory and only the single-use rotating refresh token is persisted,
with the httpOnly-cookie trade-off already written down honestly in the source.

### Closed: the AI service decoded unbounded payloads

The last two unreviewed tiers were the Python services. They came out mostly
clean — no `eval`, `exec`, `pickle`, `subprocess` or `shell=True` anywhere, and
nothing that logs a credential or a farmer's data.

One real gap. `Image.data_uri` had no `max_length` and the `images` list had no
`max_items`, so the service would `base64.b64decode` whatever it was handed,
into a container limited to one gigabyte. A large enough payload OOM-kills it
and takes assessments down for every district at once.

The API that calls it caps a capture at 8 MiB, so nothing could reach this
today. But that is a fact about the current deployment rather than a property
of the code, and this is where the decoding happens. Bounded at 12 MiB per data
URI — 8 MiB plus base64's third — and sixteen images, since a thousand small
ones cost as much as one enormous one. Pydantic enforces both before
`decode_image` is ever called, which is the point: a bound applied after
decoding has already allocated the memory it was meant to prevent.

### Closed: the internal services now authenticate their callers

The last finding left open, and closed after the deferral was reconsidered:
the reason given for deferring was risk, and the stack was running, so the risk
was testable rather than a reason.

Neither Python service checked who was calling it. Both are bound to loopback
in `docker-compose.yml`, so on a single host nothing off-box reaches them — but
that is a fact about one deployment, not a property of the services. On an
orchestrated network, anything able to resolve the service name could spend the
Gemini budget, hand the agent photographs of a named person's land and read
back what a multimodal model made of them, or run billable Earth Engine work.

"It is only reachable internally" is the assumption that has ended more
incidents than it has prevented, and it costs one header to stop relying on it.

Both services now require a shared token, compared in constant time so the
comparison does not leak its prefix. Either `Authorization: Bearer` or
`X-API-Key` is accepted — the platform's satellite client has always sent the
latter, and teaching it a second convention for one of the two services it
talks to would have been a change with no security value. Health endpoints are
deliberately ungated: an orchestrator polls them before it has credentials, and
a liveness probe that fails authentication restarts the container it was
checking.

An unset token leaves the check off, because a laptop runs without any of this
and a rule that stops a developer starting the stack is a rule they delete
rather than satisfy. `ESURVEY_INTERNAL_TOKEN` is required in a hardened
environment, so the permissive default cannot reach production. That rule is
unconditional in practice: `AI_SERVICE_URL` carries a default, so clearing the
variable does not unconfigure the agent — a test asserts this, after a first
version assumed otherwise.

Not a defence against someone who has already read a container's environment on
the same host. Complete against everything short of that: a misconfigured
network policy, a second workload on the same cluster, a port accidentally
published.

**Verified on the running stack:** both services answer 401 with no token and
401 with a wrong one; the correct token gets through to a 422 on an empty body
and a 404 on an unknown task, which is auth passing and validation doing its
own job. Health stays 200. The platform API's `/readyz` stays 200, so it still
reaches both.

### The honest limit

Everything above is what was found by a review that read this codebase, ran its
tools against it, and attacked a live instance of it. It is not proof that
nothing else is there. The controls that would catch what this missed are the
ones now in place — an audit log that cannot be quietly rewritten, denials
recorded, permissions asserted by tests rather than by comments — plus the
assumption that the next review will find something this one did not.

## Assessment artefacts

Three accounts were created for live testing (`vapt-tehsildar`,
`VAPT Farmer Owner`, `VAPT Farmer Stranger`). Their roles were revoked and all
three set to `deactivated`; sign-in is refused. They could not be deleted —
the append-only trigger refused it, which is the control working — and
`ErasePerson`, written during this assessment, is now the supported way to
carry out that removal. Redis was flushed, clearing all test sessions.

Two self-signed certificates live in `mobile-app/test/fixtures/` for the
pinning transport tests. They are test fixtures with no bearing on any
deployment, and their private keys are deliberately worthless: regenerate them
with `openssl req -x509 -newkey rsa:2048 -nodes -days 3650` if they expire.

## Verification at the close

    backend    33 packages pass · go vet clean · gofmt clean · govulncheck 0
    mobile     199 tests pass · flutter analyze clean
    live       5/5 defensive headers · 401 on every protected route unauthenticated
    chain      tamper, deletion and wrong-seed all detected
    pinning    a trusted certificate refused on the pin, against a real TLS server
    erasure    person removed, decision kept, refused mid-appeal
    ci         govulncheck gates every build and runs daily; dependabot enabled
    console    typecheck, lint and build clean; npm audit 0; offline store cleared on sign-out
    python     125 tests pass across both services; ruff clean; payloads bounded
    internal   both analysis services refuse unauthenticated callers, verified live
