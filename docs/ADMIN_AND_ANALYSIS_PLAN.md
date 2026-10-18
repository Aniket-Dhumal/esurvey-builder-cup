# eSurvey — administrative side and analysis system

Development plan for the two halves of the platform that do not yet exist: the
case management the officer hierarchy actually works through, and the analysis
layer that makes a season's worth of claims mean something.

Written 2026-08-30. Extends `DEVELOPMENT_PLAN.md`, which covers the programme
as a whole; this drills into M3 and M7 of that plan and adds what neither
covered.

---

## 1. Where this starts

Built and running:

- **Identity and access** — officers, farmers, six roles, the jurisdiction
  tree, argon2id passwords, farmer OTP, Ed25519 tokens, refresh rotation with
  reuse detection. Authorization checks both what a role carries and where it
  was granted, and pushes the *where* into SQL.
- **Audit** — append-only at the database, every sign-in, denial, grant and
  record read.
- **Assessment pipeline** — registry record → Sentinel-2 NDVI + Copernicus
  terrain → Gemini agent → guardrails applied twice → stored in Postgres,
  jurisdiction-scoped.
- **Officer console** — dashboard, assessment list and detail, parcels,
  farmers, sources.

Not built, and the subject of this plan:

- Any notion of a **disaster event**.
- Any notion of a **claim** — a case that moves, is assigned, accumulates
  evidence and ends in a decision.
- The **officer workflow**: dual review, consensus, escalation, grievance.
- Administrative UI for accounts, postings, jurisdictions and events.
- **Any analysis at all** beyond a per-claim reading.

---

## 2. The fact that governs the sequencing

Every assessment this system produces today refuses to state a damage figure,
because satellite evidence alone cannot measure damage and there is no field
photograph. That is the design working. It is also this:

> The entire administrative and analysis system described below can be built,
> shipped and staffed, and it will faithfully process a queue of claims that
> every one of which says *no damage figure — needs a field visit*.

Nothing in sections 4 or 5 changes that. Only the mobile survey capture
(M4–M6 of the main plan) does.

Two consequences, and they shape everything after:

**Run field survey capture in parallel, starting now.** It has the longest
lead time, it is the gate on the platform producing a number at all, and it is
staffed by different people (mobile + native Android) than the work here.

**Instrument the calibration loop before there is anything to calibrate.**
The moment field surveys land, you want to be able to ask "how close was the
model to what the officer decided" — for every claim, from the first one.
Retrofitting that means discarding the first season. §5.1 is therefore the
first analysis work, not the last.

---

## 3. The missing spine

Today an **assessment** is the top-level record. That conflates two things
that behave differently and must be separated before any workflow is built on
top.

| | Assessment | Claim |
| --- | --- | --- |
| What it is | A computation over evidence at a moment | A case about a farmer, a parcel and an event |
| Mutability | Immutable; re-run produces a new one | Moves through states over days |
| Owner | The system | An officer, at a tier |
| Ends in | A model judgement | An **adjudicated** loss percentage |

One claim has many assessments — the initial satellite-only run, another after
the field survey arrives, another on appeal. Each records the model and prompt
version that produced it, which is already true today and is what makes §5.6
possible.

Above both sits the **disaster event**. In the real process a calamity is
notified with a geography and a date window, and claims are filed against that
notification. Without it, `incident_date` is a free field an officer types,
there is nothing to validate a claim's timing against, and there is no unit to
aggregate over. "How bad was the October hailstorm in Malshiras" is not a
question the current schema can answer.

```
disaster_event  (notified: geography, window, calamity type)
      │
      └── claim  (farmer × parcel × event; has a lifecycle)
            ├── assessment   (model judgement — may be several)
            ├── field_survey (ground evidence — the unlock)
            ├── review       (one per officer who recorded a finding)
            └── adjudication (the human decision that leaves the system)
```

**The adjudicated percentage is a person's decision, informed by the model —
never the model's output promoted to a decision.** Keeping them in separate
columns is what makes §5.6 measurable and what keeps the system defensible
when a farmer disputes a figure.

