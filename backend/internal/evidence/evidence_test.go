package evidence

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// The rule this package exists to enforce is that no single source may fail
// an assessment, so most of what is tested is what happens when sources are
// missing, slow or broken -- and whether the packet says so in words rather
// than by omission.

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func input() Input {
	return Input{
		ReportID:     "11111111-1111-1111-1111-111111111111",
		FarmerID:     "F1",
		FarmID:       "FARM-1",
		SurveyNumber: "42",
		VillageCode:  "562100",
		EventID:      "22222222-2222-2222-2222-222222222222",
		CropName:     "Soybean",
		Season:       "kharif",
		CropYear:     "2026-27",
		CalamityType: "hailstorm",
		SowingDate:   "2026-06-15",
		IncidentDate: "2026-08-14",
		Geometry: satellite.Geometry{
			Type: "Polygon", Coordinates: []byte(`[[[73.8,18.5]]]`),
		},
		Lat: 18.5, Lon: 73.8, HasPoint: true,
		PeakNDVI: 0.72, RRVL: 55,
		DeclaredPercent: 60,
		FieldAreaHa:     2.0,
	}
}

func TestAnAssemblerWithNothingConfiguredStillProducesAPacket(t *testing.T) {
	// The whole point. An assessment can still be made from ground
	// photographs, and failing here would take that down too.
	a := &Assembler{Log: quiet()}

	p := a.Assemble(context.Background(), input(), nil)
	if p == nil {
		t.Fatal("no packet")
	}
	if p.Grounded() {
		t.Error("a packet with nothing in it reported itself as grounded")
	}
	if len(p.Absent) == 0 {
		t.Fatal("nothing was recorded as absent")
	}
}

func TestEveryAbsentSourceSaysWhyInWords(t *testing.T) {
	// A packet that gathered nothing and a packet that gathered everything
	// look identical to a reader who is only shown what arrived.
	a := &Assembler{Log: quiet()}
	p := a.Assemble(context.Background(), input(), nil)

	for _, s := range p.Absent {
		if !strings.Contains(s, ": ") {
			t.Errorf("absent entry has no reason: %q", s)
		}
		reason := s[strings.Index(s, ": ")+2:]
		if len(strings.TrimSpace(reason)) < 10 {
			t.Errorf("the reason is not a sentence: %q", s)
		}
	}
}

func TestTheSummaryTellsAnOfficerToReadTheFigureAsAJudgement(t *testing.T) {
	a := &Assembler{Log: quiet()}
	p := a.Assemble(context.Background(), input(), nil)

	s := p.Summary()
	if !strings.Contains(s, "judgement") {
		t.Errorf("summary = %q", s)
	}
}

func TestAnUnmodelledCropIsNamedRatherThanSilentlySkipped(t *testing.T) {
	// Without the crop model there is no growth stage and no susceptibility,
	// and an assessment that simply omitted them would look like one where
	// they did not apply.
	a := &Assembler{Log: quiet()}
	in := input()
	in.CropName = "dragon fruit"

	p := a.Assemble(context.Background(), in, nil)

	var named bool
	for _, s := range p.Absent {
		if strings.Contains(s, "dragon fruit") {
			named = true
		}
	}
	if !named {
		t.Errorf("the unmodelled crop is not named: %v", p.Absent)
	}
}

// ---------------------------------------------------------------------------
// Radar
// ---------------------------------------------------------------------------

type fakeRadar struct {
	res   *satellite.SARResult
	err   error
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeRadar) SAR(ctx context.Context, _ satellite.SARRequest) (*satellite.SARResult, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.res, f.err
}

func TestRadarFindingsReachThePacket(t *testing.T) {
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{
		res: &satellite.SARResult{TimeSeries: []satellite.SARPoint{
			{Date: "2026-07-20", VVdB: -8.5, VHdB: -14.5, Orbit: "ASCENDING"},
			{Date: "2026-08-01", VVdB: -8.6, VHdB: -14.6, Orbit: "ASCENDING"},
			{Date: "2026-08-20", VVdB: -19.0, VHdB: -25.0, Orbit: "ASCENDING"},
		}},
	}}

	p := a.Assemble(context.Background(), input(), nil)
	if p.Radar == nil {
		t.Fatalf("no radar reading: %v", p.Absent)
	}
	if len(p.Radar.Findings) == 0 {
		t.Fatal("no findings")
	}
	if !p.Grounded() {
		t.Error("a radar finding did not ground the packet")
	}
}

func TestAnEngineWithoutRadarIsReportedNotFatal(t *testing.T) {
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{
		err: satellite.ErrRadarUnavailable,
	}}

	p := a.Assemble(context.Background(), input(), nil)
	if p == nil {
		t.Fatal("no packet")
	}
	var said bool
	for _, s := range p.Absent {
		if strings.Contains(s, "radar") {
			said = true
		}
	}
	if !said {
		t.Errorf("a missing radar module was not reported: %v", p.Absent)
	}
}

func TestRadarIsAskedAboutTheOpticalGap(t *testing.T) {
	// The question worth asking when NDVI comes back with a two-month hole:
	// was anything watching at all.
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{
		res: &satellite.SARResult{TimeSeries: []satellite.SARPoint{
			{Date: "2026-07-01", VVdB: -8.5, VHdB: -14.5, Orbit: "ASCENDING"},
			{Date: "2026-07-13", VVdB: -8.6, VHdB: -14.6, Orbit: "ASCENDING"},
			{Date: "2026-07-25", VVdB: -8.4, VHdB: -14.3, Orbit: "ASCENDING"},
			{Date: "2026-08-20", VVdB: -8.5, VHdB: -14.4, Orbit: "ASCENDING"},
		}},
	}}
	in := input()
	in.OpticalGapFrom, in.OpticalGapTo = "2026-06-25", "2026-08-05"

	p := a.Assemble(context.Background(), in, nil)
	if p.RadarCoversGap != 3 {
		t.Errorf("radar passes in the optical gap = %d, want 3", p.RadarCoversGap)
	}
}

