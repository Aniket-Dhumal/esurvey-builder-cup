# The assessment report

The document a farmer receives at the end of a claim, and the record an appeal
is argued from. Marathi in full, then the same report again in English.

This is a specification, not a summary of code that exists. Nothing in
`internal/report` produces this today — `crop_loss_report.gohtml` renders the
*filed claim*, which is the farmer's statement of what happened. This is the
system's answer to it.

---

## 1. The organising principle

`LOSS_PARAMETERS.md` opens with the problem this report has to solve:

> **`damage_percent` is a percentage of nothing defined.**

A 60% loss on a crop already failing from a dry August is a different fact
from 60% on a crop heading for its best year, and a table of NDVI figures does
not tell them apart. A lab report is readable because every value sits beside
its reference range; without one it is decoration.

So the report **states the denominator first** and every later section is
evidence for or against a figure expressed against it:

> या क्षेत्राचे संदर्भ उत्पादन **X क्विंटल/हेक्टर** आहे — मागील सात हंगामांपैकी
> सर्वोत्तम पाच हंगामांची सरासरी. निर्धारित नुकसान **Y%** आहे.
>
> The threshold yield for this parcel is **X quintal/ha**, the mean of the best
> five of the last seven seasons. The assessed loss is **Y%** of that.

Where no denominator can be derived — no parcel history, no peer set, no CCE
figure — the report says so in that position, in those words, and the loss
figure is presented as **a judgement recorded by named officers**, not as a
measurement. That is the honest description of what it is, and printing it
that way costs nothing the system actually had.

---

## 2. The row

Every parameter is one row, six columns:

| निकष | निरीक्षित मूल्य | स्रोत व पद्धत | अपेक्षित / संदर्भ | अंदाजावरील परिणाम | पुरावा |
|---|---|---|---|---|---|
| Parameter | Observed value | Source & method | Expected / reference | Effect on the estimate | Evidence |

The fifth column is the point of the document and exists nowhere in the system
today. It is prose, not a number: what this value means for the loss estimate,
in one or two sentences a farmer can read.

### 2.1 Every row carries its role

Most rows did **not** set the figure, and a lab-style table implies by layout
that they all did. `assessment.Satellite` says so in its own doc comment —
*"They are not a damage measurement."* Printing NDVI in the same visual weight
as a plant count would be the report lying through typography.

Four markers, on every row:

| Marker | Meaning |
|---|---|
| **निर्णायक** · Determines | Entered the loss figure directly. |
| **पुष्टीकरण** · Corroborates | Independent evidence consistent, or not, with the figure. |
| **संदर्भ** · Context | Explains conditions; carries no weight on its own. |
| **वगळले** · Excluded | Available but not used — **with the reason stated**. |

Two things are marked *Determines*, and building it made the distinction
sharper than this document first had it:

- **§4, the quadrat count** — sets the *magnitude* of the loss.
- **§9's issued threshold yield** — sets *what the loss is a percentage of*.
  It is the only figure in the report in yield units, and without it the
  percentage has no denominator at all.

Everything else corroborates or contextualises. If that ever changes, the
markers change with it and the change is visible on the page — and a test
asserts the set, so a third determining row cannot appear without somebody
deciding it should.

### 2.2 Derived numbers show their arithmetic

`fieldwork.Derived.Working` already does this, and its comment is the rule for
the whole report: *"the arithmetic in words, so an officer reads a derivation
rather than a result."*

> stand loss: 240 expected less 92 present, over 240 expected = 62%

Every derived figure in this document is rendered the same way. A number
without its derivation is a number nobody can check, and this report exists to
be checked.

### 2.3 Absent is stated, never blank

A blank cell reads as something withheld. A lab report writes "not tested".

> **ऑप्टिकल उपग्रह निरीक्षण** — या कालावधीत ढगांमुळे एकही स्पष्ट प्रतिमा
> मिळाली नाही. हा निष्कर्ष आहे, त्रुटी नाही.
>
> **Optical satellite** — no cloud-free pass in the window. This is a finding,
> not a failure.

Applies to: clouded NDVI windows, `ErrRadarUnavailable`, missing weather days
(`weather.Window.Missing`), parcels with no boundary, claims with no field
measurement recorded.

