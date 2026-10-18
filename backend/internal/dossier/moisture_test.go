package dossier

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/moisture"
)

func mf(v float64) *float64 { return &v }

func stressedReading() *moisture.Reading {
	return &moisture.Reading{
		Relevance: moisture.Relevance("drought"),
		Canopy: &moisture.Canopy{
			Near: mf(0.19), NearPasses: 2,
			PriorMean: mf(0.33), PriorYears: 3, Anomaly: mf(-0.14),
			NeighbourGap: mf(-0.11), NeighbourGapBefore: mf(0.01),
		},
		Soil: &moisture.Soil{
			RelativeOrbit: 63, DryDB: -15.9, WetDB: -8.1, HistoryPasses: 55,
			Index: mf(0.12), IndexDate: "2026-08-27",
		},
		Findings: []moisture.Finding{
			{Code: moisture.FindingCanopyWaterStress, Confidence: "high",
				Detail: "drier than its history and its neighbours"},
			{Code: moisture.FindingSurfaceDry, Confidence: "moderate",
				Detail: "near the bottom of its range"},
		},
		Missions: []string{"SENTINEL-2", "SENTINEL-1"},
	}
}

func TestAnAbsentMoistureRecordIsStatedNotOmitted(t *testing.T) {
	s := Moisture(nil, "no clear Sentinel-2 pass near the incident")
	if len(s.Rows) != 1 || !s.Rows[0].Absent {
		t.Fatalf("rows = %+v, want one absent row", s.Rows)
	}
	if !strings.Contains(textOf(s.Rows[0].AbsentWhy), "sentinel-2") {
		t.Errorf("the reason was not carried: %q", s.Rows[0].AbsentWhy.EN)
	}
}

// Nothing here measures a loss, so nothing here may be marked as having
// entered the figure.
func TestNoMoistureRowDetermines(t *testing.T) {
	s := Moisture(stressedReading(), "")
	if len(s.Rows) < 4 {
		t.Fatalf("rows = %d, want relevance, two findings and the measurements", len(s.Rows))
	}
	for _, r := range s.Rows {
		if r.Role == Determines {
			t.Errorf("%q is marked as determining the figure", r.Parameter.EN)
		}
	}
}

// A farmer disputing a hailstorm assessment who reads "canopy water stress"
// is entitled to know, first, that it bears little on hail.
func TestTheRelevanceComesBeforeTheFindings(t *testing.T) {
	r := stressedReading()
	r.Relevance = moisture.Relevance("hailstorm")
	s := Moisture(r, "")
	if !strings.HasPrefix(s.Rows[0].Observed.EN, "Weak") {
		t.Fatalf("first row = %q, want the relevance", s.Rows[0].Observed.EN)
	}
}

func TestEveryMoistureRowIsBilingual(t *testing.T) {
	s := Moisture(stressedReading(), "")
	for _, r := range s.Rows {
		if r.Parameter.MR == "" || r.Parameter.EN == "" {
			t.Errorf("parameter %q is missing a language", r.Parameter.EN)
		}
		if r.Effect.EN != "" && r.Effect.MR == "" {
			t.Errorf("effect for %q has no Marathi", r.Parameter.EN)
		}
	}
}

func TestAGeneratedMoistureRecordCarriesNoWeight(t *testing.T) {
	r := stressedReading()
	r.Synthetic = true
	s := Moisture(r, "")
	for _, row := range s.Rows {
		if row.Role == Corroborates || row.Role == Determines {
			t.Errorf("%q carries weight on a generated record", row.Parameter.EN)
		}
	}
}

// The digest covers exactly what produced a document. A dossier printed before
// this section existed must keep the digest it was issued with.
func TestAddingMoistureDoesNotChangeAnOlderDossiersDigest(t *testing.T) {
	in := Inputs{State: Provisional, Reference: "A-1",
		GeneratedAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	before := Digest(in)
	if strings.Contains(string(mustJSON(t, in)), "moisture") {
		t.Fatal("an input with no moisture still serialises a moisture key")
	}
	if Digest(in) != before {
		t.Fatal("digest is not stable")
	}
}

func TestTheSectionIsInTheBuiltDossier(t *testing.T) {
	d := Build(Inputs{State: Provisional, Reference: "A-1", Moisture: stressedReading()})
	sec, ok := d.Section("6a")
	if !ok || !sec.Any() {
		t.Fatal("§6a is missing from the built dossier")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
