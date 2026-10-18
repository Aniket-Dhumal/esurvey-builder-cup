# Parameters for loss estimation

## Implementation status

Classes A to F are built, tested, and wired into the assessment. What follows
is the reasoning behind each; this table is what actually runs.

| | Parameter | Where it lives | State |
|---|---|---|---|
| A1 | Prior seasons for the same parcel | `internal/baseline` | **live** |
| A2 | Neighbourhood comparison | `internal/baseline` | **live** |
| A3 | Threshold yield | `internal/baseline`, table `threshold_yields` | **live, awaiting figures** |
| B1 | Sentinel-1 backscatter | `internal/sar`, `internal/satellite/radar.go` | **live** (satellite-intelligence service) |
| B2 | Canopy water (Sentinel-2 NDMI) | `internal/moisture`, `internal/satellite/moisture.go` | **live**, corroborating only |
| C1 | Sowing date from green-up | `internal/agronomy/greenup.go` | **live** |
| C2 | Growth stage by thermal time | `internal/agronomy/thermal.go` | **live** |
| C3 | Variety duration | `internal/agronomy` | **live** |
| C4 | Stage susceptibility | `internal/agronomy/susceptibility.go` | **live** |
| D1 | Google Weather cross-check | `internal/weather/google.go` | **built, off until a key and a terms review** |
| D2 | Season-long weather windows | `internal/weather/windows.go` | **live** |
| D3 | Hail days, separated from thunderstorms | `internal/weather` | **live** |
| D4 | Soil moisture (Sentinel-1 relative index) | `internal/moisture` | **live**, corroborating only |
| D5 | Heat and cold stress by stage | `internal/agronomy/thermal.go` | **live** |
| E | Officer quadrat counts | `internal/fieldwork`, `/reports/{id}/measurements` | **live** |
| F | Claim and village history | `internal/claims` | **live** |

Assembled concurrently by `internal/evidence`, which never fails an assessment
and names every source it could not get. The packet is sent to the panel, kept
on the assessment for reproduction at an appeal, and shown to the officer
before the panel's own reasoning.

**One thing is deliberately not done.** A3 holds issued figures and has none,
because nobody has issued any — the absence is reported rather than filled
with a satellite proxy dressed in the same units.

**Radar and moisture are served by `satellite-intelligence`**, one service on
one Earth Engine project, sharing one concurrency budget. B2 and D4 were added
on 14 September 2026; see "B2 and D4, as built" below.

Verified end to end against the live services on the Malegaon reference
parcel: 91 days of weather, a seed-fill stage at 1599 GDD, three prior seasons
read, and a neighbourhood that fell 59% against the parcel's 35%.

---

## The structural problem, before any list

**`damage_percent` is a percentage of nothing defined.**

A farmer declares 60%. An officer records 60%. The panel is asked whether 60%
is plausible. Nowhere does the system hold *what the crop would have yielded
had the calamity not happened* — so "60% of what?" has no answer in the data
model.

This matters more than any individual missing input:

- A 60% loss on a crop already failing from a dry August is a different fact
  from 60% on a crop heading for its best year, and the system cannot tell
  them apart.
- Every Indian crop-loss scheme settles on a **threshold yield** — typically
  the average of the best five of the last seven years for that crop in that
  notified unit. The platform has no concept of it.
- Without a denominator, the figure is a *judgement expressed as a number*.
  It reads as a measurement, which is worse than reading as a judgement.

Everything in Class A below exists to give that percentage a denominator.
That is the work worth doing first.

---

## What the system uses today

### Declared by the farmer
| Parameter | Where |
|---|---|
| Survey number, farm id | `crop_loss_report_items` |
| Crop name | `crop_loss_report_items.crop_name` |
| Area sown (ha) | `area_sown_ha` |
| Area affected (ha) | `area_affected_ha` |
| Damage percent | `damage_percent` |
| Calamity type, loss category | `crop_loss_reports` |
| Date of incident | `occurred_at` |

### From the registry (AgriStack)
| Parameter | Note |
|---|---|
| Parcel geometry | `plot_geometry` — **null for every live farmer**; mock only |
| Owner extent, area unit | Owner's share, not polygon area |
| Village / taluka / district LGD | Drives every authorization decision |
| Crop, sowing date, season, year | Empty for every live farmer; mock only |
| Irrigation type | Passed to the panel |
| Joint owners | Apportionment noted as unverified |

