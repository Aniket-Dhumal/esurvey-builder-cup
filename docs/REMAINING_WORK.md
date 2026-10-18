# eSurvey — what is missing, as of 10 September 2026

A survey of gaps in the platform: features not built, capabilities built but
not connected, defects found and not yet fixed, and decisions still open.

Everything here was verified against the source on the date above, not
recalled. Where a claim rests on a file and line, it is cited. Where something
could not be verified — the local stack was not running — that is said.

The codebase carries almost no `TODO` markers by convention; decisions are
argued in prose next to the code instead. So this list was assembled
structurally, by reading the integration points and comparing them against
`DEVELOPMENT_PLAN.md` §7–§9 and `ADMIN_AND_ANALYSIS_PLAN.md` §4–§5.

---

## Closed since this was written — 10 September 2026

Each item below carries its own **Done** note saying what was built. In summary:

| | What was built |
|---|---|
| §1.1 Deadlines above the pair | A per-rung allowance, an overdue view, and two bells. Nothing escalates on a timer — see §6 |
| §1.2 Filing deadline | Enforced at filing, and events close themselves when the deadline passes |
| §1.3 Password reset | Request route, per-address cooldown, reset tokens, and both screens |
| §1.4 Assessment dossier | `GET /api/v1/assessments/{id}/dossier`, rendered from the stored packet |
| §1.5 Rollup tables | `report_rollups`, kept current by a trigger and an incremental sweeper |
| §1.6 Product-admin console | `/platform` — live integration state, queue depths, circle coverage |
| §1.7 State-level view | `/statewide` — districts, crops, events and an eight-week trend |
| §3.1 Pair completeness | Checked against the findings, not inferred from a column |
| §3.2 Weather archive 502 | Retried, with a refusal never retried |
| §4.1 Circle data | Not filled in. Sourced, found not to exist as public data, and now **measured** rather than invisible |
| §5.4 Backups | `scripts/backup.sh` and `scripts/restore-drill.sh`, drilled |

Two things were deliberately **not** done, and each is recorded where it
belongs: circles were not invented (§4.1), and the mobile build was left
debug-signed (§5.3) on instruction.

Also closed, though it is not listed below because it was found while doing
the above: **the return path had never worked.** Migration 0023 wrote
`UPDATE report_verifications SET superseded_at = now()` into the Circle
Officer's send-it-back path, against a table that migration 0006 had made
refuse every UPDATE. Every attempt to return a report to the village pair
failed against a real database, and no Go test could see it because the suite
does not run one. Migration 0030 narrows the trigger to permit exactly that
one transition and nothing else, and the first database-backed tests for it
are in `internal/report/amend_test.go`.

And one thing was added that this catalogue did not ask for, because the
answer to its own question made it necessary: **the village pair can now
converge.** They still record blind and still cannot edit a finding. Once both
have filed and each has read the other, either may record a revised finding
that supersedes their own and keeps it, marked `amended` rather than
`returned` — an officer who moved towards their colleague was not corrected.
Two officers a few points apart could previously do nothing but send the gap
to the Circle Officer as a disagreement to arbitrate.

---

## 1. Not built

### 1.1 No deadline above the village pair — *highest impact*

> **Done.** `internal/report/rung_sla.go` gives the Circle three days, the
> Tehsil four and the District five — shorter than the pair's seven, because
> nobody above the pair goes anywhere. They read a finding already written,
> against evidence already gathered, at a desk; giving a desk review a field
> visit's allowance is how a claim that took a week to verify takes a month to
> approve, four times over.
>
> Migration 0027 adds `approval_level_since`, backfilled from the last approval
> rather than from `now()`, so no report starts life looking punctual.
> `OverdueRungs` feeds a new panel on the approvals screen, and `SweepOverdue`
> tells both the officer holding the rung and the officer above them — telling
> only the late officer fails in exactly the case that matters, because an
> officer who has stopped opening the console is precisely the one whose
> reports are late.
>
> **Nothing moves on a timer.** That was asked and answered: flag and notify,
> never escalate. A report that escalated on a clock would arrive at the next
> rung with no finding behind it and would quietly reward whoever ignored it.
> The contradiction with `DEVELOPMENT_PLAN.md` §9 is settled in favour of
> `overdue.go`. See §6.

