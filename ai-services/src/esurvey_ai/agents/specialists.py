"""The four specialists.

Each one sees a different slice of the evidence and is told, explicitly, what
its slice cannot settle. That boundary is the point: a specialist that is not
shown the photographs cannot form an opinion about the damage, and one that is
not shown the satellite series cannot claim the crop never grew. Disagreements
between them then mean something, because they are disagreements between
different evidence rather than between two readings of the same paragraph.
"""

from __future__ import annotations

from ..schemas import AssessRequest
from ..tools.weather import get_recent_weather
from .base import Specialist, json_block
from .findings import (
    PhenologyFinding,
    SatelliteFinding,
    VisionFinding,
    WeatherFinding,
)

PANEL_VERSION = "panel-v1.0.0"

_COMMON = """
You are one specialist on a panel assessing crop loss for a government relief
process in Maharashtra, India. Three other specialists are examining different
evidence; a synthesiser will reconcile all four.

Rules that bind every specialist:

- Reason only from the evidence you are given. Do not infer facts about
  evidence you were not shown, and never assume another specialist's finding.
- Say what your evidence cannot settle. An honest limitation is worth more to
  the synthesiser than a confident guess, and a guess that survives into a
  final figure is how a wrong payout happens.
- Where a value is marked as mocked or synthetic, treat every conclusion drawn
  from it as inheriting that uncertainty and say so.
- Return a single JSON object matching the schema you were given. No prose
  outside it.
""".strip()


# --------------------------------------------------------------------------
# Weather
# --------------------------------------------------------------------------

_WEATHER_INSTRUCTION = f"""{_COMMON}

You are the WEATHER specialist.

Your job is to establish whether the calamity the farmer reports is consistent
with the weather the area actually experienced in the fourteen days up to the
incident.

You have a tool, get_recent_weather. Call it once with the parcel's latitude,
longitude and the incident date. Do not guess weather you have not fetched.

What to look for:

- Did the reported calamity happen? Hail appears as WMO codes 96 and 99;
  unseasonal rain as precipitation out of season; a cyclone as sustained high
  gusts. Absence in the record is not proof it did not happen — the grid is
  ~11 km and a hailstorm can hit one village and miss the next — but a claim
  with nothing in the record around it is worth flagging.
- What were the antecedent conditions? A week of saturation before a storm is
  often why the storm lodged the crop, and it changes what the damage should
  look like.
- Is the timing right? Weather on a different day to the one claimed matters.

# Who declared it, and what that changes

Where the calamity was declared by the DISTRICT, it is an official
declaration under the state's own process, not an assertion by the farmer.
Say what the record shows and stop there. Do not report it as a claim
contradicted, do not set verdict to "contradicts" on the strength of the
record disagreeing with it, and never raise a fraud indicator over it: the
farmer did not make that statement, and accusing them of it is an accusation
about the wrong person.

This matters most for drought. An official drought declaration is made on
rainfall deficit against normal, the distribution of that rainfall, dry
spells at critical growth stages, sowing failure, groundwater and crop
cutting experiments -- not on a seasonal total. A season with adequate total
rain and no long dry spell does not refute a declared drought; it is one
input among several, and at ~11 km it may not even be this village's rain.
Where the two disagree, report the disagreement as context an officer can
weigh, in those terms.

Where the calamity was declared by the FARMER, it is a claim, and a claim
with nothing in the record around it is worth flagging.

You cannot measure damage and must not estimate any. Set verdict to
"contradicts" only when the record positively disagrees with the claim, not
merely when it is silent.

If the tool returns available: false, set verdict to "not_assessed", say the
source was unreachable in your limitations, and return confidence 0.

Return JSON matching this schema, and nothing else:
{json_block(WeatherFinding.model_json_schema())}
"""


# --------------------------------------------------------------------------
# Phenology
# --------------------------------------------------------------------------

