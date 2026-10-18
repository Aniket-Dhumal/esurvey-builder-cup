"""Wire contract between the Go backend and the assessment agent.

Field names match the Go structs exactly. Changing one without the other is
the most likely way to break this integration, so both sides are versioned
together and the round trip is covered by tests.
"""

from __future__ import annotations

import base64
import binascii
from enum import StrEnum
from typing import Any, Literal

from pydantic import (
    BaseModel,
    Field,
    PrivateAttr,
    field_validator,
    model_validator,
)


class Severity(StrEnum):
    """Loss tiers from the specification's remote-sensing classification."""

    NEGLIGIBLE = "Negligible"
    MODERATE = "Moderate"
    SEVERE = "Severe"
    CATASTROPHIC = "Catastrophic"


class CropPresence(StrEnum):
    """What the NDVI series says about whether a crop existed at all.

    This — not damage quantification — is what a 10 m optical series over a
    smallholding can actually establish.
    """

    CONFIRMED = "confirmed"
    ABSENT = "absent"
    INCONCLUSIVE = "inconclusive"


class FraudSeverity(StrEnum):
    INFO = "info"
    WARNING = "warning"
    CRITICAL = "critical"


class FraudIndicator(BaseModel):
    """A discrepancy between what was claimed and what the record shows.

    These do not decide anything. They route a claim to closer human scrutiny,
    which is the only place an accusation should ever be made.
    """

    code: str = Field(
        description="Stable identifier, e.g. no_vegetation_detected, "
        "crop_type_mismatch, sowing_date_mismatch, healthy_after_incident."
    )
    severity: FraudSeverity
    detail: str = Field(description="What was claimed, what was observed.")


class ImageKind(StrEnum):
    NDVI_MID = "ndvi_mid"
    NDVI_LAST = "ndvi_last"
    TERRAIN_SLOPE = "terrain_slope"
    TERRAIN_ASPECT = "terrain_aspect"
    FIELD_PHOTO = "field_photo"
    SURVEY_KEYFRAME = "survey_keyframe"


#: The largest data URI this service will decode.
#:
#: A capture is capped at 8 MiB by the API that collects it, and base64 costs
#: about a third on top, so 12 MiB admits every legitimate photograph with room
#: to spare.
#:
#: The bound exists here as well as there because this is where the decoding
#: happens: decode_image turns the payload into bytes in a container limited to
#: one gigabyte, and a caller that sent a larger one would take assessments
#: down for every district at once. That the only caller today is trusted and
#: already bounds its own input is a fact about this deployment, not a property
#: of the code.
MAX_DATA_URI_CHARS = 12 * 1024 * 1024

#: The most images one assessment may carry.
#:
#: A report has five photographs and four station stills. Sixteen is generous
#: and still bounds the total payload; without it, a thousand small images cost
#: as much as one enormous one.
MAX_IMAGES = 16

#: How many keyframes one clip is measured from.
#:
#: Eight is a walk seen end to end; more is the same plants again at the price
#: of a larger request and a slower model call.
MAX_GROUND_KEYFRAMES = 8


class Image(BaseModel):
    """One piece of visual evidence, inline as a data URI."""

    kind: ImageKind
    date: str | None = None
    data_uri: str = Field(
        description="data:image/png;base64,...",
        max_length=MAX_DATA_URI_CHARS,
    )
    caption: str | None = None


class NdviPoint(BaseModel):
    date: str
    ndvi: float = Field(ge=-1, le=1)


class Parcel(BaseModel):
    survey_number: str
    farm_id: str
    village_lgd_code: str
    district_lgd_code: str | None = None

    owner_extent_hectares: float = Field(
        description="This owner's share. Loss is apportioned over this, "
        "not over the polygon."
    )
    polygon_area_hectares: float | None = Field(
        default=None,
        description="Area of the traced boundary, which covers the whole "
        "survey number and may exceed owner_extent on a jointly held khata.",
    )
    centroid_lat: float | None = None
    centroid_lon: float | None = None
    boundary_source: str | None = Field(
        default=None,
        description="traced | agristack | bhunaksha | mock. The agent is told "
        "when a boundary is synthetic.",
    )


class Crop(BaseModel):
    name: str
    sowing_date: str | None = None
    season: str | None = None
    year: str | None = None
    irrigation_type: str | None = None
    declared_by: str | None = Field(
        default=None,
        description="agristack | farmer | mock. Provenance matters: a "
        "farmer-declared crop is weaker evidence than a registry one.",
    )