### 2.4 Every flag prints its innocent explanation

`claims.Signal.Innocent` is a required field precisely so a signal cannot be
read as an accusation. The report prints the two together, always, in the same
cell — never the flag alone.

---

## 3. Decisions taken

| Decision | Choice |
|---|---|
| Audience | **One document, identical for everyone.** No officer-only annexe. |
| States | **Provisional and final**, one template, visibly stamped. |
| AI reasoning | **Printed in full**, marked *Corroborates*. |
| Numerals | **Arabic (123)** throughout, both halves. |

### 3.1 What one document costs, and how it is paid

Two things in the parameter set looked as though they could not simply be
printed to a farmer. One turned out to be a non-problem; the other is real and
is handled by aggregating rather than removing.

**Peer parcels** (`baseline.Peer`). This turned out to be a problem the
neighbourhood design had already solved. `Store.Neighbourhood` takes a
latitude, a longitude and a radius, and runs **one NDVI analysis over a grid
cell** — there is no list of parcels in the result and no survey number to
disclose. The concern this section was written to address does not arise, and
the grid-snapping that made the analysis cheap is what made it private.

What remains is a confidence floor rather than a privacy one: below four
cloud-free observations the comparison is suppressed and stated as suppressed,
because a fall computed from two passes has the shape of evidence and none of
the substance. Printed as:

> शेजारच्या भागातील घट **13%** · या शेतातील घट **62%** ·
> Sentinel-2 NDVI, 1000 मी. परिसर · 9 ढगमुक्त निरीक्षणे
>
> Neighbourhood fell **13%** · this parcel fell **62%** ·
> Sentinel-2 NDVI over a 1000 m neighbourhood · 9 cloud-free observations

**Officer calibration** (`calibration.Summary`). An officer's historical drift
score is employment data, not claim evidence. What the farmer *is* entitled to
— and what prints — is this claim's own inter-rater delta and which
consensus route it took.

### 3.2 Provisional and final

One template, one code path, distinguished by a stamp that cannot be missed:

- **तात्पुरता अहवाल · PROVISIONAL** — generated after evidence and AI
  assessment, before officer consensus. The loss figure is **absent**, not
  estimated: a provisional number becomes the number everyone remembers.
  Carries the date it was produced and the words "अंतिम निर्णय झालेला नाही".
- **अंतिम अहवाल · FINAL** — after Collector sign-off. Carries the deciding
  officers, the consensus arithmetic, and the appeal window.

A superseded provisional report prints the reference of the final one that
replaced it, so two copies in a farmer's hand can always be told apart.

---

## 4. Sections

Numbering is the printed numbering. Classes refer to `LOSS_PARAMETERS.md`.

### Summary — सारांश

Reference · farmer · village, taluka, district · survey number · crop · event
and date · **the denominator** · **the assessed loss** · who decided it and
when · triage tier · the three parameters that most influenced it.

One page. A farmer who reads nothing else reads this.

### §1 The claim as filed — दाखल केलेला दावा

From `report.Report` and `report.Item`. What the farmer stated, kept separate
from what was found, so the two can be compared rather than conflated.

Reference, filed at, channel, filed late + reason, declaration accepted,
signature kind · survey number, farm ID, crop, area sown, area affected,
**damage percent as claimed** · calamity type and occurrence date.

### §2 The parcel — क्षेत्र

Survey number · owner extent vs polygon area (the two figures that must never
be conflated) · boundary provenance: **field-traced by whom, when, with what
worst-case GNSS accuracy**, or absent. AgriStack returns no geometry for any
live farmer, and every derived figure inherits that — so it is stated here
once, plainly, rather than implied.

### §3 Crop, sowing and growth stage — पीक, पेरणी व अवस्था *(Class C)*

From `internal/agronomy`.

| Parameter | Source |
|---|---|
| Crop and variety duration | `agronomy.Crop`, `agronomy.Duration` |
| Declared sowing date | farmer |
| Observed green-up date | `agronomy.DetectGreenUp` — NDVI inflection |
| Sowing-date agreement | `agronomy.CheckSowingDate` |
| Accumulated GDD to incident | `agronomy.DailyGDD`, capped-mean thermal time |
| **Growth stage on the incident date** | `agronomy.StageOn` |
| Harvested organ | `agronomy.HarvestedOrgan` |
| **Stage susceptibility to this calamity** | susceptibility matrix + exceptions |