`report_assignments` is the only table carrying a per-officer deadline, and
rows are inserted at exactly one tier:

```go
// backend/internal/report/verify.go:345
INSERT INTO report_assignments
    (report_id, officer_id, role, tier, assigned_by, due_at)
VALUES ($1, $2, $3::officer_role, 'village_pair', $4, $5)
```

So the Talathi and Krushi Sahayak have a seven-day clock and the Circle
Officer, Tehsildar and District Magistrate have none. A report that reaches
the tehsil can sit there for a season with nothing marking it late and nobody
above able to see it.

This is the risk `DEVELOPMENT_PLAN.md` §9 names last and most bluntly: *"Five
tiers is faithful to how Maharashtra actually works, and it is also five
chances for a claim to stall. Per-tier SLA timers and auto-escalation are not
nice-to-haves — without them the system reproduces the delays it exists to
remove."* Four of the five tiers currently have neither.

**What it needs:** a due date per rung, and the overdue view extended above
the pair. `internal/report/overdue.go` already has the right shape; it only
ever sees `village_pair` rows.

### 1.2 The filing deadline is never enforced

> **Done.** `report.Create` now selects `survey_closes_at` and refuses a filing
> past it, naming the date and saying the district can reopen the event — a
> refusal that tells a farmer what to do next rather than only that they are
> too late. `event.SweepExpired` closes `open` and `paused` events whose
> deadline has passed, leaving `closed_by` null because nobody closed it, and
> tells the field.

`report.Create` validates the area, the event's state and the incident window:

```go
// backend/internal/report/report.go:444
SELECT incident_start, incident_end, state::text, reference
```

`survey_closes_at` is not in that query and is not checked anywhere in the
creation path. Nothing closes an event when its deadline passes either. An
event stays `open` until an officer closes it by hand, and until they do, a
report can be filed a month past the advertised deadline — which the printed
report and the console both show the farmer as binding.

### 1.3 No self-service password reset

> **Done.** Migration 0028 adds `purpose` and `requested_ip` to
> `account_invites` and makes the live-token index per purpose, so a reset
> cannot silently revoke an outstanding invitation.
> `POST /api/v1/auth/password/reset` answers identically whether or not the
> address exists — a reset form that says "no such account" is an account
> enumerator with a friendly face — and is rate limited per address on a hash
> of it, five minutes, failing open when Redis is down. Accepting a reset
> revokes every session, which is the half that makes it a remedy. Each
> endpoint refuses the other's token rather than quietly honouring it.
>
> The screens are `/forgot-password` and the existing `/set-password`, which
> now words itself differently for a reset. Both render without a session:
> `AppShell` keeps a closed set of public routes, because the shell used to
> replace them with the sign-in form — so "Forgotten your password?" went
> straight back to the form it was clicked from.
>
> Sign-in also accepts an email address as well as a username, because every
> one of these emails tells the officer to use their email and, for accounts
> created before the invitation flow, that was false.

`mail.PurposePasswordReset` is declared at `internal/mail/mail.go:46` and
referenced nowhere else in the backend. There is no route, no token flow and
no screen.

An officer who forgets their password today has no way back in. The
invitation flow does not help: `resendInvite` refuses an account whose
password has already been set (`ALREADY_CLAIMED`), which is correct — an
invitation is not a reset — and leaves no path at all. The only remedy is a
database edit.

The invitation machinery is most of the answer. What is missing is a
request-a-reset route, a rate limit on it, and reuse of `account_invites`
with the reset purpose.

### 1.4 The assessment dossier is not served