_PHENOLOGY_INSTRUCTION = f"""{_COMMON}

You are the CROP PHENOLOGY specialist.

Your job is to work out what growth stage the crop was at when the loss
happened, and what that means for the damage claimed.

Reason from the crop, the sowing date, the incident date and the season, using
what is agronomically known about that crop in Maharashtra. For example, a
soybean sown in late June is at pod fill by late August; hail at that stage
causes pod shattering and the loss is largely irreversible, whereas the same
hail at the vegetative stage often regrows within a fortnight.

What to establish:

- Days after sowing, and the stage that implies.
- Whether the declared dates are even plausible — a crop reported damaged
  before it could have emerged is a fraud signal, not an assessment.
- Which damage modes this calamity plausibly produces on this crop at this
  stage. If the claimed damage mode does not fit the stage, say so.
- Whether the crop could recover. This is what separates a total loss from a
  setback, and it is a question only phenology can answer.

You have not seen the field or the photographs. You cannot say how much damage
occurred, only what damage would be plausible and what it would mean.

Return JSON matching this schema, and nothing else:
{json_block(PhenologyFinding.model_json_schema())}
"""


# --------------------------------------------------------------------------
# Vision
# --------------------------------------------------------------------------

_VISION_INSTRUCTION = f"""{_COMMON}

You are the FIELD IMAGERY specialist.

You are the only specialist looking at photographs of the field, and therefore
the only one who may state a damage percentage. That is a responsibility, not a
licence.

What to establish:

- Are these usable photographs of a crop field at all? Say plainly if an image
  is out of focus, of the sky, of a different subject, or too tight to judge
  anything from. A synthetic or placeholder image is not evidence; say so.
- What crop is it, and does that match what was declared?
- What damage is visible, in agronomic terms: lodging, shattering,
  defoliation, submersion, scorch, pest damage, or none.
- How much of the crop in view is lost.

On the percentage: give one only when the images genuinely show enough of the
field to support it. One close-up of a single plant does not; it tells you the
damage mode, not its extent. Returning null with a clear reason is the correct
answer far more often than it feels, and a figure invented from a photograph
that does not show the field is the single worst failure this panel can produce.

Return JSON matching this schema, and nothing else:
{json_block(VisionFinding.model_json_schema())}
"""


# --------------------------------------------------------------------------
# Satellite
# --------------------------------------------------------------------------

_SATELLITE_INSTRUCTION = f"""{_COMMON}

You are the SATELLITE specialist.

You are reading a Sentinel-2 NDVI series over the parcel, at 10 m resolution.

What that resolution can settle:

- Whether a crop demonstrably grew here at all. A series that never rises
  meaningfully above bare soil (NDVI ~0.12) on a parcel where a crop was
  declared is the strongest fraud signal in this whole system.
- Whether the shape and timing of the curve match the declared crop and sowing
  date. A curve peaking two months away from where the declaration implies is
  a real discrepancy and worth stating.
- Whether there is a fall consistent with the incident date.

What it cannot settle: how badly the crop was damaged. A one- or two-hectare
smallholding is a handful of pixels, and a figure derived from that alone would
be a guess wearing a decimal point. Do not produce one.

Weigh the cloud gaps honestly. A sixty-day gap around the incident means the
series is blind at exactly the moment that matters, and a confident reading of
a curve either side of that gap is not warranted.

Return JSON matching this schema, and nothing else:
{json_block(SatelliteFinding.model_json_schema())}
"""


# --------------------------------------------------------------------------
# Construction
# --------------------------------------------------------------------------

weather_specialist = Specialist(
    name="weather_specialist",
    instruction=_WEATHER_INSTRUCTION,
    output_schema=WeatherFinding,
    tools=[get_recent_weather],
    timeout_s=90.0,
)

phenology_specialist = Specialist(
    name="phenology_specialist",
    instruction=_PHENOLOGY_INSTRUCTION,
    output_schema=PhenologyFinding,
    timeout_s=60.0,
)

vision_specialist = Specialist(
    name="vision_specialist",
    instruction=_VISION_INSTRUCTION,
    output_schema=VisionFinding,
    # Longest of the four: it is the only one carrying image payloads.
    timeout_s=120.0,
)

satellite_specialist = Specialist(
    name="satellite_specialist",
    instruction=_SATELLITE_INSTRUCTION,
    output_schema=SatelliteFinding,
    timeout_s=60.0,
)


# --------------------------------------------------------------------------
# The slice of evidence each one is given
# --------------------------------------------------------------------------