This section carries the most explanatory weight in the document. Hail at
flowering and hail at grain-fill are different events, and this is where the
report says which one happened and why that matters. Stage is derived from
thermal time, not calendar days — so a cold fortnight after sowing moves the
stage, and the report shows the accumulated GDD that placed it.

### §4 Field measurement — शेतातील प्रत्यक्ष मोजणी *(Class E)* — **निर्णायक**

From `internal/fieldwork`. **The only section marked Determines.**

Quadrat side and area · plants expected / present / lodged · organ sampled /
damaged · soil type · recorded by whom, when, tied to which photograph
(`CaptureID`).

Derived, each with its `Working` line printed verbatim: stand loss %, lodged %
(never added to stand loss — a lodged plant is not a missing plant), organ
damage %, plants per m².

`Derived.Caveats` print in full. The one that matters most:

> more plants were counted than the spacing expects, so the expected figure may
> be for a different spacing than the farmer used

Where no measurement was recorded, this section says so and the report states
plainly that the loss figure rests on officer judgement without a quadrat
count behind it.

### §5 Weather — हवामान *(Class D)*

From `weather.Window`, Google Weather / Open-Meteo archive.

Window and length · rainfall total, rain days, heavy days · **max daily
rainfall and its date** · **max gust and its date** · hail-coded days ·
thunderstorm days · hottest and coldest · **longest dry spell and when it
began** · missing days.

Effect column ties weather to the claim: whether the recorded conditions are
consistent with the calamity declared, on the date declared. A hail claim on a
day with no hail coding and no gust is not refuted by that — coding is coarse
— but it is stated, with that caveat.

### §6 Optical satellite — ऑप्टिकल उपग्रह निरीक्षण *(Classes A, B)*

From `assessment.Satellite`, Sentinel-2.

Baseline NDVI and its date · incident NDVI and its date · ΔNDVI · **RRVL %** ·
observation count · **largest gap between cloud-free passes** ·
vegetation detected.

The gap figure is printed at the same weight as the result. A large gap means
the series may have missed both the canopy peak and the event itself, and a
reader who is not shown that will over-trust the delta.

`VegetationDetected == false` is a strong, separately-stated finding: nothing
grew that could be lost.

### §7 Radar — रडार निरीक्षण *(Class B)*

From `sar.Finding` and `sar.Reading`, Sentinel-1 VV/VH.

Finding code — inundation, lodging, canopy loss, **or no material change** ·
confidence · detail · caveats · the backscatter values in dB behind it.

`no_material_change` prints as prominently as the others. Radar saw the field
through the cloud and it did not change is a finding the farmer is entitled to
read, and burying it would make this section an instrument that only ever
agrees.

Where radar was unavailable (`ErrRadarUnavailable`), that prints instead.

### §8 Terrain — भूभाग

From `assessment.Terrain`. Mean slope · classification · waterlogging
susceptibility.

Marked **संदर्भ · Context**, and the effect column says why: flat ground under
a thriving crop is not a loss. Susceptibility is not damage.

### §9 Baseline and history — संदर्भ व इतिहास *(Class A)*

From `internal/baseline`. This is where §1's denominator is derived, shown as
a derivation.

This parcel's season peaks, per year · the aggregated peer comparison (§3.1) ·
threshold yield and its basis · the denominator arithmetic.

### §10 Context notes — तपासणी नोंदी *(Class F)*

From `claims.Signal`. Each: code, severity, detail, **and its innocent
explanation, in the same cell**.

Prefaced with the sentence that governs the section — nothing here determines
anything, and every item has an ordinary explanation printed beside it.

### §11 AI assessment — संगणकीय मूल्यांकन

Gemini's loss figure · its reasoning in full · the evidence it cited · model
name and version · prompt version · the date it ran.

Marked **पुष्टीकरण · Corroborates**, with the effect column saying explicitly
that it did not decide the figure and which officers did.