> **Done.** `GET /api/v1/assessments/{id}/dossier`, permissioned like reading
> the assessment, rendered by `internal/dossier` and printed from the console
> with the same fetch-and-write mechanism the crop loss report uses.
>
> It renders from the evidence packet **stored** with the assessment, never by
> re-gathering. NDVI moves, radar gets another pass, the weather archive
> answers or does not — a document that changes when nobody changed anything is
> not a record of a decision. An assessment printed today and the same
> assessment printed at an appeal next year are the same page.
>
> It is provisional until the claim is adjudicated, and `dossier.Build`
> enforces the consequence: a provisional document carries no figure at all,
> because a provisional number becomes the number everybody remembers and no
> later correction catches up with it.

`internal/dossier` builds a full bilingual assessment document — sections,
instrument rows, caveats, provenance — and nothing in `internal/api` calls
it. The only printable document over HTTP is the crop loss report
(`GET /api/v1/reports/{id}/print`), which is the farmer's filing, not the
assessment.

The practical consequence: producing an assessment PDF means rendering the
console page in a headless browser. That works and it is what was done on
3 September, but it means the document's layout is the web app's, page
breaks are incidental, and there is no server-side artefact to archive or
sign.

### 1.5 No rollup tables

> **Done.** Migration 0030 adds `report_rollups`, keyed
> `(event, village, crop)` — the leaf, with every level above it summed at
> query time across `jurisdictions.ancestor_ids`, which is already indexed
> because it is how every authorization check works. A state query then touches
> roughly 45,000 village rows rather than 10⁶ claims, and that ceiling is fixed
> by the size of Maharashtra rather than by how many claims get filed.
>
> **Percentages are stored as numerators** — `sum(percent × affected area)`
> beside the area — because a damage percentage cannot be averaged upward. Two
> villages at 60% do not make a taluka at 60% unless the same area was
> affected, and the wrong version yields a plausible-looking percentage at
> every level while being right only at the leaf.
>
> Kept current incrementally: a trigger records which `(event, village)` pairs
> a write touched, and `RefreshRollups` drains that queue in bounded batches on
> a minute ticker, claiming with `FOR UPDATE SKIP LOCKED` so two API instances
> divide the work rather than duplicating it. A full rebuild on a timer is the
> same full scan the rollup exists to avoid, moved off the request.

`ADMIN_AND_ANALYSIS_PLAN.md` §5.2 specifies rollup tables keyed by
`(event, jurisdiction, crop)`, refreshed incrementally. None exist. Every
aggregate is computed live: `report.Overview` runs `GROUP BY` over
`crop_loss_reports` on each dashboard load.

At today's volume that is correct and the simpler choice. The plan's own
estimate is order 10⁶ claims a season statewide, 10⁴–10⁵ per district, and
this will not hold there.

### 1.6 No product-administrator console

> **Done.** `/platform`, gated on a new `platform:read` permission held only
> by `system_admin` — by whoever runs the platform and by nobody who uses it.
> The data-source page's old gate was `reference:read`, which nearly every
> officer holds, which is how a Talathi in a village came to be shown Earth
> Engine's health.
>
> It leads with what is **not** connected, because that is what an operator
> opens it to find: live state and queue depth for SMS, email, evidence storage
> and threshold yields, plus the analytics engine and the panel, and the exact
> environment variables that turn each one on — named, never valued. A status
> page that echoes an API key has published it to everyone who can read the
> page. It also carries the circle-coverage figures from §4.1 and links to the
> audit trail and the data-source page, which stay out of the sidebar.

The audit trail and the data-source status page were removed from the
sidebar on 3 September, gated behind `operatorOnly` in `lib/nav.ts`, on the
understanding they belong to whoever runs the platform rather than whoever
uses it. Both routes still work and are still permissioned; there is simply
no console that offers them. Until one exists, an operator reaches the audit
log by typing a URL.

### 1.7 No state-level analytical view

> **Done.** `/statewide`: districts side by side ordered by area affected
> rather than by claim count — a district with eight hundred large claims is a
> bigger problem than one with four thousand small ones — the same claims cut
> by crop and by event, and an eight-week filing trend.
>
> Read from the rollups, and every figure is dated on the page along with how
> many villages are waiting to be recounted. An aggregate that implies it is
> live and is not is worse than one that dates itself.
>
> Offered in the sidebar on `assessment:escalate` rather than `report:read`,
> which a Talathi and a farmer both hold: a village authority's "statewide"
> view is one village, and offering it is offering a page that can only
> disappoint. The endpoint is scoped like every other read, and a farmer is
> refused in words rather than with an empty page.