# --------------------------------------------------------------------------
# The grounding packet
# --------------------------------------------------------------------------
#
# The Go service assembles what a damage figure can stand on and sends it as
# an open mapping. Each specialist is given only the parts it can reason
# about -- the same slicing principle the rest of this module follows -- but
# every one of them is told what could not be gathered.
#
# That last part is the point. A specialist shown only what arrived cannot
# tell a well-grounded assessment from an ungrounded one, and will write with
# the same confidence either way.


def _ev(req: AssessRequest, *path: str):
    """One branch of the packet, or None."""
    node = req.evidence
    for key in path:
        if not isinstance(node, dict):
            return None
        node = node.get(key)
    return node


def _declared_by(req: AssessRequest) -> str:
    """Who said this calamity happened, where that is known.

    Written beside the calamity rather than in a separate section, because it
    changes what the calamity *is*: a claim to be checked, or a declaration to
    be worked within.
    """
    who = (req.calamity_declared_by or "").strip()
    if who == "district":
        return (
            " (officially declared by the district for this area, not "
            "asserted by the farmer)"
        )
    if who == "farmer":
        return " (as claimed by the farmer)"
    return ""


def _absent_block(req: AssessRequest) -> str:
    """What could not be gathered, in the words the service used."""
    absent = _ev(req, "absent")
    if not absent:
        if req.evidence is None:
            return (
                "No grounding evidence was assembled for this assessment. Treat "
                "every figure below as resting on the claim and the imagery "
                "alone."
            )
        return "Everything the service tried to gather was available."
    lines = "\n".join(f"- {a}" for a in absent)
    return f"Could NOT be gathered for this assessment:\n{lines}"


def _grounding_prompt(req: AssessRequest) -> str:
    """The standing instruction about the packet, for every specialist."""
    return f"""
{_absent_block(req)}

Anything in that list is a gap in the evidence, not a finding of absence. Say
in your limitations which of them bear on your conclusion, and do not write
around a gap as though it were not there.
""".strip()


def _baseline_block(req: AssessRequest) -> str:
    d = _ev(req, "denominators")
    if not isinstance(d, dict):
        return (
            "No baseline was available: this season is not measured "
            "against anything."
        )

    parts = []
    if isinstance(d.get("parcel_history"), dict):
        parts.append("This parcel's own prior seasons:")
        parts.append(json_block(d["parcel_history"]))
    if isinstance(d.get("neighbourhood"), dict):
        parts.append("The surrounding fields over the same window:")
        parts.append(json_block(d["neighbourhood"]))
    if isinstance(d.get("threshold_yield"), dict):
        parts.append("Issued threshold yield for this unit:")
        parts.append(json_block(d["threshold_yield"]))
    if not parts:
        return (
            "No baseline was available: this season is not measured "
            "against anything."
        )
    return "\n".join(parts)


def _radar_block(req: AssessRequest) -> str:
    r = _ev(req, "radar")
    if not isinstance(r, dict):
        return "No radar reading was available."
    gap = _ev(req, "radar_passes_in_optical_gap")
    head = ""
    if gap:
        head = (
            f"Radar observed this field {gap} time(s) during the stretch the "
            f"optical series could not see.\n"
        )
    return head + json_block(r)


def _moisture_block(req: AssessRequest) -> str:
    """Leaf water and soil moisture, with what they bear on stated first.

    The relevance sentence leads because this evidence is strong for one
    calamity and weak for most others, and a specialist handed an NDMI anomaly
    during a hailstorm claim will otherwise reason as though it bore on
    breakage. The service computes that sentence per calamity; the prompt
    only has to put it before the numbers.
    """
    m = _ev(req, "moisture")
    if not isinstance(m, dict):
        return "No moisture reading was available."
    head = []
    relevance = m.get("relevance")
    if isinstance(relevance, str) and relevance:
        head.append(f"How much this bears on the calamity claimed: {relevance}")
    if m.get("synthetic"):
        head.append(
            "THIS RECORD WAS GENERATED FOR TESTING AND IS NOT AN OBSERVATION. "
            "Do not rely on it."
        )
    return "\n".join([*head, json_block(m)])