### Remote sensing
| Parameter | Source |
|---|---|
| NDVI time series, one season | Sentinel-2 via the analytics engine |
| Peak (baseline) NDVI and date | Derived |
| Incident NDVI, ΔNDVI, RRVL% | Derived |
| Observation count, largest cloud gap | Derived — the system knows when it is blind, and has no fallback |
| Slope, elevation, aspect | Copernicus DEM GLO-30 |
| Waterlogging risk | Derived from slope class |

### Weather (fetched by the weather specialist)
14 days before the incident, Open-Meteo ERA5, ~11 km grid: daily rainfall,
precipitation hours, wind speed and gusts, max/min temperature, WMO codes,
hail days, thunderstorm days.

### Ground evidence
Photographs and video with GPS fix, accuracy, heading, device, timestamp, and
six gate results (inside parcel, position fix, within window, after incident,
freshness, distinct image).

### Officer
Outcome, damage percent, affected area, free-text findings, visit timestamp,
per-crop-line verified items.

---

## Class A — the denominator

### A1. Historical NDVI for the same parcel

**What it buys.** The only honest baseline. If this parcel peaked at 0.74 this
year and 0.71, 0.76, 0.69 in the three previous seasons, a drop to 0.31 is a
real loss. If it peaked at 0.74 against a history of 0.45, the "loss" is a
return to normal — and possibly a claim against an unusually good year.

**How to gather.** Free, and the pipeline already exists. The Sentinel-2
archive runs to 2015; the analytics engine already queries it. The change is
one parameter: today `NDVIRequest` takes a single `sowing_date → incident_date`
window. It needs to accept a list of seasons, or a `baseline_years: 5`.

**How to use it.** Give the satellite specialist the parcel's own history and
have it answer a different question: not "did the curve drop" but "did it drop
below what this land normally does at this point in its cycle". Feed a
**z-score against the parcel's own history** rather than a raw ΔNDVI.

**Effort: low. Value: highest of anything here.**

### A2. Peer baseline — neighbouring parcels, same crop, same date

**What it buys.** Separates three things a single-parcel curve cannot:
regional calamity, local calamity, and a false claim.

| Parcel drops | Neighbours drop | Reading |
|---|---|---|
| yes | yes | Regional event. Corroborates the calamity. |
| yes | no | Local event, or the parcel was harvested early, or misreported. |
| no | yes | The claim is doubtful, and the surrounding evidence says so. |

**How to gather.** Same Sentinel-2 imagery, aggregated over the circle or
village polygon. Already have the jurisdiction geometry.

**How to use it.** A **relative anomaly**: this parcel's ΔNDVI minus the
median ΔNDVI of its village for the same crop and window. This is also the
strongest fraud signal available that does not require a photograph, and it
scales to a whole taluka without an officer visiting.

**Effort: low–medium. Value: very high.**

### A3. Threshold yield and Crop Cutting Experiments

**What it buys.** The statutory denominator. Every settlement in an Indian
crop scheme ultimately rests on it.

**How to gather.** State Agriculture Department. CCE results are already
collected at notified-unit level under existing schemes — this is an
integration and a data-sharing agreement, not a new measurement.

**How to use it.** Convert `damage_percent` from a judgement into a stated
shortfall against threshold yield, and record which basis was used. Even
holding it read-only alongside the assessment would let an officer see the
figure they are implicitly claiming against.

**Effort: medium (institutional, not technical). Value: high — this is what
makes a figure defensible in an audit.**

---

## Class B — seeing through cloud

### B1. Sentinel-1 SAR backscatter

**The most serious technical gap in the system.**

**What it buys.** Radar penetrates cloud. Monsoon calamity happens under
cloud, so optical NDVI is most likely to be blind at exactly the moment it is
most needed. The system already records `largest_gap_days` — it knows when it
cannot see, and has nothing to fall back on.

SAR also directly detects two things NDVI infers badly:
- **Lodging** — flattened crop changes backscatter geometry sharply.
- **Standing water** — open water is near-black in SAR. Waterlogging is
  currently *inferred from slope*, never observed.

