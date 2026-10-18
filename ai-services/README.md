# esurvey-ai

Gemini assessment agent for post-disaster crop loss. Called by the Go backend;
returns a damage estimate with reasoning an officer can read and disagree with.

Replaces the specification's bespoke computer-vision stack (YOLOv10 lodging
detection, SegFormer submersion segmentation, Mask2Former necrosis). The trade:
no training data, no GPU serving, no ML hire, and multimodal reasoning over
heterogeneous evidence — against purpose-trained models with published accuracy
on Indian crops. What tips it is explicability. A segmentation mask is opaque
to a Tehsildar; a paragraph citing the evidence is not.

## Run it

```bash
uv venv
uv pip install -e ".[dev]"

export ESURVEY_AI_GOOGLE_API_KEY=...        # or use Vertex, below
.venv/bin/python -m uvicorn esurvey_ai.main:app --port 8100
```

The service **starts without a credential** and reports `degraded` on
`/healthz`. Assessment calls then return `503` with an actionable message. A
container that refuses to boot tells an operator less than one that boots and
says what is wrong.

### Configuration

All via `ESURVEY_AI_*` environment variables, or a local `.env`.

| Variable | Default | Notes |
| --- | --- | --- |
| `MODEL` | `gemini-2.5-pro` | **Verify against the current model list before deploying.** Google's naming moves; a stale id fails at call time, not startup. |
| `TEMPERATURE` | `0.1` | Near-deterministic. This grades evidence for a relief decision — run-to-run variance on identical inputs is a defect, not creativity. |
| `GOOGLE_API_KEY` | — | AI Studio key. |
| `USE_VERTEX` | `false` | Use Vertex AI instead. |
| `VERTEX_PROJECT` | — | Required when `USE_VERTEX`. |
| `VERTEX_LOCATION` | `asia-south1` | Data residency. |
| `MIN_OBSERVATIONS` | `2` | Below this, refuse unless ground imagery is present. |
| `PORT` | `8100` | |

## API

```
GET  /healthz          model, prompt version, whether a credential exists
POST /api/v1/assess    evidence in, assessment out
```

Request carries the parcel, crop, NDVI series, terrain, and images as data
URIs. Response:

```json
{
  "damage_percent": 42.5,
  "severity": "Severe",
  "confidence": 0.45,
  "insufficient_evidence": false,
  "reasoning": "NDVI fell from 0.71 on 2 August to 0.28 on 14 October...",
  "evidence_cited": ["NDVI series", "last-visit overlay"],
  "contradictions": [],
  "model": "gemini-2.5-pro",
  "prompt_version": "v1.0.0",
  "guardrails_applied": ["confidence_capped_no_ground_truth"]
}
```

## Guardrails

The largest risk here is not that Gemini fails — it is that it **succeeds
plausibly on bad input**. A confident, well-written wrong answer is more
dangerous than an obvious error, because nobody questions it.

So the model is bounded on both sides, in `guardrails.py`:

**Pre-flight — refuse before spending a call**

- No cloud-free observations at all
- Below `MIN_OBSERVATIONS`, with no ground imagery
- Nothing observed on or after the incident date

Ground imagery overrides all three: a photograph of a lodged crop is direct
evidence where an NDVI drop is an inference.

**Post-flight — bound what comes back**

- A refusal cannot carry a figure out with it, and a missing figure becomes a
  refusal. `insufficient_evidence` with `damage_percent: 72` is incoherent.
- `damage_percent` clamped to 0–100.
- Confidence capped at **0.6** without ground imagery, **0.45** past a 30-day
  cloud gap, **0.5** on an unconfirmed or mocked crop.
- Severity is **derived** from the figure, never trusted from the model.
  "Catastrophic" beside 8% is self-contradiction, and downstream code reads
  the number.

`RawAssessment` is the unbounded parse of the model's reply; `AssessResult` is
the validated contract. Out-of-range values are clamped and flagged rather
than rejected — rejecting loses the reasoning along with the bad number, and
the reasoning is often still worth an officer's attention.

**The Go backend enforces its own copy of these.** The duplication is
deliberate: this is a separate deployable and the backend must not trust it.

## What the agent is told

`evidence.py` renders the packet, and is deliberately explicit about things the
model cannot infer:

- **Cloud gaps**, marked inline in the series with day counts. A 60-day
  silence is why a conclusion is weak, and it has to be visible.
- **Provenance.** A mocked boundary or a synthetic crop declaration is
  labelled as such, with a note that derived figures inherit the uncertainty.
- **Polygon vs owner extent.** Where the traced boundary exceeds the owner's
  share — 4.82 ha against 1.60 ha on the reference parcel — the note says the
  polygon covers the whole survey number and this owner holds a part of it.
- **Terrain is susceptibility, not damage.** Stated outright, because flat
  ground under a thriving crop is not a loss.

## Prompt versioning

`PROMPT_VERSION` in `agent/prompts.py` is returned with every assessment and
stored alongside it. **A prompt change is a behaviour change — bump it**, or a
past assessment becomes irreproducible and cannot be defended in a grievance
hearing.

## Development

```bash
.venv/bin/python -m pytest        # 16 tests, guardrails
.venv/bin/python -m ruff check .
.venv/bin/python -m ruff format .
```

The guardrail tests matter more than the prompt. A prompt is a request; a
guardrail is a guarantee.

## Not done yet

- No evaluation set. Prompt changes are currently unmeasurable — hand-labelled
  parcels with known outcomes are needed before tuning means anything.
- No caching. Identical evidence re-runs the model.
- Ground imagery paths exist in the schema but nothing produces them until the
  mobile capture app lands.
