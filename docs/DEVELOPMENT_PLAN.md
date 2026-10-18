# eSurvey — development plan

Post-disaster crop loss assessment for Maharashtra. Working plan from the
current vertical slice to a trial-ready system.

Revised 2026-08-29 following scope decisions. Supersedes the earlier draft.

M2 (officers and access) is built; see the repository README. The
administrative workflow and the analysis layer -- M3 and M7 here, plus what
neither covered -- are planned in detail in
[ADMIN_AND_ANALYSIS_PLAN.md](ADMIN_AND_ANALYSIS_PLAN.md).

---

## 1. Scope, as decided

**In scope**

- Post-disaster crop loss assessment, end to end up to an adjudicated loss
  percentage.
- AI assessment via **Google ADK + Gemini**, replacing the specification's
  bespoke YOLOv10 / SegFormer / Mask2Former computer-vision stack.
- Multi-tier officer verification across the Maharashtra revenue and
  agriculture hierarchy.
- Farmer-facing survey capture.

**Out of scope, for now**

- Banking, disbursement, PFMS, NPCI APBS, actuarial payout calculation.
  The platform stops at an **adjudicated loss percentage** and hands off.
- Kafka and event streaming. Revisit when ingest volume justifies it.
- Microservices. **Modular monolith** — one Go binary, clear internal module
  boundaries, extractable later if load demands it.
- Blockchain anchoring, HSM key ceremonies, zero-knowledge proofs.

Dropping disbursement removes the heaviest regulated surface in the project
and, with it, most of the compliance burden. What remains still needs a
defensible audit trail — an assessment that influences a payout decision is
evidence even when this system does not make the payment.

---

## 2. Actors

Six roles across five administrative tiers, mapped to the Maharashtra
structure.

| Tier | Role | Does what |
| --- | --- | --- |
| Village | **Krushi Sahayak** (agriculture assistant) | First technical review of the farmer's survey. Agronomic judgement on crop condition. |
| Village | **Talathi** (revenue) | Verifies land record, ownership and survey-number identity against the RoR. |
| Circle | **Circle Officer** / Mandal Adhikari | Independent check of the village pair's assessment. Resolves their disagreement. |
| Taluka | **Tehsildar** | Escalation authority. Adjudicates where circle review is contested. |
| District | **District Magistrate** / Collector | Final approving authority for the district. Signs off the assessment. |
| State | **State officer** | Oversight, cross-district reporting, no per-claim role. |
| — | **Farmer** | Submits the survey. Raises grievance against an assessment. |

The specification's two-tier maker–checker collapses this hierarchy. The real
structure is richer and worth modelling properly:

- **Talathi and Krushi Sahayak are a pair, not a sequence.** One verifies the
  land, the other the crop. Both must record a finding before it moves up.
- **Circle Officer is the first genuinely independent reviewer** — the natural
  place for the specification's blind-allocation rule, since the village pair
  are local to the parcel and the Circle Officer is not.
- **Tehsildar and Collector are exception paths**, not routine steps. Most
  claims should terminate at circle level. If they do not, the confidence
  model is mis-tuned.

**Decision needed (§6.2):** whether the village pair's findings are blind to
each other, and whether Circle Officer sees them before recording their own.

---

## 3. Architecture

```
┌──────────────────────────────────────────────────────────┐
│  esurvey (single Go binary — modular monolith)           │
│                                                           │
│  internal/registry    AgriStack records, boundaries       │
│  internal/survey      farmer submissions, evidence        │
│  internal/satellite   Farm Analytics Engine client        │
│  internal/assess      scoring, fusion, confidence         │
│  internal/workflow    officer tiers, routing, consensus   │
│  internal/audit       append-only action log              │
│  internal/api         HTTP surface                        │
└────────────────┬─────────────────────────────────────────┘
                 │
    ┌────────────┴───────────┬──────────────────┐
    ▼                        ▼                  ▼
 PostgreSQL             Farm Analytics      Gemini agent
 + PostGIS              Engine (NDVI,       (Google ADK)
                        terrain)
```

