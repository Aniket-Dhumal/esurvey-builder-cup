// Package ai is a client for the Gemini assessment agent.
//
// The agent reasons over satellite, terrain and — once the survey app exists —
// ground imagery, and returns a damage estimate with reasoning an officer can
// read and argue with. It replaces the specification's bespoke computer-vision
// stack.
//
// This package does not trust the agent. It is a separate deployable reached
// over HTTP, so the same guardrails it applies internally are applied again
// here, in internal/assessment. That duplication is deliberate.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

// Crop-presence findings. This is what a 10 m optical series over a
// smallholding can establish; damage is not.
const (
	PresenceConfirmed    = "confirmed"
	PresenceAbsent       = "absent"
	PresenceInconclusive = "inconclusive"
)

// Fraud indicator severities.
const (
	FraudInfo     = "info"
	FraudWarning  = "warning"
	FraudCritical = "critical"
)

// Image kinds. Only field_photo and survey_keyframe are ground truth — a
// colourised NDVI overlay is still satellite.
const (
	ImageNDVIMid       = "ndvi_mid"
	ImageNDVILast      = "ndvi_last"
	ImageTerrainSlope  = "terrain_slope"
	ImageTerrainAspect = "terrain_aspect"
	ImageFieldPhoto    = "field_photo"
	ImageSurveyKeyfr   = "survey_keyframe"
)

// Client talks to one assessment-agent deployment.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// Token authenticates this caller to the agent service, which holds the
	// model credential and is handed farmer photographs. Empty sends no
	// header, which is what a development stack runs with.
	Token string

	// Gate bounds how many assessments every instance together has running
	// on the agent, which is the model provider's quota. Nil admits all.
	Gate *gate.Gate
}

// New builds a client. The timeout is generous: the agent runs a multimodal
// model over several images and routinely takes 15-30 seconds.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 3 * time.Minute},
	}
}

// WithToken returns the client authenticated to the agent service.
func (c *Client) WithToken(token string) *Client {
	c.Token = token
	return c
}

type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// Image is one piece of visual evidence, inline as a data URI.
type Image struct {
	Kind    string `json:"kind"`
	Date    string `json:"date,omitempty"`
	DataURI string `json:"data_uri"`
	Caption string `json:"caption,omitempty"`
}

type NDVIPoint struct {
	Date string  `json:"date"`
	NDVI float64 `json:"ndvi"`
}

// Parcel carries the two area figures that must not be conflated:
// PolygonAreaHectares covers the whole survey number, OwnerExtentHectares is
// this owner's share of it.
type Parcel struct {
	SurveyNumber        string   `json:"survey_number"`
	FarmID              string   `json:"farm_id"`
	VillageLGDCode      string   `json:"village_lgd_code"`
	DistrictLGDCode     string   `json:"district_lgd_code,omitempty"`
	OwnerExtentHectares float64  `json:"owner_extent_hectares"`
	PolygonAreaHectares *float64 `json:"polygon_area_hectares,omitempty"`
	CentroidLat         *float64 `json:"centroid_lat,omitempty"`
	CentroidLon         *float64 `json:"centroid_lon,omitempty"`
	// BoundarySource tells the agent when a boundary is synthetic, so it can
	// say that derived figures inherit that uncertainty.
	BoundarySource string `json:"boundary_source,omitempty"`
}

type Crop struct {
	Name           string `json:"name"`
	SowingDate     string `json:"sowing_date,omitempty"`
	Season         string `json:"season,omitempty"`
	Year           string `json:"year,omitempty"`
	IrrigationType string `json:"irrigation_type,omitempty"`
	DeclaredBy     string `json:"declared_by,omitempty"`
}

type SatelliteEvidence struct {
	TimeSeries       []NDVIPoint `json:"time_series"`
	BaselineNDVI     *float64    `json:"baseline_ndvi,omitempty"`
	BaselineDate     string      `json:"baseline_date,omitempty"`
	IncidentNDVI     *float64    `json:"incident_ndvi,omitempty"`
	DeltaNDVI        *float64    `json:"delta_ndvi,omitempty"`
	RRVLPercent      *float64    `json:"rrvl_percent,omitempty"`
	ObservationCount int         `json:"observation_count"`
	LargestGapDays   int         `json:"largest_gap_days"`
}

type TerrainEvidence struct {
	MeanSlopeDegrees float64  `json:"mean_slope_degrees"`
	Classification   string   `json:"classification"`
	WaterloggingRisk string   `json:"waterlogging_risk"`
	ElevationMeanM   *float64 `json:"elevation_mean_m,omitempty"`
	ElevationMinM    *float64 `json:"elevation_min_m,omitempty"`
	ElevationMaxM    *float64 `json:"elevation_max_m,omitempty"`
	AspectCardinal   string   `json:"aspect_cardinal,omitempty"`
}

