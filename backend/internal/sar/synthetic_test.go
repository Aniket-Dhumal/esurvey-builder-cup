package sar

import "testing"

// A generated series must never be mistaken for an observation.
//
// The radar service can invent a plausible backscatter series so this side of
// the contract can be developed without an Earth Engine project, and it has
// always labelled those SYNTHETIC in the missions list. Nothing on this side
// read the label: the conversion from the service's time series to
// Observations dropped Missions on the floor, so a generated reading reached
// the evidence packet and the dossier looking exactly like a satellite pass —
// under a heading that says Sentinel-1, in a row marked as corroborating, on a
// document that decides what a farmer is paid.
//
// These tests are about the label surviving the journey.

func TestSyntheticIsRecognised(t *testing.T) {
	cases := []struct {
		name     string
		missions []string
		want     bool
	}{
		{"the service's own label", []string{"SYNTHETIC"}, true},
		{"case is not a defence", []string{"synthetic"}, true},
		{"nor is padding", []string{"  Synthetic  "}, true},
		{"mixed in with a real collection", []string{"COPERNICUS/S1_GRD", "SYNTHETIC"}, true},

		{"a real collection", []string{"COPERNICUS/S1_GRD"}, false},
		{"several real ones", []string{"COPERNICUS/S1_GRD", "S1_GRD_FLOAT"}, false},
		{"nothing named", nil, false},
		{"empty", []string{}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSynthetic(c.missions); got != c.want {
				t.Errorf("IsSynthetic(%v) = %v, want %v", c.missions, got, c.want)
			}
		})
	}
}

func TestAPartlyGeneratedSeriesIsNotAnObservation(t *testing.T) {
	// A list naming both a real collection and the generated one means part of
	// the series was invented. There is no reading that is partly an
	// observation: the whole thing is disqualified, because nothing downstream
	// can tell which passes were which.
	if !IsSynthetic([]string{"COPERNICUS/S1_GRD", "SYNTHETIC"}) {
		t.Fatal("a series with any generated pass must count as generated")
	}
}

func TestTheLabelSurvivesOntoTheReading(t *testing.T) {
	// Analyse does not set the flag — the caller does, at the point it still
	// has the service's response in hand. This test pins the field's presence
	// and its zero value: a Reading that nobody labelled must read as an
	// observation only if it really is one, and the safe default for a bool is
	// the one that does not claim provenance it was not given.
	var r Reading
	if r.Synthetic {
		t.Error("a Reading nobody labelled must not default to generated")
	}
	if len(r.Missions) != 0 {
		t.Error("a Reading nobody labelled must not invent a mission")
	}

	r.Missions = []string{SyntheticMission}
	r.Synthetic = IsSynthetic(r.Missions)
	if !r.Synthetic {
		t.Error("the flag must follow the missions list it was derived from")
	}
}