class SatelliteEvidence(BaseModel):
    time_series: list[NdviPoint] = Field(default_factory=list)
    baseline_ndvi: float | None = None
    baseline_date: str | None = None
    incident_ndvi: float | None = None
    delta_ndvi: float | None = None
    rrvl_percent: float | None = None
    observation_count: int = 0
    largest_gap_days: int = 0


class TerrainEvidence(BaseModel):
    mean_slope_degrees: float
    classification: str
    waterlogging_risk: str
    elevation_mean_m: float | None = None
    elevation_min_m: float | None = None
    elevation_max_m: float | None = None
    aspect_cardinal: str | None = None


class AssessRequest(BaseModel):
    """Everything the agent is allowed to reason over."""

    assessment_id: str
    incident_date: str
    parcel: Parcel
    crop: Crop | None = None
    satellite: SatelliteEvidence
    terrain: TerrainEvidence | None = None
    images: list[Image] = Field(default_factory=list, max_length=MAX_IMAGES)

    calamity_declared_by: str | None = Field(
        default=None,
        description=(
            "Who said this calamity happened: 'farmer' from a claim, "
            "'district' from a declared survey event. A farmer's assertion "
            "is checked against the record; a district's declaration is an "
            "administrative act this platform does not overrule."
        ),
    )

    ground_expected: bool = Field(
        default=True,
        description=(
            "Whether field imagery was ever meant to accompany this "
            "assessment. False for a screening requested against a parcel "
            "from the console, where nobody has filmed anything and nobody "
            "was asked to: the absence of photographs is then the scope of "
            "the request, not a gap in the evidence."
        ),
    )

    # What the farmer says happened, from the filed report. The weather and
    # phenology specialists cannot do their job without it: whether the record
    # bears the claim out, and whether this damage mode fits the growth stage,
    # are both questions about a named calamity.
    calamity_type: str | None = None
    loss_category: str | None = Field(
        default=None,
        description="standing_crop or post_harvest; they admit different calamities.",
    )

    sowing_date: str | None = None
    window_days: int | None = None

    # Everything the figure can be grounded in: the parcel's own prior
    # seasons, the fields around it, radar that saw through cloud, the
    # season's weather, where the crop stood in its cycle, what an officer
    # counted, and what has been claimed here before.
    #
    # Deliberately an open mapping rather than a modelled tree. The Go
    # service owns this shape and will add to it; a strict model here would
    # reject a packet for carrying a field this side had not been taught
    # about yet, which is the wrong way round -- the panel should degrade
    # when it does not understand something, not refuse the assessment.
    #
    # It carries its own "absent" list, and that list is the most important
    # thing in it: a packet that gathered nothing and one that gathered
    # everything are the same size to a reader shown only what arrived.
    evidence: dict[str, Any] | None = None

    @field_validator("images")
    @classmethod
    def _cap_images(cls, v: list[Image]) -> list[Image]:
        # A runaway caller could otherwise push a hundred keyframes into one
        # prompt. Twelve is generous for the evidence we actually produce.
        if len(v) > 12:
            raise ValueError(f"too many images: {len(v)} (max 12)")
        return v


class ModelAssessment(BaseModel):
    """Exactly what the model is asked to produce.

    Passed to ADK as the agent's output_schema. Provenance fields are absent
    deliberately — model id and prompt version are ours to record, not the
    model's to assert, and asking invites a hallucinated answer.
    """

    crop_presence: CropPresence = Field(
        description="Whether the NDVI series shows a crop was grown at all. "
        "This is what satellite can establish; damage is not."
    )
    crop_presence_reasoning: str = Field(
        description="One or two sentences on what the series shows about the "
        "growing season."
    )
    fraud_indicators: list[FraudIndicator] = Field(
        default_factory=list,
        description="Discrepancies between the claim and the observed record. "
        "Empty when the claim is consistent.",
    )

    insufficient_evidence: bool = Field(
        description="True when no damage figure can be supported. Expected "
        "whenever there is no ground imagery."
    )
    confidence: float = Field(description="0 to 1.")
    reasoning: str = Field(
        description="Three to six sentences of plain prose naming the "
        "evidence relied on."
    )
    damage_percent: float | None = Field(
        default=None,
        description="0 to 100, from GROUND IMAGERY only. Null whenever there "
        "is no ground imagery — satellite alone cannot quantify damage.",
    )
    severity: Severity | None = None
    evidence_cited: list[str] = Field(default_factory=list)
    contradictions: list[str] = Field(default_factory=list)