**How to gather.** Free via Copernicus, and available in Earth Engine —
`farm-terrain@esurvey-501512` already holds `roles/earthengine.admin`. The
analytics engine is the natural home; the platform contract barely changes.

**How to use it.** A parallel series (VV/VH backscatter) alongside NDVI, and a
new specialist — or an extension of the satellite specialist — that reports
lodging and inundation as findings in their own right. Critically: when the
optical gap is large, SAR becomes the primary observation rather than the
assessment degrading to "we could not see".

**Effort: medium. Value: very high, and it addresses a known blindness rather
than adding a nicety.**

---

## Class C — the crop, properly modelled

### C1. Objective sowing date

**What it buys.** Sowing date currently comes from a farmer declaration or the
mock file and is **never verified**, yet every phenological conclusion hangs
off it. The phenology specialist has already flagged this in live runs
("inherits uncertainty from the mocked sowing date").

**How to gather.** Derived from the NDVI green-up inflection — a standard,
well-published method, computable from data already fetched.

**How to use it.** Cross-check the declared date. A declared sowing date three
weeks from the observed green-up is either a mis-entry or a misstatement, and
either way the growth-stage reasoning built on it is wrong.

**Effort: low. Value: high — it repairs an input everything else depends on.**

### C2. Growth stage by thermal time, not calendar days

**What it buys.** The panel currently reasons from "114 days after sowing". A
crop develops on accumulated heat, not dates: the same variety reaches
flowering weeks apart between a cool and a hot season.

**How to gather.** Growing Degree Days from the temperature series already
fetched, against published crop coefficients (ICAR / state agriculture
university tables for soybean, cotton, tur, jowar, bajra, onion, grape).

**How to use it.** Replace days-after-sowing in the phenology prompt with an
accumulated-GDD stage estimate. Stage matters enormously for recoverability —
vegetative damage regrows, damage at flowering or pod-fill does not.

**Effort: medium. Value: high.**

### C3. Variety and duration

Short, medium and long-duration varieties of the same crop are at different
stages on the same date. Currently only `crop_name` is held.

**Gather:** add to the farmer's declaration and to the AgriStack crop record.
**Use:** select the right GDD requirement in C2.
**Effort: low. Value: medium — it is a multiplier on C2.**

### C4. Stage-specific susceptibility

A hailstorm at pod-fill and the same hailstorm at seedling stage produce very
different losses. There is no such table in the system; the model reasons from
general knowledge.

**Gather:** publishable agronomic matrices from ICAR / MPKV Rahuri, per crop
per stage per calamity.
**Use:** a lookup the phenology specialist is *given* rather than recalls —
this is a place where grounding beats model knowledge, and it makes the
reasoning auditable.
**Effort: medium (agronomic input needed). Value: high.**

---

## Class D — weather that matches the event

The current 14-day, 11 km ERA5 window is a reasonable regional cross-check and
too coarse for several real calamities.

### D1. Google Weather and environmental data

Replaces the institutional AWS route (Mahavedh / IMD), which needed a
data-sharing agreement before a single call could be made. Two Google
surfaces matter here and they answer different questions.

**Earth Engine — the historical record.** Already enabled on this project,
and `farm-terrain@esurvey-501512` already holds `roles/earthengine.admin`.
Its climate catalog carries what an assessment needs about a past date:
ERA5-Land hourly at ~9 km (better than the ~11 km currently used, and a full
archive rather than a 92-day reach), CHIRPS and IMERG for precipitation, and
the same catalog that holds Sentinel-1 and Sentinel-2.

This is the important point: **one integration covers A1, A2, B1 and D1.**
Parcel NDVI history, the village peer baseline, SAR, and better weather are
all queries against a service that is already provisioned and already
credentialed. They stop being four projects.

**Weather API (Maps Platform) — the near-real-time picture.** Current
conditions and hourly/daily forecast at a far finer resolution than a
reanalysis grid, with fields the panel does not get today: gust, thunderstorm
probability, dew point, cloud cover. Not enabled on this project yet; it is a
paid Maps Platform API needing billing and a key.

**Two things to verify before committing to it:**

1. **Historical reach.** It is a forecast and nowcast product; its hourly
   history lookback is short. A report filed three weeks after a hailstorm
   cannot be answered by it. The workable shape is to capture conditions at
   *filing* time from the Weather API and take the incident-date record from
   Earth Engine — not to replace the archive with it.