Module boundaries are enforced by package structure and reviewed in PR. No
module reaches into another's tables. That discipline is what makes extraction
cheap later; skipping it is what makes a monolith a mess.

**The Gemini agent is the one process outside the binary.** Google ADK is
Python-first, and our backend is Go. See §6.1 — this needs a decision before
M2 starts.

---

## 4. AI assessment — Google ADK + Gemini

This replaces the specification's §7 computer-vision pipeline entirely. The
trade is deliberate and worth stating.

**What we give up:** purpose-trained models with published accuracy on Indian
crops — YOLOv10-X at 84.2% mAP for stem lodging, SegFormer-B5 at 79.8% mIoU
for panicle submersion. Numeric outputs that plug directly into the
specification's formulas.

**What we gain:** no training data requirement, no GPU serving, no ML
engineering hire, multimodal reasoning over heterogeneous evidence, and — most
valuable for this domain — **an explanation an officer can read and argue
with**. A number from a segmentation model is opaque to a Tehsildar. A
paragraph citing the visible evidence is not.

### What the agent sees

Available **today**, before any mobile app exists:

- NDVI time series with dates and cloud-gap structure
- NDVI overlay images (mid-window and last-visit), clipped to the boundary
- Terrain slope and aspect maps, elevation, classification
- Parcel context: survey number, extent, village, crop, sowing date
- Derived figures: ΔNDVI, relative loss, gap analysis

Available **after M5**:

- Farmer's survey video keyframes
- Geotagged crop photographs
- IMU / GNSS telemetry summary

### What the agent returns

Structured output, not prose alone:

```
damage_percent        0–100
severity              Negligible | Moderate | Severe | Catastrophic
confidence            0–1
reasoning             what the evidence shows, in officer-readable language
evidence_cited        which inputs drove the conclusion
contradictions        where signals disagree
insufficient_evidence boolean — the agent may refuse
```

### Rules the agent operates under

1. **It may refuse.** An empty observation window is an absence of evidence,
   not zero damage. `insufficient_evidence` must be a first-class outcome and
   must route to human review rather than producing a number.
2. **It does not set the final figure.** Gemini produces an assessment;
   officers adjudicate. The workflow treats it as a well-informed first
   opinion, never an authority.
3. **Every output is logged with its inputs.** Model version, prompt version,
   full input set, raw response. An assessment that cannot be reproduced
   cannot be defended in a grievance hearing.
4. **Deterministic checks stay deterministic.** Geofence validation, area
   arithmetic, date windows and RRVL remain plain code. The model reasons over
   evidence; it does not do arithmetic we can do exactly.

**Known risk.** Gemini will produce a plausible number for almost any input.
The existing engine's discipline — refusing empty windows, capping confidence
without ground truth — must be enforced *outside* the model, in Go, on both
its inputs and its outputs. Do not let the agent's fluency substitute for
evidence.

---

## 5. Milestones

Ordered so a trial is possible early, and each milestone stands alone.

### M0 — Production foundation (~3 weeks)

- PostgreSQL 16 + PostGIS 3.4; schema, migrations, GIST indices. Replaces the
  in-memory store, which loses everything on restart.
- Config and secrets from environment / Secret Manager. The satellite key is
  currently passed on a command line; that stops here.
- Structured logs with request IDs, Prometheus metrics, `/healthz` + `/readyz`.
- CI: build, vet, test, lint, `tsc`, `next build` — blocking on PR.
- Container images, one-command deploy.

**Exit:** an assessment survives a restart; a broken build cannot merge.

### M1 — Gemini assessment agent (~4 weeks)

Deliberately early. It works with the evidence we already have, needs no
mobile app, and is the piece that makes the product distinctive.