class RawAssessment(BaseModel):
    """The model's response, before guardrails.

    Deliberately unbounded where AssessResult is bounded. A model returning
    120% should be clamped and flagged, not rejected outright — rejecting it
    loses the reasoning along with the bad number, and the reasoning is often
    still worth an officer's attention.
    """

    damage_percent: float | None = None
    severity: Severity | None = None
    confidence: float = 0.0
    insufficient_evidence: bool = False
    reasoning: str = ""
    crop_presence: CropPresence = CropPresence.INCONCLUSIVE
    crop_presence_reasoning: str = ""
    fraud_indicators: list[FraudIndicator] = Field(default_factory=list)
    evidence_cited: list[str] = Field(default_factory=list)
    contradictions: list[str] = Field(default_factory=list)
    model: str = ""
    prompt_version: str = ""
    guardrails_applied: list[str] = Field(default_factory=list)


class AssessResult(BaseModel):
    """What the service returns, after guardrails. Advisory — officers
    adjudicate."""

    damage_percent: float | None = Field(
        default=None,
        ge=0,
        le=100,
        description="Null when insufficient_evidence is true. An absent "
        "figure and a zero are different claims.",
    )
    severity: Severity | None = None
    confidence: float = Field(ge=0, le=1)

    insufficient_evidence: bool = Field(
        description="True when no damage figure can be supported. This is a "
        "first-class outcome, not a failure — and the normal one until ground "
        "imagery exists."
    )

    crop_presence: CropPresence = Field(
        default=CropPresence.INCONCLUSIVE,
        description="What the NDVI series establishes about the growing "
        "season. Satellite's actual contribution.",
    )
    crop_presence_reasoning: str = ""
    fraud_indicators: list[FraudIndicator] = Field(
        default_factory=list,
        description="Claim-versus-record discrepancies for human scrutiny. "
        "Never a determination of fraud.",
    )

    reasoning: str = Field(
        description="What the evidence shows, in language a revenue officer "
        "can read and disagree with."
    )
    evidence_cited: list[str] = Field(default_factory=list)
    contradictions: list[str] = Field(
        default_factory=list,
        description="Where signals disagree. Silence here on conflicting "
        "inputs is itself a warning sign.",
    )

    # Provenance. An assessment that cannot be reproduced cannot be defended
    # in a grievance hearing.
    model: str
    prompt_version: str
    guardrails_applied: list[str] = Field(default_factory=list)


class SpecialistSummary(BaseModel):
    """One specialist's run, as reported to the backend."""

    agent: str
    ran: bool
    duration_ms: int = 0
    error: str | None = None
    verdict: str | None = None
    confidence: float | None = None
    reasoning: str | None = None
    limitations: list[str] = Field(default_factory=list)


class AssessResponse(BaseModel):
    assessment_id: str
    result: AssessResult
    latency_ms: int

    # What each specialist did. Carried back so an officer can see which
    # evidence was actually examined and which was missing -- an assessment
    # made without the weather check is a different assessment, and the
    # difference should not be invisible.
    specialists: list[SpecialistSummary] = Field(default_factory=list)
    degraded: bool = False
    degraded_reason: str | None = None