2. **Caching and retention terms.** Maps Platform restricts storing results.
   An assessment must be reproducible during an appeal, which means the
   weather that justified it has to be retained with it. If the terms forbid
   that, this API cannot be the evidentiary source, only a live cross-check.

The other environmental APIs are not relevant: Air Quality and Pollen answer
nothing about crop loss, and Solar is roof-oriented.

**Effort: low for Earth Engine (already provisioned), low–medium for the
Weather API (billing, key, terms review). Value: very high** — and unlike the
institutional route, none of it is blocked on somebody signing an agreement.

### D2. Longer windows
Cumulative seasonal rainfall, longest dry spell, rainfall in the sowing
window. Drought and delayed sowing are invisible in a 14-day view.
**Effort: trivial** — the same API, a wider range.

### D3. Hail, specifically
WMO codes 96/99 are a weak proxy: they say a thunderstorm *with* hail was
coded for an 11 km cell. IMD Doppler radar reflectivity, or a lightning
network, resolves convective cells far better.
**Value: high** — hail is the most-claimed and least-visible calamity.

### D4. Soil moisture
Waterlogging risk was *inferred from slope*. Sentinel-1-derived soil moisture
measures it. Pairs with B1. **Built — see below.**

### B2 and D4, as built

**What is measured.** For the parcel, over sixty days before the incident
(clipped to sowing) and thirty after, in one Earth Engine computation:

- **NDMI** `(B8A − B11)/(B8A + B11)` from Sentinel-2 surface reflectance at
  20 m, per clear pass, for the season and for the same calendar window in the
  three prior seasons. A pixel is clear only when Cloud Score+ `cs_cdf ≥ 0.6`
  *and* the scene classification is not cloud, shadow, cirrus or snow — shadow
  drags NDMI down in a way that reads as drought.
- The same, for **cropland within 1.5 km** (ESA WorldCover class 40, excluding
  a 30 m margin around the parcel), on the same dates.
- **Sentinel-1 VV** over two years, per relative orbit, for a soil index.

**What it means** (`internal/moisture`). No absolute NDMI threshold exists, so
none is used. Near the incident the field is compared with its own normal (a
mean of per-year means, so one wet year with many clear passes does not become
the normal) and with its neighbours, *as a change* against the gap before the
incident, so a field that is always a little drier is not newly stressed.

| Finding | Pattern |
|---|---|
| `canopy_water_stress` | drier than its own normal **and** than its neighbours |
| `locality_dry` | drier than its normal, **level** with its neighbours — corroborates the event, not the field |
| `wetter_than_neighbours` | markedly wetter than the cropland around it; in a dry spell, usually irrigation |
| `no_canopy_water_change` | at its normal |
| `surface_wet` / `surface_dry` | soil index ≥ 0.8 / ≤ 0.2 of the field's own 5th–95th percentile range on one orbit |

Starting thresholds: noise 0.04 NDMI, material 0.08. A pass needs ≥ 50% of the
parcel clear and ≥ 9 pixels; the ring needs ≥ 200 cropland pixels; the soil
index needs ≥ 20 passes on one relative orbit and is flagged weak when NDVI
near the incident is ≥ 0.6. Orbits are never mixed.

**What it is not.** Never a damage figure, never in the fusion, marked
*Corroborates* on the dossier (§6a). Each reading states how much it bears on
the calamity claimed — strong for drought, limited for flood (the soil index
is the useful half), weak for hail, fire and landslide.

**Cost and scale.** One Earth Engine call per parcel and window, about 20 s,
bounded by the service's shared semaphore and queue limit; identical requests
join one computation; results are cached in Postgres (`remote_sensing_cache`,
product `moisture:v1`), permanently once the window has settled. A generated
(synthetic) record is refused outside development and never cached.

**Calibrating it.** The thresholds are named constants. After a season, compare
moisture findings against officer findings in the calibration store before
giving this evidence any weight.

### D5. Heat and cold stress
Hours above ~35 °C during flowering, or below threshold for horticulture. The
temperature series is already fetched; only the stage-aware aggregation is
missing.

---

## Class E — ground truth an officer can measure