- ADK agent, prompt design, structured-output schema.
- Go client, with the guardrails in §4 enforced on both sides of the call.
- Prompt and model versioning; full input/output logging.
- Evaluation set: hand-labelled parcels with known outcomes, so prompt changes
  can be measured rather than eyeballed.
- Console surfaces the reasoning and cited evidence beside the figures.

**Exit:** an officer opens an assessment and reads why the system reached its
conclusion.

### M2 — Officers and access (~3 weeks)

- Six roles, five tiers, jurisdiction-scoped (a Talathi sees their village
  only; a Collector sees their district).
- Authentication — see §6.3.
- Append-only action log: actor, role, jurisdiction, action, payload digest,
  timestamp. Foundation for the audit trail; get the shape right now.

**Exit:** no anonymous write path exists, and every action is attributable.

### M3 — Verification workflow (~5 weeks)

The heart of the product.

- Routing through the five tiers with per-tier SLA timers.
- Village pair (Talathi + Krushi Sahayak) both record findings.
- Circle Officer independent review; blind allocation.
- Consensus rule: Δ ≤ 5% approve; 5–12% arbitration; > 12% escalate to
  Tehsildar.
- Collector sign-off; grievance intake and re-assessment path.
- Inter-rater reliability tracking, officer drift alerts.

**Exit:** a claim moves from submission to adjudicated loss percentage through
real people at real tiers.

### M4 — Boundary acquisition (~3 weeks)

Resolves the AgriStack geometry gap (§7.1).

- Boundary store with provenance, captured-by, version chain.
- Server-side validation: closed ring, self-intersection, area sanity against
  `owner_extent`, village-envelope containment.
- Reconciliation view for when AgriStack geometry eventually arrives.

**Exit:** a parcel gets a boundary without AgriStack.

### M5 — Farmer survey capture, Flutter (~8 weeks)

- Offline-first SQLite + SpatiaLite, encrypted at rest.
- Cadastral pre-cache; point-in-polygon geofence with 5 m buffer.
- Guided boundary walk, then photo and video capture.
- IMU / GNSS / barometer telemetry, frame-synced.
- Resumable chunked upload.
- Marathi voice guidance.

**Flutter caveat.** High-rate IMU, Camera2 frame-timestamp sync and hardware
keystore access all need platform channels dropping to native Kotlin. Flutter
carries the UI and offline store well; it does not carry the sensor pipeline.
Budget for a native Android module inside the app.

**Exit:** a farmer with no signal completes a survey; it uploads later.

### M6 — Evidence pipeline (~4 weeks)

- Chunk reassembly, signature verification, object storage.
- Anti-spoofing, in value-for-effort order:
  1. Hardware signature + attestation chain
  2. Cadastral geofence over the trajectory
  3. IMU vs optical-flow motion coherence
  4. Solar ephemeris vs observed shadow
- Keyframe extraction; feed imagery to the Gemini agent (closes the loop with
  M1).

Gates 1–2 are cheap and catch the common cases. Gates 3–4 are research-grade;
schedule them separately.

**Exit:** submitted evidence is verifiable and forgery is rejected with a
reason.

### M7 — Audit and reporting (~3 weeks)

- Tamper-evident hash chain over the M2 action log.
- Assessment reproducibility: any past assessment re-derivable from its
  recorded inputs.
- District and state reporting.
- DPDP Act 2023: Aadhaar tokenisation, consent capture, purpose limitation,
  retention. PII redaction in officer views — officers should see geometry and
  agronomy, not caste or phone number.

**Exit:** an auditor can verify no record was altered after the fact.

---

## 6. Sequencing for trials

**Trial-ready path: M0 → M1 → M2 → M3.** Roughly **15 weeks**.

That produces a system where a real disaster event can be assessed on real
parcels, with Gemini's opinion reviewed through the actual officer hierarchy,
using satellite and terrain evidence. No mobile app — parcels are seeded from
the registry and boundaries traced by field staff on any device.

**Then M4 → M5 → M6** adds the farmer's own evidence, which is what makes the
assessment defensible against a grievance.

