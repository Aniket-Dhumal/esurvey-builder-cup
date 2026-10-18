package evidence

import (
	"context"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// A generated radar series must not become evidence.
//
// The radar service invents a plausible backscatter series when it has no
// Earth Engine project, so this side of the contract can be built without one,
// and it labels every such result SYNTHETIC. This package used to convert the
// time series into Observations and throw the label away, which put invented
// decibels into the packet the panel reasons over and onto the dossier that
// decides what a farmer is paid.
//
// The refusal is default-on: AllowSynthetic is a permission, so a deployment
// that never heard of this flag refuses generated readings rather than
// quietly believing them.

func generated() *satellite.SARResult {
	return &satellite.SARResult{
		Missions: []string{"SYNTHETIC"},
		TimeSeries: []satellite.SARPoint{
			{Date: "2026-07-20", VVdB: -8.5, VHdB: -14.5, Orbit: "ASCENDING"},
			{Date: "2026-08-01", VVdB: -8.6, VHdB: -14.6, Orbit: "ASCENDING"},
			{Date: "2026-08-20", VVdB: -19.0, VHdB: -25.0, Orbit: "ASCENDING"},
		},
	}
}

func TestAGeneratedSeriesIsRefusedByDefault(t *testing.T) {
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{res: generated()}}

	p := a.Assemble(context.Background(), input(), nil)

	if p.Radar != nil {
		t.Fatal("a generated series became a radar reading in the packet")
	}
	if p.Grounded() {
		t.Error("a generated series grounded the packet; nothing invented may")
	}

	// Absent, and said in words, because silence here reads as "the module is
	// off" — which is a different thing from "it answered and we refused it".
	var said bool
	for _, s := range p.Absent {
		if strings.Contains(s, "radar") && strings.Contains(s, "generated") {
			said = true
		}
	}
	if !said {
		t.Errorf("the refusal was not explained to the officer: %v", p.Absent)
	}
}

func TestAGeneratedSeriesIsUsableWhenExplicitlyPermitted(t *testing.T) {
	// Development needs the path to run end to end, or it cannot be built.
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{res: generated()}, AllowSynthetic: true}

	p := a.Assemble(context.Background(), input(), nil)
	if p.Radar == nil {
		t.Fatalf("permitted and still refused: %v", p.Absent)
	}

	// Permitted is not the same as believed. Everything downstream has to be
	// able to tell, so the label travels with the reading.
	if !p.Radar.Synthetic {
		t.Error("the reading did not carry the label that made it admissible")
	}
	if len(p.Radar.Missions) == 0 {
		t.Error("the missions list did not survive the conversion")
	}
}

func TestARealSeriesIsUnaffected(t *testing.T) {
	// The guard must not cost anything when a satellite really did look.
	real := generated()
	real.Missions = []string{"COPERNICUS/S1_GRD"}

	a := &Assembler{Log: quiet(), Radar: &fakeRadar{res: real}}
	p := a.Assemble(context.Background(), input(), nil)

	if p.Radar == nil {
		t.Fatalf("a real series was refused: %v", p.Absent)
	}
	if p.Radar.Synthetic {
		t.Error("a real series was labelled generated")
	}
	if got := p.Radar.Missions; len(got) != 1 || got[0] != "COPERNICUS/S1_GRD" {
		t.Errorf("missions = %v, want the collection that was queried", got)
	}
}

func TestAnUnlabelledSeriesIsTreatedAsReal(t *testing.T) {
	// A service too old to send a missions list is not a service that is
	// lying. Refusing here would take radar away from every deployment
	// running the previous release.
	unlabelled := generated()
	unlabelled.Missions = nil

	a := &Assembler{Log: quiet(), Radar: &fakeRadar{res: unlabelled}}
	p := a.Assemble(context.Background(), input(), nil)

	if p.Radar == nil {
		t.Fatalf("an unlabelled series was refused: %v", p.Absent)
	}
	if p.Radar.Synthetic {
		t.Error("an unlabelled series was assumed generated")
	}
}