An officer currently records a percentage and free text. That is a judgement,
recorded as a number, with nothing behind it.

| Add | Why |
|---|---|
| Plant population per m² vs expected | The single most objective field measurement |
| Lodged plant % in a quadrat | Countable; defensible |
| Damaged pods/grain per sample | Directly proportional to yield loss |
| Quadrat photograph with scale | A photograph with a measuring tape is evidence; one without is an impression |
| Soil type | Determines drought and waterlogging response |

**How to gather.** Structured fields in the verification form, and a quadrat
step in the capture wizard — the farmer app already does staged capture with
sensor gates, so the mechanism exists.

**How to use it.** Feed the counts to the vision specialist alongside the
image, and let the officer's figure be *derived* from measurements rather than
asserted. This is also what makes an adjudication survive an appeal.

**Effort: low–medium. Value: high, and it is the only item here that improves
the *human* half of the assessment.**

---

## Class F — history and context

- **Prior claims on the same parcel and farmer.** Repeat-claim patterns are a
  standard fraud signal and the data is already in the database — nothing
  queries it.
- **Prior assessment outcomes for neighbours.** Spatial coherence of claims.
- **Distance to drain / flood-prone mapping.** Corroborates inundation claims.
- **Land tenure — owner, tenant, sharecropper.** Determines who is entitled;
  currently only ownership share is held, and its apportionment is flagged
  unverified.

**Effort: low** (mostly queries over existing data). **Value: medium–high for
fraud detection.**

---

## Priority

Ordered by value per unit of effort, not by class:

| # | Parameter | Effort | Why first |
|---|---|---|---|
| 1 | **Peer/village NDVI baseline** (A2) | low | Separates regional from local from false, at scale |
| 2 | **Historical NDVI, same parcel** (A1) | low | Gives the percentage a denominator; pipeline exists |
| 3 | **Objective sowing date** (C1) | low | Repairs an unverified input everything depends on |
| 4 | **Longer weather windows** (D2) | trivial | The tool already takes `days_before`; nothing passes it |
| 5 | **Sentinel-1 SAR** (B1) | medium | Fixes a known blindness; detects lodging and water directly |
| 6 | **Google Weather and Earth Engine** (D1) | low–med | Carries 1, 2 and 5 with it; no agreement to wait on |
| 7 | **Structured field measurements** (E) | low–med | Makes the officer's number defensible |
| 8 | **GDD growth stage** (C2) | medium | Replaces calendar-day reasoning |
| 9 | **Prior-claim history** (F) | low | Existing data, unqueried |
| 10 | **Threshold yield / CCE** (A3) | institutional | Statutory defensibility |

Ranked separately from the classes because the ordering changed once D1
became Google rather than an institutional agreement. Items 1, 2 and 5 are
all Earth Engine queries, and Earth Engine is already enabled and already
credentialed on this project — so the honest reading is that **one
integration delivers the top of this table**, and it is no longer sensible to
schedule them as separate pieces of work.

Only item 10 is still blocked on somebody outside this project.

---

## What should **not** be added

Worth stating, because the temptation runs the other way:

- **A single composite loss score.** Combining these into one number would
  hide exactly the disagreements between sources that make the assessment
  trustworthy. The panel's value is that it reports four opinions and their
  limitations.
- **More decimal places.** Nothing here justifies reporting damage to 0.1%.
  Precision the evidence cannot support is the failure this system was built
  to avoid.
- **Any parameter that cannot be shown to a farmer.** If an officer cannot
  explain why a figure changed, it should not move the figure.
- **Model-recalled agronomy in place of tables.** C4 is worth doing as a
  lookup precisely because a recalled coefficient cannot be audited.

---

## The honest ceiling

Even with all of the above, two limits remain:

**No ground truth exists.** Nobody has photographed a damaged field, had it
assessed by an officer, and recorded both. Until that dataset exists, the
platform's damage figures cannot be validated — only made more defensible.
Building it is a field exercise, not a software change, and it is the single
thing that would most change what this system can claim.

**AgriStack returns no geometry and no crop for any live farmer.** Every
parameter above that needs a parcel boundary inherits that. Field-traced
boundaries in the farmer app are the current answer, and their provenance is
carried on every derived figure for exactly this reason.