def _agronomy_block(req: AssessRequest) -> str:
    parts = []
    for label, key in (
        ("Thermal time and growth stage at the incident", "thermal"),
        ("What this calamity costs at that stage", "susceptibility"),
        ("Heat and cold stress over the season", "stress"),
        (
            "The declared sowing date, checked against observed emergence",
            "sowing_check",
        ),
    ):
        node = _ev(req, key)
        if isinstance(node, dict):
            parts.append(f"{label}:")
            parts.append(json_block(node))
    if not parts:
        return (
            "No agronomic derivation was available: the growth stage below is "
            "yours to establish from the dates alone."
        )
    return "\n".join(parts)


def _weather_block(req: AssessRequest) -> str:
    w = _ev(req, "weather")
    if not isinstance(w, dict):
        return ""
    return (
        "The service has already fetched and summarised the record. These "
        "figures are computed deterministically and are the ones that will be "
        "kept with the assessment:\n" + json_block(w)
    )


def _official_rainfall_block(req: AssessRequest) -> str:
    """Rainfall as the state's own gauges recorded it, against the normal.

    The figure a drought determination is actually made of, and the one this
    platform had no source for at all. Two things it supplies that the
    reanalysis cannot:

    The amount. ERA5 reported 505 mm for Barshi's 2026 kharif where the state's
    gauges recorded 286 mm -- a model spreading convection over an
    eleven-kilometre cell against a network of rain gauges.

    The comparison. A deficit is measured against the long-period average for
    that place, and nothing else in this request carries one. "505 mm" reads as
    an adequate season; 286 mm of a normal 516.6 mm is 55%, and the month the
    crop was filling grain was 16% of its normal.
    """
    o = _ev(req, "official_rainfall")
    if not isinstance(o, dict):
        return (
            "No published rainfall figure was available for this unit, so the "
            "season's rain is known only from reanalysis and there is no normal "
            "to compare it against. Say so rather than treating the reanalysis "
            "total as a measured amount."
        )
    return (
        "Rainfall as the state itself recorded it, for the administrative unit "
        "this parcel sits in, with the long-period normal beside each figure. "
        "These are rain-gauge readings published by the state, not model "
        "output:\n" + json_block(o)
    )


def _fieldwork_block(req: AssessRequest) -> str:
    m = _ev(req, "field_measurements")
    if not isinstance(m, dict):
        return "No officer measurements were recorded."
    return json_block(m)


def _ground_cv_block(req: AssessRequest) -> str:
    """What the survey clips were measured at, frame by frame.

    A deterministic reading of the same footage this specialist is looking
    at: stem angle, the fraction of the canopy that is lodged, submerged or
    necrotic, and how much of each frame was crop at all. The fusion that
    produces the platform's damage figure weights it heavily.

    It was not shown to this specialist until now, so the model formed its own
    impression of the clips and the measurement sat beside it, and the two
    could not corroborate or contradict each other. Where they disagree, that
    disagreement is worth more to an officer than either number alone.
    """
    cv = _ev(req, "ground_cv")
    if not isinstance(cv, dict):
        return "The clips were not measured."
    return json_block(cv)


def _point_ndvi_block(req: AssessRequest) -> str:
    """The optical index at the spots the farmer actually filmed.

    The same analysis as the parcel-wide series, over a 20 m patch around each
    point of the walk, clipped to the boundary. It is an addition: the field
    series, its peak and its relative loss are unchanged and remain what the
    platform's figure derives from.

    It exists because a field mean cannot answer the question that matters most
    about farmer-supplied footage -- whether the ground a clip came from looks,
    from orbit, like what the clip shows. Averaged over the whole parcel, a clip
    of a ruined crop taken where the satellite sees vigour disappears entirely.
    Read at the point, it is the strongest signal this platform holds, because
    the usual explanation for a mismatch (the satellite averages the whole
    field) is unavailable: this is that ground.

    Every reading carries the area it rests on and whether it is marked weak.
    A weak reading is one taken over too little ground, from too few passes, or
    at a position outside the boundary, and nothing may be concluded from it.
    """
    pn = _ev(req, "point_ndvi")
    if not isinstance(pn, dict):
        return (
            "The optical index was not read at the individual capture points, "
            "so the footage has been compared only against the field mean."
        )
    return json_block(pn)


def _history_block(req: AssessRequest) -> str:
    h = _ev(req, "claim_history")
    if not isinstance(h, dict):
        return ""
    signals = h.get("signals") or []
    if not signals:
        return ""
    return (
        "What has been claimed on this land before. Every signal carries the "
        "ordinary explanation next to it; these route a claim to closer "
        "attention and determine nothing:\n" + json_block(signals)
    )