class GroundMeasurement(BaseModel):
    """What the model must return for one survey clip."""

    crop_visible: bool = Field(
        description=(
            "True only if a crop canopy is actually visible in the frames. "
            "False for footage of a path, a bund, the sky, an empty field or "
            "anything else that is not the claimed crop."
        )
    )

    stem_angle_degrees: float = Field(
        ge=0,
        le=90,
        description=(
            "Angle of the dominant stems from vertical, in degrees. 0 is "
            "perfectly erect, 90 is flat on the ground. Judge the stems, not "
            "the camera: a tilted camera does not lodge a crop."
        ),
    )

    healthy_fraction: float = Field(
        ge=0,
        le=1,
        description=(
            "Share of the visible frame area that is functional foliage and "
            "intact panicles."
        ),
    )
    submerged_fraction: float = Field(
        ge=0,
        le=1,
        description=(
            "Share of the visible frame area that is submerged or "
            "silt-coated crop. Standing water with no crop in it is "
            "background, not submerged crop."
        ),
    )
    necrotic_fraction: float = Field(
        ge=0,
        le=1,
        description=(
            "Share of the visible frame area that is dead or necrotic crop: "
            "browning, blighted or desiccated foliage still standing."
        ),
    )
    background_fraction: float = Field(
        ge=0,
        le=1,
        description=(
            "Share of the visible frame area that is not crop at all: soil, "
            "open water, sky, bund, path, people, equipment. The four "
            "fractions must sum to approximately 1."
        ),
    )

    canopy_height_ratio: float | None = Field(
        default=None,
        ge=0,
        le=2,
        description=(
            "Measured canopy height over the height expected for a healthy "
            "crop at this stage, where the footage gives something to judge "
            "scale by (a person, a bund, a hand). Null when it does not."
        ),
    )

    crop_seen: str | None = Field(
        default=None,
        max_length=60,
        description=(
            "The crop you believe is in the footage, in plain English "
            "(\"soybean\", \"sugarcane\", \"a cereal, species unclear\"). "
            "Null when you cannot tell."
        ),
    )
    crop_matches_declaration: bool | None = Field(
        default=None,
        description=(
            "Whether what you see is consistent with the crop the farmer "
            "declared. Null when no crop was declared, or when the footage "
            "cannot settle it. False is a serious claim about a person: use "
            "it only for a crop you can positively identify as something "
            "else."
        ),
    )
    crop_mismatch_confidence: float = Field(
        default=0.0,
        ge=0,
        le=1,
        description=(
            "How sure you are that the crop is not what was declared, 0 to 1. "
            "Zero unless crop_matches_declaration is false."
        ),
    )
    crop_mismatch_detail: str | None = Field(
        default=None,
        max_length=300,
        description=(
            "What you saw that a person can check against the same video: "
            "leaf shape, growth habit, row spacing, height. Null unless there "
            "is a mismatch."
        ),
    )

    quality: float = Field(
        ge=0,
        le=1,
        description=(
            "How much you trust your own reading of this footage, 0 to 1. "
            "Low for motion blur, darkness, rain on the lens, very few "
            "frames, or a camera that mostly saw sky or soil. This is not a "
            "damage figure and must not move with the damage."
        ),
    )
    quality_notes: list[str] = Field(
        default_factory=list,
        max_length=6,
        description=(
            "Short reasons for the quality score, so a low one can be argued "
            "with rather than merely disbelieved."
        ),
    )
    observations: list[str] = Field(
        default_factory=list,
        max_length=6,
        description=(
            "What you actually saw, in plain words an officer can check "
            "against the same video."
        ),
    )


class GroundMeasureRequest(BaseModel):
    """One survey clip, as keyframes, to be measured.

    The keyframes rather than the video: decoding is the vision service's job
    and it already samples the walk evenly. This service holds the model
    credential, which is the only reason the measurement happens here.
    """

    keyframes: list[Image] = Field(
        min_length=1,
        max_length=MAX_GROUND_KEYFRAMES,
        description="Keyframes in walk order, as data URIs.",
    )
    crop_name: str | None = Field(
        default=None, description="The crop the farmer declared, where known."
    )
    station_no: int | None = Field(
        default=None,
        ge=1,
        le=8,
        description="Which directed point of the walk this clip is, where the "
        "walk was directed.",
    )


class GroundMeasureResponse(BaseModel):
    """The measurement, or the reason there is none."""

    measured: bool = Field(
        description="False when the model could not read a crop in the "
        "footage. The caller reports the ground signal absent rather than "
        "scoring an invented one."
    )
    reason: str | None = Field(
        default=None, description="Why there is no measurement."
    )
    failed: bool = Field(
        default=False,
        description="True when the model or this service failed, as opposed "
        "to reading the footage and finding no crop in it. The caller must "
        "not read a failure as a finding about the field.",
    )
    measurement: GroundMeasurement | None = None

    model: str = Field(description="The model that produced it.")
    prompt_version: str
    latency_ms: int = 0


class HealthResponse(BaseModel):
    status: Literal["ok", "degraded"]
    model: str
    prompt_version: str
    gemini_reachable: bool | None = None


# --- the officer agent's turn -------------------------------------------------
#
# The loop, the tools and the scope live in the Go service, where an officer's
# posting already decides what they may see. This carries one turn of it: the
# conversation so far and the tools available, in and a thing to say or one tool
# to call, out. See agents/turn.py for why the division is that way round.