---

## 4. Administrative side

### 4.1 Disaster events

A calamity is declared by a District Magistrate for their district or a state
officer for wider events. It carries: calamity type (flood, cyclone,
hailstorm, unseasonal rain, drought, pest), the jurisdictions affected, an
incident window (start and end date), a claim-filing deadline, and the
notification reference if a government resolution exists.

Claims may only be filed against an open event, for a parcel inside its
geography, with an incident date inside its window. That single constraint
removes a whole class of retrospective claiming.

Events have their own lifecycle: `draft → notified → open_for_claims →
closed_for_claims → assessment_complete → archived`. Closing for claims is a
deliberate act with a date, because the deadline is contestable and someone
has to own it.

**Effort:** ~1.5 weeks backend, ~1 week UI.

### 4.2 Claims and the state machine

```
                    ┌──────────────── withdrawn
                    │
draft ─→ submitted ─┼─→ assigned ─→ field_survey ─→ pair_review ─┐
                    │                                             │
                    └─→ rejected_intake                           ▼
                                                          circle_review
                                                                  │
              ┌───────────────────────────────────────────────────┤
              ▼                          ▼                        ▼
        taluka_review ─────────→   adjudicated            adjudicated
        (on disagreement)          (approved)              (rejected)
              │                          │                        │
              ▼                          └────────┬───────────────┘
        district_review                           ▼
        (on escalation)                      grievance ─→ re_assessment
```

Every transition records who, when, from what state, and why. Transitions are
authorized by permission *and* jurisdiction, using the machinery already
built — a Tehsildar can move a claim in their taluka and nowhere else.

Two rules worth fixing in code rather than convention:

- **A claim cannot reach `adjudicated` without a recorded finding from both
  village officers**, or an explicit override by a Tehsildar with a reason.
  The pair is the process; letting one signature suffice quietly deletes it.
- **No state transition may set the adjudicated percentage except an
  adjudication.** The figure has exactly one writer.

**Per-tier SLA timers.** Each state carries a deadline. On breach the claim
auto-escalates to the next tier and the breach is recorded. This is not a
nice-to-have: five tiers is five places for a claim to stall, and the system
exists to remove the delay, not reproduce it with better logging.

**Effort:** ~3 weeks.

### 4.3 Assignment and queues

Claims auto-assign on submission to the Talathi and Krushi Sahayak posted to
the parcel's village. Where several hold the posting — additional charge is
routine — allocate by current open workload, not round-robin, so a returning
officer is not handed a month of backlog at once.

Circle review allocates to the Circle Officer for the parcel's circle. Where a
circle has none defined, it falls through to the Tehsildar, and the gap is
reported rather than silently absorbed.

Every officer gets a worklist: what is mine, what is overdue, what is waiting
on someone else. That last column is what stops a claim sitting in a queue
nobody believes is theirs.

**Effort:** ~2 weeks backend, ~2.5 weeks UI.

### 4.4 Dual-officer review and consensus

The Talathi verifies land record, ownership and survey-number identity against
the RoR. The Krushi Sahayak verifies crop, condition and agronomic
plausibility. They are a pair, not a sequence.

**Recommendation: blind within the pair.** Neither sees the other's finding
until both have submitted. The Circle Officer sees both, plus the model's, and
records their own after.

The main plan left this open (§8.2 there). The argument for blindness: the
village pair are local to the parcel and to the farmer, and independence is
precisely what the pairing is meant to buy — a sequential sign-off where the
second officer sees the first's number produces agreement that means nothing.
Blindness also yields inter-rater reliability data for free, which is the only
honest ceiling on how good the model can be made to look (§5.6). The cost is
that officers cannot confer, which in practice they will do anyway; the point
is that the record shows what each concluded independently.

**Consensus rule**, from the specification, applied to the two officers'
damage figures:

| Δ between findings | Outcome |
| --- | --- |
| ≤ 5 points | Circle Officer may approve on the pair's mean |
| 5–12 points | Arbitration by the Circle Officer, who must record a reason |
| > 12 points | Escalate to Tehsildar |

**Effort:** ~3 weeks backend, ~2.5 weeks UI (the panchnama form is the most
detailed screen in the product).

### 4.5 Escalation and grievance

A farmer who disputes an adjudicated figure opens a **grievance**, which
forces re-assessment at the tier above the one that decided. Bounded: one
grievance per claim, and one appeal beyond that to the District Magistrate,
whose decision closes it. Unbounded appeal is how a claim never ends.

A grievance re-runs the assessment against whatever evidence now exists —
which, after a field visit, is more than the first run had. That is the
mechanism by which a wrong refusal gets corrected.

**Effort:** ~1.5 weeks backend, ~1 week UI.

### 4.6 Account and jurisdiction administration

The backend exists; the console does not. Needed:

- Officer creation, with the posting chosen from the jurisdiction tree rather
  than typed as a code.
- **Transfer and acting charge** — the operation that actually happens. An
  officer moves; their open claims must go somewhere, and the system should
  ask where rather than orphan them. Additional charge is a second live grant,
  already representable.
- Deactivation on retirement, with the same question about open work.
- The audit browser: filter by actor, action, outcome, jurisdiction, window.
  An audit log that needs a database console is one nobody checks.

**Effort:** ~2 weeks UI, ~1 week backend for transfer/handover.

### 4.7 Reference data

The jurisdiction tree is seeded one branch deep — state 27 → district 496 →
sub-district 4246 → Malegaon Circle → village 562111. Maharashtra has roughly
44,000 villages.

Needed: an **LGD master loader**, idempotent and re-runnable, that ingests the
official state/district/sub-district/village codes and names. Then, separately
and by hand, the **circle definitions** — circles have no LGD code and the
village-to-circle mapping exists only in revenue department records. Expect
this to be a data-gathering exercise per district, not a download.

Until circles are mapped for a district, villages there hang off the
sub-district and Circle Officers cannot be posted. The system should say so
plainly on the district's admin page rather than presenting an empty role.

**Effort:** ~1.5 weeks, plus unbounded data gathering that is not engineering.

---

## 5. Analysis system

### 5.1 The adjudication ledger — build this first

One append-only record per officer decision, capturing:

- the claim, the officer, their role and posting, the tier
- **the figure they were shown** (model's, and the other officer's if visible)
- **the figure they recorded**
- their stated reason
- time from assignment to decision

Everything in the rest of this section is derived from this table. It is
cheap to build, it must exist before the first real claim is adjudicated, and
it cannot be reconstructed afterwards from anything else.

**Effort:** ~1.5 weeks. Lands with §4.4, not after it.

### 5.2 Aggregation architecture, and what we deliberately do not build

Claims roll up by event × jurisdiction × crop × time. At full state
deployment that is order 10⁶ claims a season; a district is order 10⁴–10⁵.

**Postgres handles this.** Rollup tables keyed by (event, jurisdiction, crop),
refreshed incrementally when a claim changes state, indexed on the jurisdiction
ancestor chain already used for authorization. Reads are then a single indexed
lookup regardless of tier — a state officer's query costs what a Talathi's
does.

**Not building:** a data warehouse, ClickHouse, a BI tool, or a separate
analytics store. At this volume they add an operational surface and a
synchronisation problem in exchange for query performance nobody needs. The
decision is worth revisiting if statewide interactive slicing gets slow; it
will not be at 10⁶ rows a season.

Rollups respect jurisdiction scoping. They also **suppress cells below a small
count** — a village-crop cell with two claims identifies two farmers.

**Effort:** ~2 weeks.

### 5.3 Operational analytics

The analysis that keeps the hierarchy from becoming the bottleneck:

- **Queue depth and ageing** per tier, per jurisdiction, per officer.
- **SLA breaches** — how many, where, how long, and whether auto-escalation
  fired.