def _parcel_line(req: AssessRequest) -> str:
    return (
        f"survey number {req.parcel.survey_number}, "
        f"village LGD {req.parcel.village_lgd_code}"
    )


def _days_line(req: AssessRequest) -> str:
    if req.window_days is None:
        return "not computed"
    return str(req.window_days)


def _extent_line(req: AssessRequest) -> str:
    return (
        f"survey number {req.parcel.survey_number}, "
        f"owner's extent {req.parcel.owner_extent_hectares} ha"
    )


def _declared_sowing(req: AssessRequest) -> str:
    return (
        (req.crop.sowing_date if req.crop else None)
        or req.sowing_date
        or "not declared"
    )


def weather_prompt(req: AssessRequest) -> str:
    lat = req.parcel.centroid_lat
    lon = req.parcel.centroid_lon
    return f"""
Parcel: {_parcel_line(req)}.
Centroid: latitude {lat}, longitude {lon}.
Incident date: {req.incident_date}
Reported calamity: {req.calamity_type or "not stated"}{_declared_by(req)}
Crop declared: {req.crop.name if req.crop else "not declared"}

{_official_rainfall_block(req)}

{_weather_block(req)}

Which of those two answers which question:

- **How much rain fell, and how far below normal it was** -- the state's
  published figure above. It is gauge data and it carries the normal. Quote it
  for any statement about amount or deficiency, and quote the unit it is for.
- **Which days the rain fell on** -- the reanalysis. Dry spells, heat runs,
  gusts and hail coding come from there and nowhere else, because the state
  publishes no daily figures for the current year.

Where the two disagree on the amount, the state's gauges are right and the
reanalysis is a known wet bias over this region. Do not average them, and do not
present the reanalysis total as a measurement. Where only the reanalysis is
present, say that the amount is unverified against gauge data.

A seasonal percentage of normal hides the thing that kills a crop. Read the
month-by-month figures: two consecutive months at a fifth of their normal during
grain fill is a crop failure inside a season that averages out at "poor".

What the satellites measured of the water actually in the crop and the soil:
{_moisture_block(req)}

{_grounding_prompt(req)}

A dry spell in the rainfall record and a canopy that held its water are not the
same event: the field may be irrigated, or the soil may have carried it through.
Where the moisture reading and the rainfall disagree, say so, and say which you
rely on for what.

Where the summarised record above is present, reason from it: it covers the
whole season rather than a fortnight, and it is what will be kept with the
assessment. Call get_recent_weather only to check a specific day it does not
answer, or when no summary was supplied.

Report whether the reported calamity is consistent with what the record shows,
and say explicitly if the season had already been through a long dry spell or
a run of days above the crop's heat threshold before the incident -- a crop
that had lost yield before the calamity arrived is a different assessment from
one that had not.
{_declaration_rule(req)}
{_reanalysis_caveat(req)}
""".strip()


def _declaration_rule(req: AssessRequest) -> str:
    """What outranks what, when a district has already decided.

    A farmer's assertion is a claim to check. A district's declaration is not:
    the Collector has declared drought for this area under the state's own
    process, and this specialist is reading an eleven-kilometre reanalysis grid.
    If the two disagree the honest finding is about the instrument, not about
    the farmer -- and "contradicts" against a named person is an accusation the
    grid cannot support.

    Written as a rule rather than left to judgement because the model had no
    reason to infer it. Barshi, September 2026: a declared drought came back as
    "contradicts" on all four specialists.
    """
    if (req.calamity_declared_by or "").strip() != "district":
        return ""
    return """
This calamity was declared by the district, not asserted by the farmer. That
changes what a disagreement with the record means. You are reading a coarse
reanalysis grid; the district declared this after its own process over ground
you cannot see.

So: do not return "contradicts" on the strength of the gridded record alone.
Where the record does not show what was declared, the finding is a limitation
of the instrument -- "the reanalysis grid over this cell does not show it" --
and belongs in your limitations, not as a contradiction of the claim. Reserve
"contradicts" for a positive disagreement the grid can actually carry: heavy
rain recorded through a declared dry spell, for instance, rather than the
absence of a signal.

What remains fully yours to report is whether *this parcel* looks like it
suffered what the district declared. A declaration covers an area; it does not
establish that every field in it lost a crop.
""".rstrip()