A State Officer signs in and gets the district dashboard scoped upward. There
is no cross-district comparison, no per-crop or per-event breakdown, and no
trend over time — which is most of what oversight at that level is for.

### 1.8 Localisation is hardcoded, not a framework

Marathi and English are paired at each call site as literal string pairs
(`T("...", "...")` in the dossier, `messageForCode` in the mobile app). There
is no locale negotiation and no message catalogue. Adding a third language —
Hindi is the obvious one — means editing every site rather than adding a file.

### 1.9 Disbursement — out of scope by decision

No payment file, no bank integration, no treasury handoff. This was ruled out
at the start and is recorded here only so the list is complete.

---

## 2. Built, but not connected

These are wired end to end and waiting on a credential or a provider. None
require code.

| Capability | State | What unblocks it |
|---|---|---|
| **SMS to farmers** | `internal/notify` queues every message; no gateway | A DLT-registered provider, sender ID and template ids |
| **Email** | `internal/mail` queues invitations; no SMTP host | `ESURVEY_SMTP_HOST` and credentials |
| **Evidence in GCS** | `blob.NewGCS` exists; falls back to a local directory | `ESURVEY_EVIDENCE_BUCKET` |
| **Threshold yields** | Read if present; absent for every crop | A statutory figure per crop per unit per season |

Each degrades honestly rather than silently — the queues report their backlog,
and an assessment with no threshold yield says the percentage is not a
shortfall against a statutory figure. But none of them is delivering.

### 2.1 Ground vision is a placeholder, and it is the heaviest signal

> **Changed 18 September 2026.** The ground signal is now read by the
> assessment model (Gemini) rather than by colour thresholds, so it is no
> longer absent in production. `ground-vision` decodes the walk and samples up
> to eight keyframes; `ai-services` holds the model credential and returns the
> measurement through `POST /api/v1/ground-measure`. The reading is not
> labelled a baseline, so the platform uses it.
>
> Three limits are deliberate and written into the code:
>
> - **It is an estimate, not a segmentation.** The model has no pixel mask, so
>   its area shares are judgements. They are converted to the contract's counts
>   over a fixed basis, which is exact for the ratios the score is built from
>   (`agent.AREA_UNITS`).
> - **Its confidence is capped at 0.80** (`ESURVEY_VISION_QUALITY_CAP`), below
>   the 0.85 branch that would give it 0.50--0.65 of a compensation figure. It
>   therefore carries **0.35**, against the classical baseline's 0.20 and a
>   validated model's 0.65. Raise the cap when there is a validation against
>   labelled field footage to point at.
> - **A model that fails is never reported as footage with no crop in it.**
>   The two answers travel separately, because "the farmer filmed a path" and
>   "nobody looked" are different things to tell an officer.
>
> What is still not done: validation against ground truth, and the
> specification's own instruments (SegFormer-B5 segmentation, YOLOv10-X stem
> detection over a Structure-from-Motion point cloud). The paragraphs below
> describe the classical baseline, which remains in the service as the
> development fallback.


`ground-vision` implements colour thresholds and a structure tensor, not the
trained segmentation the specification asks for. It caps its own quality at
0.55, which caps its weight in the fusion at **0.20** — against the **0.45**
the specification gives stem lodging alone.

It is accurate at what it does: clips built at 0°, 45°, 68°, 75° and 90°
measured back within about 1°, including the fully-lodged case. But it cannot
distinguish ripening cereal from dying cereal, which its own source says
plainly, and the platform refuses its measurements outside development.

Replacing it with a trained model is the single largest piece of remaining
engineering, and the one with the largest effect on assessment quality.

---

## 3. Known defects

### 3.1 Pair completeness is inferred, not checked