- **Throughput** — claims decided per officer per day, and the distribution
  rather than the mean.
- **Stuck claims** — anything with no state change in N days, which is
  different from anything overdue, and more useful.

This is the analysis with the shortest path to being acted on, and it works
with damage figures that are all null. It is therefore the first *dashboard*
work worth doing.

**Effort:** ~2 weeks.

### 5.4 Dashboards, differentiated by tier

Five tiers ask five different questions of the same data. One dashboard for
all of them is how you get a dashboard nobody opens.

| Tier | The question | The screen |
| --- | --- | --- |
| Talathi / Krushi Sahayak | What must I do today | Worklist: assigned, overdue, waiting on the other officer |
| Circle Officer | Where do the two officers disagree | Arbitration queue sorted by Δ, with both findings side by side |
| Tehsildar | What is stuck, and who is slow | Ageing by village and officer, escalations in, SLA breaches |
| District Magistrate | What is the district's exposure, what needs my signature | Event totals, area and claim counts by taluka, exception queue, memorandum draft |
| State officer | Which districts look unlike the others | District comparison on rate, mean loss, refusal rate, fraud flag rate; outliers surfaced |

The state-level comparison is the one with real analytic content: a district
whose approval rate or mean adjudicated loss sits far from its neighbours,
after controlling for the event, is either differently affected or differently
administered, and both are worth a phone call.

**Effort:** ~3 weeks.

### 5.5 Cross-claim fraud analytics

The per-claim indicators already exist. What no single claim can show:

- **The same survey number claimed by two farmers** in one event.
- **Claimed area exceeding holding** — the sum across a farmer's claims
  against their total `owner_extent`.
- **Village-level absence of vegetation** — one claim on a parcel that never
  rose above bare soil is a case; forty percent of a village's claims showing
  it is coordination.
- **Timing clusters** — a burst of filings in the final hours before a
  deadline, from one location.
- **Repeat parcels** — the same parcel claimed across consecutive events at an
  implausible rate.
- **Officer patterns** — an officer whose figure matches the model to within a
  point on almost every claim (rubber-stamping, so the review adds nothing), or
  who consistently exceeds it by a stable margin.

**Rules, not a model.** A fraud flag has consequences for a named person. A
rule that says *this parcel was claimed twice in the same event* can be put in
front of an officer and argued with. An anomaly score from a gradient-boosted
ensemble cannot, and it will be the thing a farmer's advocate attacks. Start
with rules; revisit only if rules demonstrably miss a pattern that matters.

**On measuring officers:** this is politically live and must be built as
though the officers will read it, because they will. Three constraints —
outliers are surfaced for a human to look at and never auto-penalise; every
officer can see their own numbers; and the comparison is always against peers
handling similar claims, not a global mean. A surveillance system rolled out
quietly to government staff gets rejected or gamed, and a gamed system is
worse than none.

**Effort:** ~2.5 weeks.

### 5.6 Model calibration

The point of the whole exercise: is the model any good, and where is it wrong.

Once field surveys exist and adjudicated figures are real, the ledger in §5.1
supports:

- **Agreement** — mean absolute error between the model's damage figure and
  the adjudicated one.
- **Bias** — signed error, sliced by crop, damage band, district and season.
  A model that is right on average and 15 points low on catastrophic loss is
  not usable, and the mean hides it.
- **Confidence reliability** — bucket assessments by stated confidence and
  measure how often they were right. A model saying 0.7 should be right about
  70% of the time; if it is right 40% of the time, the confidence figure is
  decoration and the guardrail thresholds built on it are miscalibrated.
- **Inter-rater reliability** — agreement between the two village officers.
  This is the ceiling: the model cannot be shown to be more accurate than the
  humans agree with each other.
- **Version drift** — everything above, sliced by prompt version and model
  version. Both are already stored on every assessment, which is what makes
  this possible without a migration.