Printed in the farmer's copy in full. Withholding it invites the belief that a
machine decided in secret, which is the accusation this platform is most
exposed to — and if it later emerged that the reasoning existed and was kept
back, that would be far worse than any confusion it causes.

### §12 Officer verification — अधिकाऱ्यांची पडताळणी

Talathi finding · Krushi Sahayak finding · Circle Officer independent review
(blind) · **Δ between them and the consensus route it triggered** — ≤5%
approve, 5–12% arbitration, >12% escalate · Tehsildar, where reached ·
Collector sign-off.

The consensus arithmetic prints as arithmetic, like every other derived figure.

### §13 Evidence — पुरावा यादी

Every photograph: kind, captured at, GNSS accuracy, tilt, **geofence result
against the parcel boundary**, SHA-256 · the boundary walk and its provenance ·
the satellite passes used, by date · the weather grid and source.

Hashes print in full. This is the section that makes the report checkable
rather than merely readable.

### §14 What this report cannot say — या अहवालाच्या मर्यादा

Not optional, and not softened.

- **No validated ground truth exists.** No dataset of photographed, officer-
  assessed fields has been built, so these figures are made defensible, not
  validated. `LOSS_PARAMETERS.md` §"The honest ceiling".
- **AgriStack returns no geometry**, so boundaries are field-traced and every
  area-derived figure inherits that provenance.
- Whatever was absent for this specific claim — clouded window, no radar, no
  quadrat count, no peer set — restated here in one list.

A report that omits its own limits is the one an appeal destroys.

### §15 How to object — आक्षेप कसा नोंदवावा

Grounds (`report.GrievanceGround`) · where to go · the window · what happens
next. In Marathi first, and in words, not references to sections.

---

## 4a. What makes it a record rather than a printout

A crop-loss assessment ends up in three places a web page never goes: a taluka
file, a grievance hearing, and occasionally a court. Each asks the same
questions before reading a word — whose office issued it, under what
authority, which page is missing, who signed, and whether this copy is the one
that was issued. A document that cannot answer them gets argued about instead
of read.

**Cover page.** Government, department, issuing office, title, form code, the
provisional/final stamp, the claim's identity fields, the legal basis, and a
contents list. The "produced by a computer system" statement is on the cover,
not in small print at the end: a reader who assumes a person checked every
figure because the document looks official is reading it wrongly, and the
correction belongs where they start.

**Contents list**, derived from the sections actually printed. A section that
built empty never appears in the body *or* the contents, so the two cannot
disagree.

**Running header and footer** carrying the report number and `page / total`.
The total is the point: a record with a page missing is only detectable if the
pages say how many there should be.

**Signature block**, printed empty, with room for a wet signature and a seal.
A generated document that shows a name where a signature belongs invites the
question of who actually checked it, and at the moment of printing the honest
answer is nobody. The block states that the report is incomplete without them.

**Legal basis** on the cover, because a reader in a hearing looks for it
before anything else.

## 4b. The PDF, and why it is a container

Devanagari is not a font problem. Rendering it means reordering matras that
are stored after their consonant and drawn before it, forming conjuncts from
consonant clusters, and placing the shirorekha across the result — complex
text layout, of the kind HarfBuzz does.

**No pure-Go PDF library has it.** `fpdf`, `gopdf` and `maroto` embed a
TrueType font and draw its glyphs in codepoint order. For Latin that is fine.
For Devanagari it produces text that is not merely ugly but *reordered into
different words*, on a document that decides somebody's compensation and that
they may need to argue with.

So the HTML this package already renders is converted by a real browser
engine. `pdf-service/` is Gotenberg — Chromium behind an HTTP API — with the
Noto Devanagari faces installed explicitly rather than hoped for. The base
image carries a Latin set; without the Indic faces Chromium falls back, and a
fallback font with no Devanagari coverage renders a farmer's report as empty
boxes.

The API image stays distroless and static. It posts HTML and receives PDF.

Three things about the converter's configuration:

- **`--chromium-deny-list=.*`** — it only ever receives HTML this system
  generated, and refusing every remote fetch means a converted document cannot
  be made to reach out of the network on somebody else's behalf.