> **Done.** `Standing` carries `PairComplete`, read from the findings rather
> than inferred from the level column, and `ladder.Check` asks the findings.
> Two tests hold it: a rung with no findings behind it is refused, and one with
> findings behind it is allowed.

```go
// backend/internal/report/ladder.go:197
if s.At == "" {
    return ErrPairNotComplete
}
```

Whether the village pair has recorded its findings is inferred solely from
`approval_level` being empty. A report that already carries a level is never
re-checked, so if its findings are absent or superseded it can still climb.

On 3 September a report was observed reaching `adjudicated` with
`findings: []`, `complete: false` and **no `adjudicated_percent` at all** —
because `standing()` found nothing to reconcile, leaving the top-rung write
as `adjudicated_percent = COALESCE(NULL, adjudicated_percent)`, a no-op. An
adjudicated claim with no percentage for the dossier, the rollups or the
farmer's copy.

The inference is confirmed in source. The runtime instance could not be
re-verified on 10 September because the local stack was not running. Worth
establishing whether `recordReturn`'s supersession path can reach this in
normal operation, or only from seeded state.

### 3.2 The weather archive returns 502 intermittently

> **Done.** Two retries, 400 ms apart, on a 5xx or a dropped connection. A 4xx
> and the archive's own refusal are never retried — those are the archive
> declining this request, and asking again gets the same answer more slowly out
> of a budget shared with radar, the registry and the panel. The retry stops
> when the caller's context ends.
>
> Worth fixing rather than tolerating, because weather is the record of whether
> the calamity happened at all: a hailstorm claim assessed with no weather
> behind it has lost the evidence that there was a hailstorm. The packet is
> stored with the assessment, so the gap outlives the outage and is what an
> appeal reads.

Seen on 3 September. It degrades correctly — the source is reported absent
and the assessment proceeds — and since the absent-note sanitiser landed, the
officer no longer sees the JSON decoder's complaint about an HTML error page.
The underlying flakiness is unaddressed.

---

## 4. Data gaps

### 4.1 One circle for the whole state

> **Sourced, and deliberately not filled in.** On the instruction to pull
> whatever is in the public domain: it is not there. LGD stops at sub-district
> and resumes at village. Solapur's own site publishes **counts** — 91 circles,
> 535 sazas and 1,144 villages across eleven talukas — and no names and no
> village mapping. The authoritative record is a district collectorate
> notification, one Marathi PDF per district, published irregularly. Every open
> mirror is derived from LGD or the census and inherits the same gap. The full
> search, the Solapur counts as a reconciliation check for whoever transcribes
> them, and why nothing was generated from them are in
> `reference_data/README.md`.
>
> **What was built instead is the measurement.**
> `GET /api/v1/platform/circle-coverage` and the Platform console now report,
> per taluka, how many circles are recorded and how many villages sit with no
> Circle Officer above them: today 1 of 358 talukas, and 44,910 villages. The
> gap was invisible; it is now a number on a screen.

**Updated 18 September 2026.** Barshi taluka's **ten circles are now loaded
and named** — Barshi, Agalgaon, Gaudgaon, Khandavi, Nari, Pangaon, Pangri,
Surdi, Upale Dumala, Vairag — sourced from circle-wise rainfall and drought
reporting, reconciled against Solapur's own count of ten, and each one a real
village of the taluka. Only the ten headquarters villages are mapped: the
village-to-circle mapping is still published nowhere, and 128 of Barshi's 138
villages remain directly under the taluka. See `reference_data/README.md`.

`jurisdictions` holds 45,315 rows covering every district, taluka and village
in Maharashtra, and **ten circles, all in Barshi**. Every other taluka has
none.

A circle is a revenue unit and LGD assigns it no code, so the
village-to-circle mapping is in no national directory. It has to come from a
Maharashtra revenue source (Mahavedh, Mahabhulekh, or Settlement Commissioner
records). The `lgd-load` ingestion is built and waiting.

Until then the Circle Officer rung is real in the ladder and unmapped on the
ground everywhere except Barshi, and the area picker can offer circles for one
taluka only.