That last point is worth stating plainly: **the reason calibration is cheap
here is that `model` and `prompt_version` were recorded on every assessment
from the start.** A prompt change that quietly degrades catastrophic-loss
accuracy is otherwise invisible.

**Effort:** ~1.5 weeks. Depends on field survey data existing.

### 5.7 Statutory reporting

The outputs government actually needs, in formats imposed from outside:

- **Panchnama** — the joint field inspection report, per claim, signed by both
  village officers. This is the legal artifact; the rest of the system is
  scaffolding around producing it defensibly.
- **Taluka abstract** — claims, area, adjudicated loss by village and crop.
- **District memorandum** — the submission for SDRF/NDRF assistance, which has
  a prescribed form and prescribed loss thresholds.

**These formats must be obtained before this is estimated.** Building to a
guessed format is rework. Flagged in §7.

**Effort:** ~2 weeks *once formats are in hand*.

### 5.8 Privacy in analytics

Aggregation over personal data is still processing of personal data.

- Rollup cells suppressed below a small-count threshold.
- Officer-facing views carry geometry, crop and area — not caste category,
  mobile number or full Aadhaar. The officer's job needs the former.
- Retention limits on the ledger and rollups, distinct from the audit log's,
  which is evidence and kept longer.
- Exports are audited like reads, because an exported spreadsheet is where
  scoping stops applying.

---

## 6. Schema additions

Concrete, so the work can be estimated rather than imagined.

```
disaster_events        id, calamity_type, name, notification_ref,
                       incident_start, incident_end, claim_deadline,
                       state, declared_by, created_at
event_jurisdictions    event_id, jurisdiction_id          -- affected areas

claims                 id, event_id, farmer_id, farm_id, survey_number,
                       village_lgd_code (+ ancestors, as on assessments),
                       state, incident_date, crop_name, claimed_extent_ha,
                       adjudicated_percent, adjudicated_by, adjudicated_at,
                       assigned_talathi, assigned_krushi_sahayak,
                       sla_due_at, submitted_at, closed_at
claim_transitions      claim_id, from_state, to_state, actor, role,
                       reason, at                          -- append-only
claim_reviews          claim_id, officer_id, role, tier,
                       damage_percent, findings, blind_of,
                       submitted_at                        -- one per officer
adjudications          claim_id, officer_id, shown_model_percent,
                       shown_peer_percent, decided_percent, reason, at
grievances             claim_id, raised_by, reason, state, resolved_by,
                       resolution, raised_at, resolved_at

assessments            + claim_id                          -- existing table
field_surveys          claim_id, captured_by, device_attestation,
                       trajectory, captured_at, evidence_uri  -- M5/M6

rollup_event_jur_crop  event_id, jurisdiction_id, crop, claim_count,
                       area_ha, adjudicated_mean, refused_count,
                       fraud_flag_count, refreshed_at
fraud_signals          claim_id | jurisdiction_id, rule, severity,
                       detail, detected_at
```

`claim_transitions`, `claim_reviews` and `adjudications` are append-only,
enforced by trigger the way `audit_log` already is. A review an officer can
edit after seeing their colleague's is not an independent review.

---

## 7. Sequencing

Three tracks, run in parallel, assuming **3–4 engineers**. At one engineer,
run Track A only and roughly double the elapsed time.

### Track A — case spine and workflow (backend)

| | Work | Effort |
| --- | --- | --- |
| A1 | Disaster events | 1.5 wk |
| A2 | Claims, state machine, transitions | 3 wk |
| A3 | Assignment, queues, SLA timers | 2 wk |
| A4 | Dual review, consensus, escalation | 3 wk |
| A5 | Adjudication ledger (§5.1) | 1.5 wk |
| A6 | Grievance and appeal | 1.5 wk |
| | | **~12.5 wk** |

### Track B — administrative console (frontend)

B1, B2 and B6 depend on nothing new and can start immediately.