M7 runs alongside from M2 onward rather than at the end. Audit trails
retrofitted are audit trails not trusted.

The specification's Phase I — 10 districts, Kharif 2026 — is not reachable;
that season is being harvested now. Trials on Kharif 2026 post-harvest events
are possible in ~4 months. **Kharif 2027 is the first full pilot season.**

---

## 7. Blocking dependencies

### 7.1 Parcel geometry
AgriStack returns `plot_geometry: null` on 7/7 parcels across 3 farmers and 2
state deployments; the APIs that guarantee geometry return 403 for our
participant. Boundaries must come from field tracing (M4). Everything spatial
depends on this.

### 7.2 Crop declarations
`crop_sown_data` is empty for every farmer tested. Crop type and sowing date
must be captured from the farmer at survey time. Treat AgriStack's crop
registry as enrichment, never a dependency.

### 7.3 Cloud gaps in kharif
The reference parcel yielded four cloud-free optical passes in 110 days, with
a **60-day gap**. Cyclones and floods land inside exactly those gaps.

Without SAR, optical assessment is structurally blind at the moments that
matter. Two mitigations, and we need at least one: add Sentinel-1, or lean
harder on farmer-captured ground evidence (M5) for cloudy events. Gemini can
reason over ground photographs where NDVI has nothing to say — which is an
argument for pulling M5 earlier if trials fall in monsoon.

---

## 8. Decisions needed

### 8.1 How does Go talk to ADK? — blocks M1
Google ADK is Python-first; our backend is Go. Three options:

- **Python ADK sidecar**, Go calls it over HTTP. True agentic orchestration,
  tools, multi-step reasoning. Costs a second deployable — the one exception
  to the monolith.
- **Go calls the Gemini API directly.** Stays a single binary. Loses ADK's
  agent loop; we hand-roll any tool use.
- **ADK Java**, embedded differently. Neither language matches the stack.

If the requirement is "use Gemini for assessment", option 2 is simpler and
keeps the monolith intact. If it is specifically ADK's agent framework —
multi-step tool use, the agent deciding what evidence to pull — option 1 is
the honest choice. **Which is it?**

### 8.2 Blindness within the village pair — blocks M3
Do the Talathi and Krushi Sahayak see each other's findings? Does the Circle
Officer see both before recording their own, or after? This determines whether
we are building independent verification or sequential sign-off, and they
produce very different consensus statistics.

### 8.3 Officer identity — blocks M2
Where do officer accounts come from? Maharashtra department SSO, Aadhaar
eSign / DSC per the specification, or platform-managed accounts with OTP?
Also governs how much PII we hold.

### 8.4 Farmer access
How does a farmer authenticate to submit a survey and raise a grievance?
Mobile OTP is the obvious answer for the demographic; Aadhaar-linked is the
specification's answer. Different privacy postures.

### 8.5 Team shape
Everything above assumes 2–4 engineers. At 1, extend timelines roughly 2× and
run the trial path only. At 8+, M1, M3 and M5 parallelise.

---

## 9. Standing risks

**A fluent model is not an accurate one.** Gemini will produce a confident,
well-written assessment for almost any input, including bad ones. The
guardrails in §4 must live in Go, on both the inputs and the outputs, and
`insufficient_evidence` must be a real and frequently-taken path. This is the
single largest risk in the revised design.

**Mock data reaching a real assessment.** `mock_files/` holds synthetic
geometry and an invented crop. Every consumer must know provenance, and the
trial path must refuse to assess a parcel whose boundary is mocked. Prefer a
runtime assertion over a convention.

**AgriStack as foundation.** Two of the three registries we need are
unavailable to our participant and we cannot fix that. Architect for AgriStack
as enrichment.

**Hierarchy as friction.** Five tiers is faithful to how Maharashtra actually
works, and it is also five chances for a claim to stall. Per-tier SLA timers
and auto-escalation are not nice-to-haves — without them the system reproduces
the delays it exists to remove.