// ---------------------------------------------------------------------------
// Timing
// ---------------------------------------------------------------------------

func TestTheSlowestSourceDoesNotHoldTheOthers(t *testing.T) {
	// Six sequential network calls of several seconds each would add most of
	// a minute to every assessment.
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{
		delay: 300 * time.Millisecond,
		res:   &satellite.SARResult{},
	}}

	started := time.Now()
	p := a.Assemble(context.Background(), input(), nil)
	elapsed := time.Since(started)

	if p == nil {
		t.Fatal("no packet")
	}
	// Every other source fails immediately for want of configuration, so the
	// whole assembly should be about as long as the one slow call.
	if elapsed > 2*time.Second {
		t.Errorf("assembly took %s with one 300ms source", elapsed)
	}
}

func TestABudgetStopsASourceHoldingTheOfficer(t *testing.T) {
	// An assessment that is late is worse than one that is thin, because
	// somebody is waiting for it.
	a := &Assembler{
		Log:    quiet(),
		Budget: 200 * time.Millisecond,
		Radar:  &fakeRadar{delay: 10 * time.Second, res: &satellite.SARResult{}},
	}

	started := time.Now()
	p := a.Assemble(context.Background(), input(), nil)
	elapsed := time.Since(started)

	if elapsed > 3*time.Second {
		t.Fatalf("the budget did not stop a ten-second source: %s", elapsed)
	}
	if p.Radar != nil {
		t.Error("a source that ran past the budget still contributed")
	}
	var said bool
	for _, s := range p.Absent {
		if strings.Contains(s, "radar") {
			said = true
		}
	}
	if !said {
		t.Errorf("the timed-out source was not reported: %v", p.Absent)
	}
}

func TestTheElapsedTimeIsRecorded(t *testing.T) {
	// A slow source should be visible before it becomes a complaint that the
	// platform is slow.
	a := &Assembler{Log: quiet()}
	p := a.Assemble(context.Background(), input(), nil)
	if p.Elapsed == "" {
		t.Error("no elapsed time recorded")
	}
}

// ---------------------------------------------------------------------------
// Containment
// ---------------------------------------------------------------------------

type panickyRadar struct{}

func (panickyRadar) SAR(context.Context, satellite.SARRequest) (*satellite.SARResult, error) {
	panic("the engine returned something nobody expected")
}

func TestAPanicInOneSourceDoesNotTakeTheAssessment(t *testing.T) {
	a := &Assembler{Log: quiet(), Radar: panickyRadar{}}

	p := a.Assemble(context.Background(), input(), nil)
	if p == nil {
		t.Fatal("a panicking source took the packet with it")
	}
	var said bool
	for _, s := range p.Absent {
		if strings.Contains(s, "radar") {
			said = true
		}
	}
	if !said {
		t.Errorf("the panicking source was not reported absent: %v", p.Absent)
	}
}

func TestAFailingSourceDoesNotStopTheRest(t *testing.T) {
	a := &Assembler{Log: quiet(), Radar: &fakeRadar{
		err: errors.New("the engine is down"),
	}}
	in := input()

	// A series good enough for the sowing-date check, which needs nothing
	// configured at all.
	series := []agronomy.Observation{
		{Date: "2026-06-16", NDVI: 0.16},
		{Date: "2026-06-21", NDVI: 0.20},
		{Date: "2026-06-26", NDVI: 0.42},
		{Date: "2026-07-01", NDVI: 0.63},
		{Date: "2026-07-06", NDVI: 0.72},
	}

	p := a.Assemble(context.Background(), in, series)
	if p.SowingCheck == nil {
		t.Fatalf("the sowing check did not run while radar was failing: %v", p.Absent)
	}
	if p.SowingCheck.ObservedDate == "" {
		t.Error("the sowing check produced no emergence date")
	}
}

func TestTheSowingCheckReportsWhenItCannotRun(t *testing.T) {
	// A flat series in kharif is the ordinary case, and silence would read as
	// the declared date having been checked and found fine.
	a := &Assembler{Log: quiet()}
	flat := []agronomy.Observation{
		{Date: "2026-06-16", NDVI: 0.17},
		{Date: "2026-06-21", NDVI: 0.18},
		{Date: "2026-06-26", NDVI: 0.19},
	}

	p := a.Assemble(context.Background(), input(), flat)
	if p.SowingCheck != nil {
		t.Error("a flat series produced a sowing check")
	}
	var said bool
	for _, s := range p.Absent {
		if strings.Contains(s, "sowing date") {
			said = true
		}
	}
	if !said {
		t.Errorf("the unchecked sowing date was not reported: %v", p.Absent)
	}
}

func TestTheRadarWindowStraddlesTheIncident(t *testing.T) {
	// A baseline needs passes before the incident and a reading needs passes
	// after it, so a window that stopped at the incident date would never
	// produce a finding.
	from, to := radarWindow("2026-06-15", "2026-08-14")
	if from >= "2026-08-14" {
		t.Errorf("the window starts at %s, on or after the incident", from)
	}
	if to <= "2026-08-14" {
		t.Errorf("the window ends at %s, on or before the incident", to)
	}
}

func TestTheRadarWindowNeverPrecedesSowing(t *testing.T) {
	// Backscatter from bare ground before the crop existed is not a baseline
	// for the crop.
	from, _ := radarWindow("2026-08-01", "2026-08-14")
	if from < "2026-08-01" {
		t.Errorf("the window starts at %s, before sowing", from)
	}
}