**Codes must not be invented for this.** A wrong code is another real place,
and it misroutes a farmer silently.

### 4.2 Mumbai City (LGD 482) is absent

Not in the source CSV. Not added, for the same reason.

### 4.3 AgriStack returns neither geometry nor crops

`plot_geometry` is null on every parcel tested and `crop_sown_data` is empty
for every farmer. Both are structural and outside our control.

Both are mitigated — boundaries by field tracing, crops by asking the farmer
at survey time — but the mitigation has a cost that is now visible: a parcel
with no boundary has no sampling stations, and until 3 September that silently
routed the farmer to photographs instead of the video walk, skipping the
heaviest evidence signal. That is fixed; the farmer is now offered the
boundary walk first. The underlying dependency remains.

---

## 5. Operational readiness

### 5.1 Hardened mode is unreachable

The API refuses to start as `staging` or `production` until four things are
true. Three are configuration; one is not:

1. `ESURVEY_OTP_SENDER` must be a real delivery channel — and setting it to
   `sms` requires an SMS gateway, which does not exist. **This is the blocker.**
2. `ESURVEY_DATABASE_URL` must not be `sslmode=disable` — needs TLS on Postgres
3. `ESURVEY_AUDIT_CHAIN_SEED` must be set — fixed on 3 September; it was never
   passed through in `docker-compose.yml`, so it could not be satisfied at all
4. `ESURVEY_BOOTSTRAP_ADMIN_PASSWORD` must be removed after first boot — needs
   a two-phase deployment

Until (1) is solved, every deployment runs in development mode, which also
means generated radar series would be accepted as evidence if synthetic mode
were ever switched on.

### 5.2 The cloud deployment is a demonstration, not production

The VM at `34.14.221.80` (project `esurvey-501512`, asia-south1) runs the full
stack behind Caddy with a **self-signed** certificate, in **development**
mode, currently **open to 0.0.0.0/0** at the operator's request. It carries
real farmer records, Aadhaar hashes and photographs.

Before it is anything more than a demo: a domain and a real certificate,
hardened mode per §5.1, a narrowed firewall, and backups — there are none.

### 5.3 The mobile release build is debug-signed

> **Left debug-signed, on instruction, and now documented as such.**
> `mobile-app/README.md` says plainly what this tree produces, what it costs —
> the Play Store refuses it, a handset cannot tell it from a substitute, and no
> field handset can be upgraded to a properly signed build without an uninstall
> that takes the offline queue with it — and what to do when the hold lifts.

`flutter build apk --release` succeeds and produces a 56.8 MB artefact, but
`android/key.properties` does not exist so the signing config falls back to
debug keys. `apksigner verify` reports `CN=Android Debug`. Not distributable.

There is also no release channel, no update mechanism and no version pinning
between the app and the API.

### 5.4 No backups, no restore drill

> **Done, and drilled.** `scripts/backup.sh` dumps Postgres in custom format,
> verifies the archive is readable before reporting success, tars the evidence
> volume, writes a SHA-256 beside both, and prunes on age.
> `scripts/restore-drill.sh` restores into a throwaway database created empty,
> applies the current migrations on top of the restored schema — a backup is
> nearly always older than the code that will read it — and prints the row
> counts.
>
> Run against the live local stack on 10 September 2026: 45,306 jurisdictions,
> 20 reports, 41 captures and 1,073 audit entries restored, migrations 27 to 30
> applied on top, passed. Nothing schedules it yet; the cron line is in
> `docs/OPERATIONS.md`.

Nothing schedules a dump of the database or the evidence volume, on either
the local stack or the VM. The audit log's hash chain makes tampering
detectable and does nothing about loss.

---

## 6. Decisions settled

Both of the open ones were put to the operator on 10 September 2026 and
answered. They are recorded here rather than in a commit message, because
each is a rule the code now enforces and the reasoning is the part that
outlives the implementation.

**§8.2 — Blindness within the village pair. Settled: independent, then
mutually visible, then revisable.**

The answer given was that the two should record independently, *and then see
each other's findings*, so they can agree — because a small numeric difference
should not cause an escalation.