def _reanalysis_caveat(req: AssessRequest) -> str:
    """Why the seasonal total may read better than the season was.

    The grid resolves convection by averaging it over its cell, so a downpour
    on one village becomes a thin film over all of them. The seasonal total
    then looks adequate while individual days that were dry on the ground
    carry a millimetre or two. The deterministic summary now counts those days
    separately; this says what the count means.
    """
    w = _ev(req, "weather")
    if not isinstance(w, dict):
        return ""
    season = w.get("season")
    if not isinstance(season, dict):
        return ""
    rain_days = season.get("rain_days") or 0
    trace_days = season.get("trace_rain_days") or 0
    if not rain_days or trace_days < rain_days:
        return ""
    return f"""
Read the seasonal rainfall total as an upper bound. Of this season's
{rain_days} rain days, a further {trace_days} days held only a trace (under
2.5 mm) -- more trace days than rain days. That is characteristic of a
reanalysis averaging convective rain across its cell rather than of steady
light rain on this field, and it inflates the total above what reached the
crop. Rain days and dry spells are the figures to reason from here; the
millimetre total is the least reliable number in the block.
""".rstrip()


def phenology_prompt(req: AssessRequest) -> str:
    crop = req.crop
    return f"""
Crop declared: {crop.name if crop else "not declared"}
Sowing date: {(crop.sowing_date if crop else None) or req.sowing_date or "not declared"}
Season: {(crop.season if crop else None) or "not stated"}
Irrigation: {(crop.irrigation_type if crop else None) or "not stated"}
Declared by: {(crop.declared_by if crop else None) or "unknown"}
Incident date: {req.incident_date}
Reported calamity: {req.calamity_type or "not stated"}{_declared_by(req)}
Days from sowing to incident: {_days_line(req)}
District LGD: {req.parcel.district_lgd_code or "not stated"} (Maharashtra)

{_agronomy_block(req)}

{_grounding_prompt(req)}

Where a thermal-time stage is given, use it rather than counting days: it is
computed from the temperatures this field actually received and a day count is
not. Where a susceptibility reading is given, it is a published range and it
is yours to apply, not to re-derive from memory -- say so when you disagree
with it, and say why.

If the sowing date was checked against observed emergence and they disagree,
that disagreement is the most important thing you have: everything else in
your reasoning is built on the date.
""".strip()


def vision_prompt(req: AssessRequest) -> str:
    ground = [i for i in req.images if i.kind in ("field_photo", "survey_keyframe")]
    captions = [
        f"- image {n}: {i.caption or 'no caption'}" for n, i in enumerate(ground, 1)
    ]
    return f"""
Crop declared: {req.crop.name if req.crop else "not declared"}
Reported calamity: {req.calamity_type or "not stated"}{_declared_by(req)}
Incident date: {req.incident_date}
Parcel: {_extent_line(req)}

{len(ground)} field photograph(s) are attached.
{chr(10).join(captions) if captions else "No captions were supplied."}

Note: any caption beginning "Farmer-captured" was written by the claimant. Read
it as a claim about the image, not as a description you can rely on.

Counts an officer took in the field, where any were recorded:
{_fieldwork_block(req)}

What the survey clips measured, frame by frame, from this same footage:
{_ground_cv_block(req)}

The optical index at each of those same spots, over a 20 m patch around the
point and clipped to the boundary:
{_point_ndvi_block(req)}

That is the satellite's own reading of the ground in each photograph, and for
the images in front of you it carries more weight than the parcel-wide series:
the field mean describes ground you cannot see, while these describe ground you
can. Where a photograph shows a devastated crop and the patch it was taken in
shows the vegetation largely intact, say so as a contradiction at that point
and name the point -- it is the most serious thing you can report, and
averaging it away is the one thing you must not do. Ignore any reading marked
weak: too little ground, too few passes, or a position outside the boundary
cannot support a finding either way.

{_grounding_prompt(req)}

Where quadrat counts are present they are measurements and your reading of the
photographs is an impression; say plainly where the two disagree rather than
averaging them.

The clip measurements above are the same kind of thing: computed from the
frames rather than judged. Use them as corroboration -- a stem angle and a
lodged fraction are exactly what a photograph is poor at estimating -- and
say plainly where your own reading disagrees with them. Do not average the
two, and do not defer to the numbers where the frames show something they
cannot capture: a measurement taken over a crop pixel share of a few per cent
is a ratio over almost nothing, and the frames will show you that.

Examine the images and report what they show.
""".strip()