type AssessRequest struct {
	AssessmentID string `json:"assessment_id"`
	IncidentDate string `json:"incident_date"`

	// CalamityType and LossCategory are what the farmer says happened. The
	// weather and phenology specialists cannot work without them: "is this
	// consistent with the record" and "does this damage mode fit the growth
	// stage" are both questions about a named calamity.
	CalamityType string `json:"calamity_type,omitempty"`
	LossCategory string `json:"loss_category,omitempty"`

	// CalamityDeclaredBy is who said this calamity happened: "farmer" when it
	// comes from a claim, "district" when it comes from the survey event.
	//
	// The difference is not a nuance. A farmer's assertion is a claim to be
	// checked against the record, and a mismatch is worth flagging. A survey
	// event is a district's official declaration -- the Collector's, under
	// the state's own process -- and the platform contradicting it on an
	// ~11 km reanalysis grid is not a finding about the farmer. Raising a
	// fraud indicator there accuses a named person of something the district
	// declared.
	CalamityDeclaredBy string `json:"calamity_declared_by,omitempty"`

	SowingDate string            `json:"sowing_date,omitempty"`
	WindowDays int               `json:"window_days,omitempty"`
	Parcel     Parcel            `json:"parcel"`
	Crop       *Crop             `json:"crop,omitempty"`
	Satellite  SatelliteEvidence `json:"satellite"`
	Terrain    *TerrainEvidence  `json:"terrain,omitempty"`
	Images     []Image           `json:"images,omitempty"`

	// GroundExpected says whether this assessment was ever meant to have
	// field imagery.
	//
	// An assessment requested from the console against a parcel is a
	// satellite screening: nobody has filmed anything and nobody was asked
	// to. An assessment of a filed claim carries the farmer's own clips. The
	// two have the same shape and different meanings, and without this the
	// panel could not tell them apart -- so a screening came back reading
	// like a failed assessment, leading with "no damage assessment is
	// possible" because photographs that were never going to exist did not.
	GroundExpected bool `json:"ground_expected"`

	// Evidence is everything the assessment could be grounded in: the
	// parcel's own prior seasons, the fields around it, radar that saw
	// through cloud, the season's weather, where the crop stood in its cycle,
	// what an officer counted, and what has been claimed here before.
	//
	// Untyped on purpose. This package is a thin HTTP client and the
	// analysis packages that build the packet depend on the database, the
	// analytics engine and the weather archive; importing them here to gain a
	// type would make every consumer of the client carry all three. The
	// packet's own package owns its shape, and the agent's schema is the
	// contract that matters.
	//
	// It carries its own Absent list, so a packet that gathered nothing and a
	// packet that gathered everything do not look alike to the panel.
	Evidence any `json:"evidence,omitempty"`
}

// FraudIndicator is a discrepancy between the claim and the observed record.
// It routes a claim to closer human scrutiny; it never determines anything.
type FraudIndicator struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// Specialist is one panel member's run.
//
// Carried back so an officer can see which evidence was actually examined and
// which was missing. An assessment made without the weather check is a
// different assessment, and the difference should not be invisible.
type Specialist struct {
	Agent       string   `json:"agent"`
	Ran         bool     `json:"ran"`
	DurationMS  int      `json:"duration_ms"`
	Error       string   `json:"error,omitempty"`
	Verdict     string   `json:"verdict,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Reasoning   string   `json:"reasoning,omitempty"`
	Limitations []string `json:"limitations,omitempty"`
}

// Result is the agent's assessment, after its own guardrails.
type Result struct {
	DamagePercent *float64 `json:"damage_percent"`
	Severity      string   `json:"severity"`
	Confidence    float64  `json:"confidence"`

	// InsufficientEvidence is the expected outcome until ground imagery
	// exists — satellite alone cannot quantify damage.
	InsufficientEvidence bool `json:"insufficient_evidence"`

	Reasoning string `json:"reasoning"`

	CropPresence          string           `json:"crop_presence"`
	CropPresenceReasoning string           `json:"crop_presence_reasoning"`
	FraudIndicators       []FraudIndicator `json:"fraud_indicators"`

	EvidenceCited  []string `json:"evidence_cited"`
	Contradictions []string `json:"contradictions"`

	Model             string   `json:"model"`
	PromptVersion     string   `json:"prompt_version"`
	GuardrailsApplied []string `json:"guardrails_applied"`
}

type AssessResponse struct {
	AssessmentID string `json:"assessment_id"`
	Result       Result `json:"result"`
	LatencyMS    int    `json:"latency_ms"`

	// Specialists is what each panel member found, including the ones that
	// did not run.
	Specialists []Specialist `json:"specialists,omitempty"`
	// Degraded reports that the panel ran short-handed, or fell back to a
	// deterministic assessment entirely.
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
}

// Error carries the agent's HTTP status so callers can distinguish an
// unconfigured service from a genuine failure.
type Error struct {
	HTTPStatus int
	Detail     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ai agent: %s (http %d)", e.Detail, e.HTTPStatus)
}

// Unavailable reports whether the agent is reachable but not configured —
// worth surfacing differently from a crash, since it is an operator action.
func (e *Error) Unavailable() bool {
	return e.HTTPStatus == http.StatusServiceUnavailable
}

// Health reports the agent's status, model and prompt version.
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching ai agent: %w", err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding ai health: %w", err)
	}
	return out, nil
}

// Assess runs one assessment.
func (c *Client) Assess(ctx context.Context, r AssessRequest) (*AssessResponse, error) {
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	buf, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encoding assess request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.BaseURL+"/api/v1/assess", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authenticate(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling ai agent: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading ai response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var body struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &body)
		detail := body.Detail
		if detail == "" {
			detail = string(raw)
		}
		return nil, &Error{HTTPStatus: resp.StatusCode, Detail: detail}
	}

	var out AssessResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding ai response: %w", err)
	}
	return &out, nil
}

// HasGroundImagery reports whether any image is direct field evidence.
//
// This gates the damage figure. A colourised NDVI overlay is a satellite
// product, not a photograph of the field, and must not unlock a number.
func HasGroundImagery(images []Image) bool {
	for _, img := range images {
		if img.Kind == ImageFieldPhoto || img.Kind == ImageSurveyKeyfr {
			return true
		}
	}
	return false
}

// authenticate adds the shared token, when one is configured.
//
// Bearer rather than X-API-Key here because this client has no established
// convention to keep; the agent service accepts either.
func (c *Client) authenticate(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}