| | Work | Depends on | Effort |
| --- | --- | --- | --- |
| B1 | Account and posting administration | — | 2 wk |
| B2 | Jurisdiction browser, LGD loader | — | 1.5 wk |
| B6 | Audit browser | — | 1 wk |
| B3 | Event declaration and management | A1 | 1 wk |
| B4 | Worklists per tier | A3 | 2.5 wk |
| B5 | Review and panchnama forms | A4 | 2.5 wk |
| | | | **~10.5 wk** |

### Track C — analysis

| | Work | Depends on | Effort |
| --- | --- | --- | --- |
| C1 | Aggregation layer and rollups | A2 | 2 wk |
| C2 | Operational analytics | A3 | 2 wk |
| C3 | Tiered dashboards | C1, C2 | 3 wk |
| C4 | Cross-claim fraud analytics | A2, C1 | 2.5 wk |
| C5 | Statutory reports | formats in hand | 2 wk |
| C6 | Model calibration reporting | A5 + field data | 1.5 wk |
| | | | **~13 wk** |

### Track D — field survey capture

M4 → M5 → M6 of the main plan, ~15 weeks, different people. **Start it now.**
It is the gate on every damage figure the tracks above will display, and it is
the longest pole.

### Elapsed

With the tracks overlapping, an administratively complete and analytically
instrumented system in **roughly 16–18 weeks**. Every damage figure in it stays
null until Track D lands, which on the same start date is about the same
horizon — so the two arrive together, which is the point of starting D now
rather than after.

---

## 8. Decisions needed

**8.1 Is there a calamity-notification process to mirror? — blocks A1.**
Does Maharashtra issue a formal notification (a GR, a circular) naming
affected talukas and a window, which this system should reference and mirror?
Or do we define the event ourselves and treat the government notification as a
reference field? The first makes events an integration; the second makes them
ours to design. It changes A1 from a fortnight to a research task.

**8.2 The statutory formats — blocks C5.**
Panchnama form, taluka abstract, district memorandum for SDRF/NDRF. Building
to a guessed format is rework, and these are prescribed. Can you obtain the
current versions?

**8.3 Where does the adjudicated percentage go? — shapes the handoff.**
The platform stops at an adjudicated loss percentage and hands off. To whom,
in what format, and does that consumer need a push, a pull, or a file? An
export nobody specified is an export that gets rebuilt.

**8.4 Blindness within the village pair — recommended above, needs your call.**
§4.4 recommends blind. The main plan (§8.2 there) left it open. This is a
process decision more than a technical one and it should be made by whoever
owns the process, but it must be made before A4 starts.

**8.5 Team shape and target season.**
The sequencing assumes 3–4 engineers. The main plan concluded Kharif 2027 is
the first full pilot season, with trials possible on Kharif 2026 post-harvest
events in about four months. Is that still the target?

---

## 9. Risks specific to this phase

**The analysis layer will look finished before it is meaningful.** Dashboards
render, rollups populate, comparisons draw — over a column that is null on
every row. It will be tempting to declare the analysis system done. It is not
done until §5.6 has real adjudicated figures to calibrate against, and the
distinction will not be visible from the screens.

**Fraud analytics against named people.** Every detector in §5.5 produces an
accusation about a farmer or an officer. Rules over models, an explanation
attached to every flag, human review before consequence, and the flag visible
to the person flagged. A false positive here is not a bad metric — it is
someone's claim held up or someone's career touched.

**Five tiers is five places to stall.** SLA timers and auto-escalation are
load-bearing, not decoration. Without them the workflow faithfully models a
hierarchy and reproduces its delays with better instrumentation.

**Circle definitions are a data problem with no download.** Villages-to-circles
exists in revenue department records, district by district. It will take
longer than the code that consumes it, and it blocks posting Circle Officers —
who are, per the main plan, the first genuinely independent reviewer in the
chain.

**The adjudicated figure is the one that matters.** Everything in this
document exists to produce one number per claim that a person decided and can
defend. The model informs it; the model does not set it. Any shortcut that
lets a model output become an adjudication without a named human decision
removes the only thing making the system defensible.