- **`--chromium-disable-javascript`** — the report is static; nothing in it
  needs to run.
- **PDF/A-3b is available and not the default.** It is what a record kept for
  years should be — fonts embedded, no external references, readable by a
  viewer that does not exist yet — and it costs a second conversion step that
  a draft an officer is checking does not need.

**Nothing depends on the converter.** A report is complete as HTML and prints
from any browser, which is how it will be printed in a taluka office anyway.
`ErrUnavailable` covers both an unconfigured converter and an unreachable one,
because the caller falls back the same way for both; a converter that answered
and *refused* is a different error, and keeps its reason.

The running header and footer carry no Marathi. Chrome renders them in an
isolated context that does not reliably pick up the document's fonts, and a
Devanagari string there can vanish rather than merely lose its styling. A page
number missing from the copy filed at the taluka office is worse than one with
no word in front of it, so the footer reads `1 / 15` — identical in both
halves and impossible to mis-shape.

## 4c. The type

The report carries its own fonts, inlined as data URIs. The stack used to name
faces and hope, which fails three ways and every one of them lands on the
Marathi half:

- A machine without any of the named faces falls back to something with no
  Devanagari coverage, and the farmer's half prints as **empty boxes**. That
  failure is invisible on a developer's laptop, which has Nirmala UI, and total
  in a village office that does not.
- Which face gets picked changes the line breaks, so the same report runs to a
  different number of pages in different offices — and a record whose page
  count is not stable cannot be checked for a missing page.
- The PDF converter refuses every remote fetch, deliberately. A linked webfont
  would simply not arrive.

**Noto Serif Devanagari and Noto Serif**, both variable so bold costs no second
file. They are drawn as one family across the two scripts with harmonised
weights and vertical metrics — which is the whole reason Noto exists, and it
means the two halves of this document are set in the same voice rather than
two. A serif because this is a record that gets filed, photocopied and read in
a hearing, not a screen.

Each face is declared under **one CSS family with its own `unicode-range`**, so
a Devanagari codepoint takes the Devanagari file and a Latin one takes the
Latin file, automatically, mid-sentence. The stylesheet never has to know which
language a cell holds — which matters, because most cells hold both.

SIL OFL 1.1; `fonts/OFL.txt` travels with them as the licence requires.

**Verifying this is harder than it looks.** A `.notdef` box still gets a
ToUnicode entry, so "the Devanagari codepoints are in the PDF" proves the text
is there and says nothing about whether it is legible. And Chromium emits text
fonts as `Type0`/`CIDFontType2` composites inside compressed object streams, so
a scan for `/BaseFont` in the raw bytes finds nothing and looks like a missing
font. The check that actually works: decompress the object streams and read
`/FontName` from the font descriptors — and, better, screenshot the HTML and
compare the embedded face against the host's, which is the only way to see that
the right one won.

## 5. Bilingual mechanics

**One data structure, two renderings.** Every label, every explanatory
sentence, every marker is an `(mr, en)` pair in Go — the same shape as `Lines`
in the farmer app. No translation at render time.

A report whose halves can drift is worse than a monolingual one, because only
the English half gets proofread and the Marathi half is the one the farmer
reads. A pair in the source cannot drift: changing one without the other does
not compile into a sentence, it produces a visibly half-written record.

The Marathi document is complete on its own — summary through §15 — and the
English document follows it in full. Not interleaved, not a parallel column:
the same report twice, so either half can be detached and read alone.

Arabic numerals in both halves, per §3. Dates as `DD-MM-YYYY` in the Marathi
half and `D MMMM YYYY` in the English, both unambiguous.

**Rendering** extends `internal/report/render.go` and `templates/`: HTML with
an A4 print stylesheet, Noto Sans Devanagari embedded, print-to-PDF from the
browser. No new PDF dependency, and the console can show the identical
document on screen.

---

## 6. Reproducibility

The report carries a digest of every input that produced it — the NDVI series,
the weather window, the SAR reading, the measurements, the model and prompt
versions — so that a past report can be re-derived from its recorded inputs
and shown to be unaltered.

That is M7's exit criterion, and this document is the natural place for it to
land: a report an auditor cannot verify is a report an appeal can dismiss.
