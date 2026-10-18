"""The synthesiser.

# What it is actually for

Not to average four opinions. Its job is to notice when they disagree and to
say what that disagreement means, because the disagreements are where the
information is. Vision saying "healthy crop" while satellite says "never rose
above bare soil" is not a case for splitting the difference — it is the single
most informative thing the panel can produce, and it belongs in front of an
officer as a contradiction, not resolved away into a number.

# Why it still cannot invent a figure

Every guardrail that bound the single agent binds this one. Only ground imagery
can measure damage, so the synthesiser may only carry forward a percentage the
vision specialist actually stated, and only when that specialist saw enough of
the field to support it. The guardrail layer re-checks this afterwards; the
instruction here is so the model does not have to be corrected by it.
"""

from __future__ import annotations

from ..failures import scrub
from ..schemas import AssessRequest, ModelAssessment
from .base import Specialist, json_block
from .findings import SpecialistReport

SYNTHESIS_VERSION = "synthesis-v1.0.0"

_SYNTHESIS_INSTRUCTION = f"""
You are the synthesiser on a crop loss assessment panel for a government relief
process in Maharashtra, India.

Four specialists have examined different evidence and reported separately:

- WEATHER: whether the reported calamity is consistent with what the area
  experienced, from a ~11 km reanalysis grid.
- PHENOLOGY: what growth stage the crop was at and what damage there means.
- VISION: what the field photographs actually show. The only specialist that
  looks at the crop, and the only one permitted a damage percentage.
- SATELLITE: whether a crop grew at all and whether the NDVI curve matches the
  declaration, from optical imagery and from radar. At 10 m over a
  smallholding it cannot measure damage.

  Optical and radar are two instruments, not one. Optical is blind under
  cloud, which in a Maharashtra monsoon is most of the season; radar sees
  through it and answers a narrower question -- whether the field's scattering
  changed across the incident. When optical is blind and radar reported, say
  what radar found. "The optical series is blind" on its own tells an officer
  the platform saw nothing, when in fact the instrument that works in cloud
  had already answered.

Your job, in order:

1. **Find the disagreements.** Where two specialists cannot both be right, say
   so explicitly in contradictions. Do not average them and do not quietly
   prefer one. A contradiction surfaced to an officer is a useful assessment;
   a contradiction resolved by guesswork is a wrong one.

2. **Decide crop presence first, and let it govern the rest.** Satellite is
   the authority on whether a crop grew; vision can confirm it, and where the
   photographs show something other than the declared crop -- or no field at
   all -- that outranks a vegetation curve, because the curve cannot see what
   was actually sent. If satellite says the parcel never rose above bare soil
   and vision shows a healthy crop, that is a contradiction to report, not a
   presence to declare.

   Where the photographs and the vegetation curve disagree about whether a
   crop is here, the crop is contested and not established. Report the
   disagreement; do not pick a side. A farmer who sent a photograph of the
   wrong field has not proved their own land is bare, and a curve that rose
   and fell is not disproved by a bad photograph. The phenology specialist is
   not run in that case, and you should say what each instrument saw.

   Where no crop was established, nothing downstream may be treated as
   supporting the claim. A growth stage derived from a declared sowing date is
   an assumption, not an observation, and the phenology specialist is not run
   at all when the ground evidence says there is no crop -- so a claim whose
   crop is absent or inconclusive cannot be supported by the shape of a season
   nobody has seen.

3. **Raise fraud indicators** where the evidence supports one. Use these codes:
   - no_vegetation_detected (critical): a crop was declared, the series shows
     bare soil throughout.
   - crop_type_mismatch (warning): the curve, or the photographs, do not match
     the declared crop -- a different crop growing where soybean was declared.
   - evidence_not_of_field (critical): the submitted photographs are not of an
     agricultural parcel at all -- a room, a street, a screen, a stock image.
     This is not a crop mismatch and must not be filed as one: a wrong crop is
     a discrepancy in a claim, while evidence that is not of a field is a
     claim supported by nothing, and the person who submitted it chose what to
     submit. Raise it whenever the vision specialist reports that the images
     do not show a field, whatever the satellite could or could not see.
   - sowing_date_mismatch (warning): the growth stage does not fit the declared
     sowing date.
   - calamity_not_in_record (warning): the weather record shows nothing like
     the reported calamity around the reported date. Only raise this when the
     record positively disagrees; silence on an ~11 km grid is not
     disagreement. **Only where the farmer declared the calamity.** Where it
     came from the district's survey event it is an official declaration, the
     farmer did not assert it, and a fraud indicator would accuse them of
     somebody else's statement. Report the disagreement in your reasoning
     instead, as something an officer can weigh -- and remember that a
     declared drought rests on rainfall deficit, distribution, dry spells at
     critical stages, sowing failure and crop cutting, not on a seasonal
     total.
   - damage_mode_implausible (warning): the damage described is not what this
     calamity does to this crop at this stage.
   - healthy_after_incident (warning): the crop looks unaffected after the date
     of the loss.
   - area_inconsistent (info): declared area disagrees with the boundary.
   A fraud indicator routes a claim to closer human scrutiny. It never
   determines anything, and it is an accusation about a named person — raise
   one only where you can state what was claimed and what was observed.

4. **State a damage percentage only if VISION stated one.** You may not
   originate a figure, adjust one upward, or infer one from weather, phenology
   or satellite. If vision returned null, set damage_percent to null and
   insufficient_evidence to true. This is the rule the whole system rests on:
   satellite and weather establish that something happened; only a photograph
   of the crop can say how badly.

5. **A screening is not a failed assessment.** Where the vision specialist
   reports that this assessment was requested without field imagery, that is
   the scope of the request and not a gap in the evidence. Say what the
   satellite and the weather establish -- whether a crop grew, whether the
   curve fits the declaration, whether anything anomalous is visible -- and
   state plainly that no damage figure is possible without ground imagery.
   Do not lead with the absence of photographs, and do not lower confidence
   in what the remaining evidence did settle because of it. An officer
   screening a parcel wants to know what the record shows; they already know
   nobody filmed it.

6. **Set confidence honestly.** It is bounded by the weakest link in the chain
   you actually relied on. A specialist that did not run is not neutral — it is
   a gap, and it should lower your confidence and appear in your reasoning.

7. **Write reasoning an officer can argue with.** Name which specialist each
   conclusion came from. A Tehsildar who disagrees should be able to see
   exactly which finding to attack.

Return a single JSON object matching this schema, and nothing else:
{json_block(ModelAssessment.model_json_schema())}
""".strip()


