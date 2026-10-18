# Reference data — the administrative hierarchy

Maharashtra only. The platform is scoped to state 27 for now.

```
lgd_maharashtra.csv        45,305 rows — the official LGD hierarchy
circles_maharashtra.csv         2 rows — revenue circles, mostly missing
```

Loaded with, and safe to re-run:

```bash
docker compose run --rm --no-deps api lgd-load /app/reference_data/lgd_maharashtra.csv
docker compose run --rm --no-deps api lgd-load /app/reference_data/circles_maharashtra.csv
```

LGD first, circles second. Both files use the same format, because a circle is
just another level of the same tree:

```
level,code,name,parent_level,parent_code
```

`level` is one of `state`, `district`, `sub_district`, `circle`, `village`.

---

## What is in `lgd_maharashtra.csv`

| Level | Count | Source |
| --- | --- | --- |
| state | 1 | Maharashtra, LGD code 27 |
| district | 35 | LGD |
| sub_district (taluka) | 358 | LGD |
| village | 44,911 | LGD |

Extracted from the Local Government Directory dump of **29 August 2026**,
mirrored at [ramSeraph/opendata](https://github.com/ramSeraph/opendata)
(release `lgd-latest`), which is a scrape of
[lgdirectory.gov.in](https://lgdirectory.gov.in). Villages of every category
and status are included — rural, urban, uninhabited and forest — because a
parcel can sit in any of them and an excluded village is a farmer who cannot
file.

**Refreshing.** Download a newer `states`/`subdistricts`/`villages` dump,
filter to state code 27, flatten to the five columns above, and re-run the
loader. It reconciles: names are updated, new nodes inserted, nothing is
deleted. A taluka that disappears from LGD stays in the tree, because claims
already reference it.

---

## What is *not* in it: circles

A **circle** (mandal, or *saza*) sits between the taluka and the village. It
is a revenue administrative unit and **LGD does not assign it a code** — LGD
stops at sub-district and resumes at village. The village-to-circle mapping
exists in Settlement Commissioner and district revenue records, not in any
national directory.

This matters because the Circle Officer is the first genuinely independent
reviewer in the assessment chain — the village pair are local to the parcel,
and the Circle Officer is not. Until a district's circles are loaded, no
Circle Officer can be posted there and claims fall through to the Tehsildar.

`circles_maharashtra.csv` now holds **Barshi taluka's ten circles** (Solapur,
LGD 4246) and each circle's headquarters village. Every other taluka in the
state is still missing its circles.

### Barshi's ten circles — sourced 18 September 2026

| Code | Circle | Headquarters village | LGD code |
| --- | --- | --- | --- |
| C-4246-01 | Barshi | Barshi | 562083 |
| C-4246-02 | Agalgaon | Agalgaon | 562019 |
| C-4246-03 | Gaudgaon | Gaudgaon | 562137 |
| C-4246-04 | Khandavi | Khandavi | 562086 |
| C-4246-05 | Nari | Nari (Bhandewadi) | 562060 |
| C-4246-06 | Pangaon | Pangaon | 562077 |
| C-4246-07 | Pangri | Pangri | 562046 |
| C-4246-08 | Surdi | Surdi | 562102 |
| C-4246-09 | Upale Dumala | Upale (Du.) | 562120 |
| C-4246-10 | Vairag | Vairag | 562124 |

**Where the names come from.** Revenue circles are the unit Maharashtra
reports rainfall and declares drought by, so their names appear in government
and press reporting even though no directory lists them. Barshi's ten were
taken from circle-wise rainfall reporting for the taluka
([eSakal, 2021](https://www.esakal.com/paschim-maharashtra/solapur/heavy-rain-barshi-srs97):
बार्शी, आगळगाव, वैराग, पानगाव, सुर्डी, गौडगाव, पांगरी, नारी, उपळेदुमाला,
खांडवी) and cross-checked against drought and crop-insurance reporting that
names Solapur's circles taluka by taluka.

Three checks had to pass before loading, and all three did:

1. **The count reconciles.** Ten circles, which is exactly what
   [Solapur's own tehsil structure](https://solapur.gov.in/en/tehsil-structure/)
   publishes for Barshi. A transcription producing nine or eleven has lost or
   invented one.
2. **Every name is a real village of this taluka**, present in the LGD load
   under sub-district 4246 with the code in the table above. A circle is named
   after and administered from its headquarters village.
3. **No code was invented.** The circle codes are the local `C-<taluka>-<NN>`
   convention, since no national code exists; every village code is LGD's own.

`C-4246-01` keeps its code from the provisional entry it replaces, because a
running deployment already has that row and a code change would orphan
anything posted to it.

### What is still missing: 128 of Barshi's 138 villages

Only the ten headquarters villages are mapped. The other 128 stay directly
under the taluka, which is the honest state -- the village-to-circle mapping is
**still not published anywhere**, re-checked on 18 September 2026 against the
Solapur district site (tehsil structure, documents, rainfall, tahsil pages,
sitemap), PMFBY notified areas, Mahavedh, LGD and the census. Ahilyanagar
[publishes exactly the page needed](https://ahmednagar.nic.in/महसूल-मंडळ/) --
each circle with its villages -- and Solapur publishes no equivalent.

**To finish it**, one sheet from the Barshi tahsil office settles it: the
circle-wise village list (मंडळनिहाय गावांची यादी), which the office holds
because it is how the mandal officers are posted. Add a row per village to
`circles_maharashtra.csv`:

```csv
village,<LGD code>,<village name>,circle,C-4246-<NN>
```

and re-run the loader. The names must match the LGD spelling in the tree; the
loader refuses a village code it has not seen rather than creating one.

Until then Platform → Circle coverage counts the gap, and the ladder falls
through to the Tehsildar for the villages that have no circle above them.

### Where this was looked for, and what was found

Searched on 10 September 2026, and again on 18 September 2026 for Barshi, on the instruction to take whatever is in the
public domain. The mapping is not there. Recorded so the next person does not
repeat the search:

| Source | What it has | Why it is not enough |
| --- | --- | --- |
| [LGD](https://lgdirectory.gov.in) | State, district, sub-district, village | Stops at sub-district and resumes at village. No circle tier at all |
| [Solapur district](https://solapur.gov.in/en/tehsil-structure/) | **Counts** per taluka — 91 circles, 535 sazas, 1,144 villages | Counts only. No circle names, no village mapping |
| District collectorate notices, e.g. [Ahilyanagar](https://ahilyanagar.maharashtra.gov.in/en/notice/notification-list-of-talukas-revenue-circle-talathi-saze-and-villages-under-the-jurisdiction-of-additional-collectorate-ahmadnagar-and-additional-collectorate-shirdi-in-ahmednagar-district/) | Talukas, revenue circles, talathi sazas **and villages** — the real record | One Marathi PDF per district, published irregularly, not machine readable. This is the authoritative source and it has to be transcribed |
| [mahavillages.mahabhumi.gov.in](https://mahavillages.mahabhumi.gov.in/) | Village master with land-records codes | Villages to talukas. No circle tier |
| Open mirrors (DataMeet, ramSeraph/opendata, census handbooks) | Village boundaries and census variables | Derived from LGD and the census, so they inherit the same gap |

**The counts are worth keeping even without the names.** Solapur's own figures
are the check any transcription has to reconcile against:

| Taluka | Circles | Sazas | Villages |
| --- | --- | --- | --- |
| North Solapur | 5 | 25 | 54 |
| Barshi | 10 | 57 | 138 |
| South Solapur | 7 | 40 | 90 |
| Akkalkot | 9 | 54 | 131 |
| Pandharpur | 9 | 54 | 95 |
| Mohol | 8 | 46 | 104 |
| Madha | 9 | 55 | 118 |
| Karmala | 8 | 48 | 118 |
| Mangalwedha | 7 | 43 | 81 |
| Sangola | 9 | 54 | 103 |
| Malshiras | 10 | 59 | 112 |
| **Total** | **91** | **535** | **1,144** |

A transcription of Solapur's circle names that does not produce ten circles in
Barshi has lost or invented one, and should not be loaded. Barshi's ten are
now loaded and reconcile against this table; the other ten talukas do not.

**What is not done, and why.** No circle was *generated* from these counts --
Barshi's were sourced by name and reconciled against them. A
circle named "Barshi Circle 3" would be a real rung above real villages,
routing real claims to an officer posted to a place that does not exist, and
nothing downstream would disagree with it. The platform reports the gap instead
— Platform → Circle coverage counts every village with no Circle Officer above
it — and the ladder falls through to the Tehsildar where the tier is missing,
which is a correct outcome rather than a broken one.

### Adding a district's circles

Two kinds of row, in one file:

```csv
level,code,name,parent_level,parent_code
circle,C-4246-01,Barshi,sub_district,4246
circle,C-4246-02,Vairag,sub_district,4246
village,562111,Malegaon,circle,C-4246-01
village,562112,Someshwar,circle,C-4246-01
```

- **Circle codes are locally assigned**, since no national code exists. The
  convention is `C-<taluka LGD code>-<NN>`. The `C-` prefix guarantees a
  circle code can never be confused with an LGD code, which are all numeric.
- **Village rows re-parent an existing village** under its circle. The village
  must already exist from the LGD load; the loader refuses a village whose
  parent it has not seen rather than creating one.
- A village not listed stays directly under its taluka. That is a valid state,
  not an error — it means the circle for it has not been recorded yet.

### The reconciliation rule

Re-running the LGD load after circles are in place **does not undo them**. The
loader keeps a village under a circle when that circle hangs off the village's
correct taluka. Without that rule, the next routine LGD refresh would silently
unpost every Circle Officer in the state, and it would surface weeks later as
"why can't I see my villages any more".

---

## The rule this data is governed by

**An LGD code is never invented.**

A wrong code does not fail loudly. It attaches a farmer to the wrong village,
and every jurisdiction check downstream then agrees with itself while being
wrong about who may see whose land.

This is not hypothetical. During development, `4247` and `562999` were picked
as obviously-fake test codes. Both are real: `4247` is Solapur North taluka
and `562999` is Khambil Pokale village. The loader corrected the names on its
first run. Codes come from the official export or they do not go in.