The first two halves already worked: each records blind, and once you have
recorded you see everything. The third did not exist, and it is where the cost
was. Two officers four points apart stayed four points apart on the record, and
two officers eight points apart — the same field, the same crop, one of them
having counted a different quadrat — went to the Circle Officer as a
disagreement to arbitrate. That is a great deal of a Circle Officer's week
spent on gaps the two people who stood in the field could close in a phone
call.

So `PUT /api/v1/reports/{id}/verify` now accepts a revised finding, under
narrow conditions: only the village pair, only after **both** have filed, only
while the report is still theirs, and never in place. See
`internal/report/amend.go`.

What is deliberately unchanged:

- **The first finding is still blind and still cannot be edited.** The
  append-only trigger permits exactly one transition — stamping
  `superseded_at` on a row that does not carry it, changing nothing else — and
  refuses everything else, compared over the whole row rather than a list of
  columns, so a column added next year is protected without anybody remembering
  to add it here.
- **The original survives and is marked.** A reader can always see what each
  officer said before they knew what their colleague thought, which is the
  evidence a dispute actually turns on. `superseded_reason` distinguishes
  `amended` from `returned`: an officer who moved towards their colleague was
  not corrected, and an audit asking how often an officer is sent back must not
  count both.
- **An amendment before the peer has filed is refused.** Then it is not an
  amendment, it is a rewrite of a blind finding, and nothing would distinguish
  an officer fixing a typo from an officer who saw the figure on a screen
  somewhere.
- **The window closes when the report climbs.** Once the Circle holds it, the
  pair's findings are the evidence being weighed, and amending underneath a
  supervisor mid-decision changes the thing being decided. After that the
  mechanism is a return, which is recorded and counted as one.

**Auto-escalation. Settled: flag and notify, never move.**

`internal/report/overdue.go` was right and `DEVELOPMENT_PLAN.md` §9 was wrong,
and the reason is the one already written next to the code: a report that
escalates on a timer arrives at the next rung with no finding behind it, and
quietly rewards the officer who ignored it. The plan's real complaint — that
four of five tiers had no deadline at all and nobody above could see a stalled
claim — is answered by §1.1 without moving anything: a per-rung allowance, an
overdue view scoped so a supervisor sees the delay below them, and two
notifications rather than one.

Settled earlier: the ADK question (§8.1 — a Python sidecar exists), officer
identity (§8.3 — platform accounts with email invitations), farmer access
(§8.4 — mobile OTP), and the kharif cloud gap (§7.3 — Sentinel-1 is
integrated and observing).

---

## 7. What is left, ranked

By what most changes the platform's usefulness or risk. Everything above the
line in §0 is done; this is the remainder.

1. **Trained ground-vision model** (§2.1) — the heaviest signal in the
   specification's own fusion is a colour threshold and a structure tensor,
   capped at a fifth of the weight it should carry. The largest piece of
   remaining engineering and the one with the largest effect on assessment
   quality.
2. **SMS gateway** (§2) — no farmer is being told anything, and the queue now
   says so on the Platform page instead of in a JSON health check. Blocked on a
   DLT-registered sender ID and template ids, not on code.
3. **Circle mapping** (§4.1) — a whole rung of the ladder is unmapped for
   44,910 villages. Blocked on transcribing district revenue records; the
   loader and the reconciliation counts are ready.
4. **Hardened deployment** (§5.1) — blocked on (2): `ESURVEY_OTP_SENDER` cannot
   be a real delivery channel without a gateway, so every deployment runs in
   development mode.
5. **A domain and a real certificate** (§5.2) — the VM is a self-signed demo
   holding real farmer records.
6. **Scheduling the backups** (§5.4) — the scripts exist and are drilled;
   nothing runs them on a timer yet.
7. **Release signing and an update channel** (§5.3) — held deliberately, and
   the hold has a cost that grows with every handset that receives a build.
8. **A localisation framework** (§1.8) — two languages are paired at each call
   site. A third means editing every site rather than adding a file.
