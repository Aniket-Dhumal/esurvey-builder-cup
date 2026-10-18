package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// The evidence packet the panel is given, and the panel's account of itself
// coming back. Both directions matter and neither is covered by the guardrail
// tests, which start after the request is built.

func testParcel() *agristack.Parcel {
	return &agristack.Parcel{
		FarmerID:           "F1",
		FarmID:             "MH003078187222",
		SurveyNumber:       "453",
		VillageLGDCode:     "562111",
		DistrictLGDCode:    "524",
		SubDistrictLGDCode: "4246",
		OwnerExtent:        "1.60",
		AreaUnit:           "Hectare",
		PlotGeometry: &agristack.Geometry{
			Type: "Polygon",
			Coordinates: json.RawMessage(
				`[[[75.90,17.65],[75.91,17.65],[75.91,17.66],[75.90,17.66],[75.90,17.65]]]`),
		},
	}
}

func testAgentRequest(t *testing.T) ai.AssessRequest {
	t.Helper()
	s := &Server{}
	return s.buildAgentRequest(
		createAssessmentRequest{
			FarmerID:     "F1",
			FarmID:       "MH003078187222",
			IncidentDate: "2025-10-12",
		},
		testParcel(),
		nil,
		"2025-06-20",
		1.6,
		assessment.Satellite{},
		assessment.Terrain{},
		&satellite.NDVIResult{},
		&satellite.TerrainResult{},
		// The recorded provenance of the boundary. Passed rather than assumed
		// now that a farmer-traced plot and a registry one support different
		// claims and the agent is told which it is looking at.
		"field_traced",
	)
}

func TestAgentRequestCarriesTheParcelPosition(t *testing.T) {
	// The weather specialist is not given a window; it looks one up from the
	// centroid. Without this it does not run at all.
	req := testAgentRequest(t)

	if req.Parcel.CentroidLat == nil || req.Parcel.CentroidLon == nil {
		t.Fatal("no centroid: the weather specialist would be skipped")
	}
	if lat := *req.Parcel.CentroidLat; lat < 17.65 || lat > 17.66 {
		t.Errorf("centroid latitude %.5f is not on the parcel", lat)
	}
	if lon := *req.Parcel.CentroidLon; lon < 75.90 || lon > 75.91 {
		t.Errorf("centroid longitude %.5f is not on the parcel", lon)
	}
}

func TestAgentRequestSurvivesAParcelWithNoGeometry(t *testing.T) {
	// AgriStack returns plot_geometry: null for every farmer we can reach.
	// That must cost the weather specialist, not the whole assessment.
	s := &Server{}
	p := testParcel()
	p.PlotGeometry = nil

	req := s.buildAgentRequest(
		createAssessmentRequest{FarmID: "X", IncidentDate: "2025-10-12"},
		p, nil, "2025-06-20", 1.6,
		assessment.Satellite{}, assessment.Terrain{},
		&satellite.NDVIResult{}, &satellite.TerrainResult{},
		"field_traced",
	)

	if req.Parcel.CentroidLat != nil {
		t.Error("a parcel with no boundary reported a position anyway")
	}
}

func TestTheCalamityAndPanelCrossTheWire(t *testing.T) {
	// The specialists' prompts name req.calamity_type. If the Go side does not
	// send it, two of the four have nothing to reason about -- and the failure
	// is silent, because the panel still answers.
	var got ai.AssessRequest

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("agent received unparseable request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"assessment_id": "t",
				"latency_ms": 41622,
				"degraded": true,
				"degraded_reason": "did not report: vision_specialist (no ground photographs were supplied)",
				"specialists": [
					{"agent":"weather_specialist","ran":true,"duration_ms":24868,
					 "verdict":"inconclusive","confidence":0.7,
					 "reasoning":"no precipitation recorded on the incident date",
					 "limitations":["11 km grid can miss a localized hailstorm"]},
					{"agent":"vision_specialist","ran":false,"duration_ms":0,
					 "error":"no ground photographs were supplied"}
				],
				"result": {
					"crop_presence":"confirmed",
					"crop_presence_reasoning":"the curve is a crop",
					"reasoning":"three of four reported",
					"confidence":0.4,
					"insufficient_evidence":true,
					"model":"gemini-3.1-pro-preview",
					"prompt_version":"panel-v1.0.0+synthesis-v1.0.0"
				}
			}`))
		}))
	defer srv.Close()

	req := testAgentRequest(t)
	req.CalamityType = "अवकाळी पाऊस व गारपीट"
	req.LossCategory = "standing_crop"

	resp, err := ai.New(srv.URL).Assess(context.Background(), req)
	if err != nil {
		t.Fatalf("assess: %v", err)
	}

	if got.CalamityType != "अवकाळी पाऊस व गारपीट" {
		t.Errorf("calamity did not reach the panel: %q", got.CalamityType)
	}
	if got.LossCategory != "standing_crop" {
		t.Errorf("loss category did not reach the panel: %q", got.LossCategory)
	}
	if got.Parcel.CentroidLat == nil {
		t.Error("centroid did not reach the panel")
	}

	if !resp.Degraded || resp.DegradedReason == "" {
		t.Error("a degraded panel came back looking healthy")
	}
	if n := ranSpecialists(resp.Specialists); n != 1 {
		t.Errorf("ran specialists = %d, want 1", n)
	}

	// And the account of the panel has to survive onto the stored record --
	// an officer reading a confidence of 0.4 needs to see it was low because
	// nobody examined a photograph.
	v := assessment.Verify(resp.Result, req)
	v.Specialists = resp.Specialists
	v.Degraded = resp.Degraded
	v.DegradedReason = resp.DegradedReason

	round, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling the verified assessment: %v", err)
	}
	var back assessment.Verified
	if err := json.Unmarshal(round, &back); err != nil {
		t.Fatalf("unmarshalling the verified assessment: %v", err)
	}
	if len(back.Specialists) != 2 {
		t.Fatalf("specialists stored = %d, want 2", len(back.Specialists))
	}
	if back.Specialists[0].Confidence == nil {
		t.Error("a specialist's confidence was lost on the way to storage")
	}
	if len(back.Specialists[0].Limitations) != 1 {
		t.Error("a specialist's limitations were lost on the way to storage")
	}
	if back.Specialists[1].Error == "" {
		t.Error("the reason a specialist did not run was lost")
	}
}