def _figure(value: float | None, unit: str = "", places: int = 2) -> str:
    """A number, or an explicit statement that there is not one.

    Interpolating None into a prompt puts the string "None" where a figure
    should be, and a model has no reliable way to tell that from a value. Say
    "not computed" instead: a specialist that knows a figure is missing can
    reason about the gap, which is the whole point of the limitations field.
    """
    if value is None:
        return "not computed"
    return f"{value:.{places}f}{unit}"


def satellite_prompt(req: AssessRequest) -> str:
    sat = req.satellite
    series = [{"date": p.date, "ndvi": round(p.ndvi, 4)} for p in sat.time_series]
    return f"""
Crop declared: {req.crop.name if req.crop else "not declared"}
Sowing date declared: {_declared_sowing(req)}
Incident date: {req.incident_date}
Boundary source: {req.parcel.boundary_source or "unknown"}

NDVI series, cloud-free passes only:
{json_block(series)}

Derived figures:
- peak NDVI {_figure(sat.baseline_ndvi)} on {sat.baseline_date or "an unrecorded date"}
- latest NDVI {_figure(sat.incident_ndvi)}
- change {_figure(sat.delta_ndvi)}
- relative loss toward bare soil {_figure(sat.rrvl_percent, "%", 1)}
- {sat.observation_count} usable observations
- largest gap between passes: {sat.largest_gap_days} days

What this parcel normally does, and what the fields around it did:
{_baseline_block(req)}

Radar, which sees through cloud:
{_radar_block(req)}

Leaf water (Sentinel-2 NDMI) and surface soil moisture (Sentinel-1), each
against this field's own prior seasons and the cropland around it:
{_moisture_block(req)}

{_history_block(req)}

{_grounding_prompt(req)}

The change above is only a loss if it is a change from what this land normally
manages. Where prior seasons are given, judge the fall against them and say so;
where the surrounding fields fell too, say that the event is corroborated by
ground the claimant does not control; where they did not, say that is a reason
to look and not a finding.

The same index at the spots the farmer filmed, over a 20 m patch around each
point and clipped to the boundary:
{_point_ndvi_block(req)}

Weigh those point readings above the field mean whenever you are judging the
farmer's footage, because they are the only figures here that describe the
ground the footage actually came from. Weigh the field mean above them for
everything else: the parcel's own loss, its trend and its comparison with prior
seasons are the field's question and the patches are too small to answer it.
Never restate a point reading as the parcel's figure.

A point where the footage measures serious damage and the patch around it shows
little or no loss is the most serious finding available to you, and you must
report it as a contradiction at that point rather than averaging the two. Say
so only for readings not marked weak: a patch of a few hundred square metres,
or one covered by a single pass, cannot carry an allegation. Where the readings
came out materially worse than the field, say the footage is not representative
of the parcel -- that is a statement about sampling, not about honesty.

Where radar is present it is the stronger evidence for the days the optical
series could not see, and a radar reading of no change through a cloud gap is
worth stating as plainly as a damage signature.

Radar also bears on whether a crop is there at all, and not only on whether it
changed. Backscatter over a standing crop differs from bare soil -- a canopy
scatters where bare ground reflects away -- so where the optical series is
blind, say what the radar indicates about crop presence and set crop_present
from it rather than leaving presence unsettled because the optical could not
see. A blind optical series with radar available is not "we could not look";
it is "the instrument that works in cloud looked, and this is what it saw".
Where the radar is itself inconclusive about presence, say that instead.

Leaf water falls before greenness does, so a canopy water finding can show a
crop short of water while the NDVI above still looks healthy -- say so when the
two disagree, rather than letting the greener index win by default. It measures
water, not loss: never turn an NDMI figure into a damage percentage. A field
drier than its own history but level with its neighbours is a dry locality,
which corroborates the event and does not single out this field; a field much
wetter than its neighbours in a dry spell is usually irrigated.

Report what this series can and cannot establish.
""".strip()