class ToolSpec(BaseModel):
    """A tool the caller is offering, described for the model."""

    name: str = Field(description="What the model calls it by.")
    description: str = Field(
        description="What it does and when to reach for it. Read by the model "
        "rather than by a person, so it says what the tool answers rather than "
        "how it is implemented."
    )
    parameters: dict[str, Any] | None = Field(
        default=None,
        description="JSON Schema for the arguments, or null for a tool that "
        "takes none.",
    )


class ToolCall(BaseModel):
    """One tool the model wants run."""

    name: str
    arguments: dict[str, Any] = Field(default_factory=dict)


#: The most files one question may carry.
#:
#: Four, which is what the platform API allows: a circular, two photographs and a
#: sheet of figures. Repeated here rather than trusted for the same reason
#: MAX_DATA_URI_CHARS is -- that the only caller today bounds its own input is a
#: fact about this deployment, not a property of the code, and this is the process
#: that holds the Gemini credential and pays for the call.
MAX_ATTACHMENTS = 4

#: The largest attachment this will forward, as base64 characters.
#:
#: The platform API caps an upload at 8 MiB and base64 costs a third on top, so
#: eleven mebibytes of characters admits everything that passed through it and
#: refuses anything that did not.
MAX_ATTACHMENT_BASE64_CHARS = 11 * 1024 * 1024

#: The most text one attachment may carry.
#:
#: The platform API reads a spreadsheet into a table bounded at 64 KiB and appends
#: a note where it has cut one short, so the note rides on top of that budget. The
#: kibibyte of headroom is for the note.
MAX_ATTACHMENT_TEXT_CHARS = 65 * 1024


class AttachmentKind(StrEnum):
    """How an attachment reaches the model.

    Not what the file is -- `media_type` says that. A workbook arrives as text,
    because the model cannot open a workbook, and the two fields together are what
    lets the label name the real file while the part carries the table.
    """

    IMAGE = "image"
    DOCUMENT = "document"
    TEXT = "text"


#: What each kind may carry, which is the whole of what this will forward.
#:
#: An allowlist rather than a check on the prefix: `image/svg+xml` is an image by
#: its media type and a document full of script by its contents, and a media type
#: the model does not accept comes back as an error from the API rather than as
#: anything this could explain to an officer.
ATTACHMENT_MEDIA_TYPES: dict[AttachmentKind, frozenset[str]] = {
    AttachmentKind.IMAGE: frozenset({"image/png", "image/jpeg", "image/webp"}),
    AttachmentKind.DOCUMENT: frozenset({"application/pdf"}),
    AttachmentKind.TEXT: frozenset(
        {
            "text/plain",
            "text/markdown",
            "text/csv",
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        }
    ),
}