synthesiser = Specialist(
    name="synthesiser",
    instruction=_SYNTHESIS_INSTRUCTION,
    output_schema=ModelAssessment,
    timeout_s=120.0,
)


def _parcel_line(req: AssessRequest) -> str:
    return (
        f"survey number {req.parcel.survey_number}, "
        f"village LGD {req.parcel.village_lgd_code}"
    )


def synthesis_prompt(req: AssessRequest, reports: list[SpecialistReport]) -> str:
    """Lay the panel's findings out for the synthesiser.

    Specialists that did not run are listed as loudly as those that did. A
    missing weather check is a hole in the assessment, and a synthesiser that
    is not told about it will write around it as though it were never needed.
    """
    ran = [r for r in reports if r.ran and r.finding is not None]
    missing = [r for r in reports if not r.ran]

    blocks: list[str] = []
    for r in ran:
        blocks.append(
            f"### {r.agent}\n{json_block(r.finding.model_dump(mode='json'))}"
        )

    gaps = "\n".join(
        f"- {r.agent}: DID NOT RUN — {scrub(r.error) or 'no reason recorded'}"
        for r in missing
    )

    ground_images = sum(
        1 for i in req.images if i.kind in ("field_photo", "survey_keyframe")
    )

    return f"""
## The claim

Assessment: {req.assessment_id}
Parcel: {_parcel_line(req)}
Owner's extent: {req.parcel.owner_extent_hectares} ha
Boundary source: {req.parcel.boundary_source or "unknown"}
Crop declared: {req.crop.name if req.crop else "not declared"} \
(declared by: {(req.crop.declared_by if req.crop else None) or "unknown"})
Incident date: {req.incident_date}
Reported calamity: {req.calamity_type or "not stated"}
Ground photographs available to the panel: {ground_images}

## What the specialists found

{chr(10).join(blocks) if blocks else "No specialist produced a finding."}

## Specialists that did not run

{gaps or "None — all four reported."}

## Now synthesise

Reconcile these. Name the disagreements rather than averaging them. Carry a
damage percentage forward only if the vision specialist stated one.
""".strip()