class AgentAttachment(BaseModel):
    """One file the officer attached, prepared by the caller.

    The caller sniffs the bytes, refuses what it cannot send, reads spreadsheets
    out into tables and audits every file by name, type and size. What arrives here
    is the outcome of that, and the checks below are the second reading of it: a
    payload that does not decode, or a kind that disagrees with its own media type,
    is refused before a model call is paid for.

    Nothing here inspects an image for what it depicts. Deciding whether a
    photograph is somebody's identity document cannot be done reliably, and a
    filter that works most of the time teaches an officer that the system is
    protecting them when it is not. The officer is warned in the console and
    decides; the file is audited either way. Text is a different matter -- text is
    read, and every piece of it passes `redact` like any other string.
    """

    filename: str = Field(
        min_length=1,
        max_length=200,
        description="The name the officer's own file had. Shown to the model so "
        "it can refer to the file the way the officer does, and redacted on the "
        "way past like any other text.",
    )
    kind: AttachmentKind
    media_type: str = Field(
        description="What the file turned out to be, decided by the caller from "
        "the bytes rather than from the name or from what the client claimed."
    )

    data_base64: str | None = Field(
        default=None,
        max_length=MAX_ATTACHMENT_BASE64_CHARS,
        description="The file itself, for a kind the model reads natively. Base64 "
        "because the turn is JSON; the hop this encoding costs a third on is "
        "between two of our own services, and the officer's upload reaches the "
        "caller as multipart.",
    )
    text: str | None = Field(
        default=None,
        max_length=MAX_ATTACHMENT_TEXT_CHARS,
        description="What was read out of a file the model cannot read itself, or "
        "the contents of a text file.",
    )

    # The size of the officer's original upload. Carried because the field names
    # here match the Go structs exactly and the Go side records it; nothing in
    # this process reads it.
    size_bytes: int = Field(default=0, alias="bytes", ge=0)

    _decoded: bytes = PrivateAttr(default=b"")

    @model_validator(mode="after")
    def _the_kind_the_type_and_the_payload_agree(self) -> AgentAttachment:
        permitted = ATTACHMENT_MEDIA_TYPES[self.kind]
        if self.media_type not in permitted:
            raise ValueError(
                f"{self.media_type} cannot be sent as {self.kind}; "
                f"{self.kind} carries {', '.join(sorted(permitted))}"
            )

        if self.kind is AttachmentKind.TEXT:
            if not (self.text or "").strip():
                raise ValueError("a text attachment with no text in it")
            if self.data_base64:
                raise ValueError(
                    "a text attachment carrying bytes as well; one or the other"
                )
            return self

        if self.text:
            raise ValueError(
                f"a {self.kind} attachment carrying text as well; one or the other"
            )
        if not self.data_base64:
            raise ValueError(f"a {self.kind} attachment with no bytes in it")

        try:
            # Decoded once, here, so that a truncated or mistyped payload is a
            # refused request rather than an exception half way through building a
            # prompt -- and so the bytes are not decoded twice for an 8 MiB scan.
            self._decoded = base64.b64decode(self.data_base64, validate=True)
        except (binascii.Error, ValueError) as exc:
            raise ValueError(f"that attachment is not valid base64: {exc}") from exc
        if not self._decoded:
            raise ValueError("that attachment decoded to nothing")
        return self

    def decoded(self) -> bytes:
        """The file, as bytes. Empty for a text attachment."""
        return self._decoded


class AgentMessage(BaseModel):
    """One turn already spoken, or one tool result already obtained."""

    role: Literal["user", "assistant", "tool"] = "user"
    text: str | None = None

    # Set together, for a tool that has already run. The result is whatever the
    # tool returned and is redacted on the way to the model like everything else.
    tool_name: str | None = None
    tool_result: Any | None = None

    # The files the officer attached to this turn.
    #
    # On the message rather than on the request because that is where a file
    # belongs in a conversation: "what does this circular say about late filing"
    # is one question with one attachment, and the model needs the file in that
    # position to answer it.
    attachments: list[AgentAttachment] = Field(
        default_factory=list, max_length=MAX_ATTACHMENTS
    )


class AgentTurnRequest(BaseModel):
    """Ask the model what to do next."""

    system_instruction: str = Field(
        description="Who the model is and what it may do. Composed by the "
        "caller, which is what keeps the officer agent's limits -- read and "
        "draft, never act -- next to the code that enforces them."
    )
    messages: list[AgentMessage] = Field(
        default_factory=list, description="The conversation so far, oldest first."
    )
    tools: list[ToolSpec] = Field(
        default_factory=list,
        description="What the model may ask for this turn. An empty list asks "
        "it to answer from what it already has.",
    )
    model: str | None = Field(
        default=None, description="Overrides the configured model."
    )

    @model_validator(mode="after")
    def _no_more_files_than_one_question_may_carry(self) -> AgentTurnRequest:
        """The bound again, over the whole request rather than one message.

        Four per message would be eighty over a twenty-turn history, each one
        resent on every turn. The caller attaches files only to the question being
        asked now and strips them out of the history on the way in, so this is the
        shape it already sends; stated here because the bound that matters is the
        one on what reaches the model.
        """
        carried = sum(len(m.attachments) for m in self.messages)
        if carried > MAX_ATTACHMENTS:
            raise ValueError(
                f"{carried} attachments in one turn; at most {MAX_ATTACHMENTS} "
                "may be carried, and only on the question being asked"
            )
        return self


class AgentTurnResponse(BaseModel):
    """What to say, or what to run."""

    text: str | None = Field(
        default=None, description="What to show the officer, where the model "
        "answered rather than reaching for a tool."
    )
    tool_call: ToolCall | None = Field(
        default=None,
        description="One tool to run. The caller decides whether to run it: a "
        "tool the model asks for is a request, not an instruction.",
    )
    model: str
    latency_ms: int = 0
