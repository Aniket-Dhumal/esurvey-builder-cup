package moisture

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Each test builds a measurement of known shape and checks the analysis says
// what that shape means. The thresholds are starting values; what these hold
// is the logic -- that a dry locality is not read as a dry field, that an
// irrigated field is noticed, that a cloud-clipped pass is not believed.

const incident = "2026-09-03"

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

// pass is a clear, well-covered Sentinel-2 date with a readable neighbourhood.
func pass(date string, offset int, ndmi, ring float64) satellite.OpticalPass {
	return satellite.OpticalPass{
		Date: date, SeasonOffset: offset,
		NDMI: f(ndmi), NDVI: f(0.5),
		ClearFraction: 1, ClearPixels: 30,
		RingNDMI: f(ring), RingNDVI: f(0.5), RingClearPixels: 5000,
	}
}

// history gives three prior seasons with the field at `normal` near the
// incident's calendar days.
func history(normal float64) []satellite.OpticalPass {
	return []satellite.OpticalPass{
		pass("2025-08-30", 1, normal, normal-0.02),
		pass("2024-09-05", 2, normal+0.01, normal-0.01),
		pass("2023-09-01", 3, normal-0.01, normal-0.03),
	}
}

func result(optical []satellite.OpticalPass) *satellite.MoistureResult {
	return &satellite.MoistureResult{
		Optical: optical, ParcelPixels: 30, RingCroplandPixels: 18000,
		BaselineYears: 3, Missions: []string{"SENTINEL-2", "SENTINEL-1"},
	}
}

func codes(r *Reading) []string {
	var out []string
	for _, fd := range r.Findings {
		out = append(out, fd.Code+"/"+fd.Confidence)
	}
	return out
}

func has(r *Reading, code string) *Finding {
	for k := range r.Findings {
		if r.Findings[k].Code == code {
			return &r.Findings[k]
		}
	}
	return nil
}

func TestAFieldDrierThanItsHistoryAndItsNeighboursIsStressed(t *testing.T) {
	opt := append(history(0.33),
		pass("2026-07-25", 0, 0.32, 0.31), // before: level with neighbours
		pass("2026-08-30", 0, 0.20, 0.30), // near: 0.13 below normal, fell 0.11 against the ring
		pass("2026-09-07", 0, 0.19, 0.29),
	)
	r, why, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	fd := has(r, FindingCanopyWaterStress)
	if fd == nil {
		t.Fatalf("findings = %v, want canopy water stress", codes(r))
	}
	if fd.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s, want high with two passes and three prior seasons", fd.Confidence)
	}
	if r.Canopy.PriorYears != 3 {
		t.Errorf("prior years = %d, want 3", r.Canopy.PriorYears)
	}
	if !strings.HasPrefix(r.Relevance, "Strong") {
		t.Errorf("drought relevance = %q", r.Relevance)
	}
}

// The distinction that matters most for a compensation decision: a whole
// locality drying out corroborates the event, and must not be read as this
// field being singled out.
func TestADryLocalityIsNotReadAsADryField(t *testing.T) {
	opt := append(history(0.33),
		pass("2026-07-25", 0, 0.32, 0.31),
		pass("2026-08-30", 0, 0.20, 0.19),
		pass("2026-09-07", 0, 0.21, 0.20),
	)
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if has(r, FindingLocalityDry) == nil {
		t.Fatalf("findings = %v, want locality_dry", codes(r))
	}
	if has(r, FindingCanopyWaterStress) != nil {
		t.Fatal("a locality-wide dry spell was reported as stress on this field")
	}
}

// A field that has always been drier than the land around it -- lighter soil,
// an earlier variety -- has not become drier because the gap is negative.
func TestAFieldAlwaysDrierThanItsNeighboursIsNotNewlyStressed(t *testing.T) {
	opt := append(history(0.24),
		pass("2026-07-25", 0, 0.24, 0.34), // always 0.10 below the ring
		pass("2026-08-30", 0, 0.24, 0.34),
		pass("2026-09-06", 0, 0.23, 0.33),
	)
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if has(r, FindingCanopyWaterStress) != nil {
		t.Fatalf("a standing difference was read as a change: %v", codes(r))
	}
	if has(r, FindingNoCanopyWaterChange) == nil {
		t.Fatalf("findings = %v, want no_canopy_water_change", codes(r))
	}
}

func TestAnIrrigatedFieldInADrySpellIsNoticed(t *testing.T) {
	opt := append(history(0.40),
		pass("2026-07-25", 0, 0.41, 0.25),
		pass("2026-08-30", 0, 0.40, 0.21),
		pass("2026-09-06", 0, 0.39, 0.20),
	)
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	fd := has(r, FindingWetterThanNeighbours)
	if fd == nil {
		t.Fatalf("findings = %v, want wetter_than_neighbours", codes(r))
	}
	if !strings.Contains(fd.Detail, "irrigation") {
		t.Errorf("the detail does not name the usual cause: %s", fd.Detail)
	}
	// It is a reason to look, never an accusation.
	for _, word := range []string{"fraud", "false", "suspicious"} {
		if strings.Contains(strings.ToLower(fd.Detail), word) {
			t.Errorf("the finding accuses the farmer: %s", fd.Detail)
		}
	}
}

func TestACanopyAtItsNormalSaysSoPlainly(t *testing.T) {
	opt := append(history(0.33),
		pass("2026-08-30", 0, 0.33, 0.31),
		pass("2026-09-06", 0, 0.34, 0.32),
	)
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if has(r, FindingNoCanopyWaterChange) == nil {
		t.Fatalf("findings = %v, want no_canopy_water_change", codes(r))
	}
}

// The live Earth Engine run on 14 September returned exactly this: a pass
// 6.7% clear over two pixels, reading 0.23 beside clear passes at 0.33. Read,
// it would have been a drought.
func TestACloudClippedPassIsNotBelieved(t *testing.T) {
	clipped := pass("2026-09-01", 0, 0.05, 0.30)
	clipped.ClearFraction = 0.067
	clipped.ClearPixels = 2

	opt := append(history(0.33), clipped, pass("2026-09-06", 0, 0.33, 0.31))
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if r.Canopy.NearPasses != 1 {
		t.Fatalf("near passes = %d, want the clipped one excluded", r.Canopy.NearPasses)
	}
	if has(r, FindingCanopyWaterStress) != nil {
		t.Fatal("a cloud-clipped pass produced a stress finding")
	}
}

// A mean of per-year means. A wet year with many clear passes and a dry year
// with one would otherwise make the wet year the field's normal.
func TestTheNormalIsAMeanOfYearsNotOfPasses(t *testing.T) {
	opt := []satellite.OpticalPass{
		pass("2025-08-25", 1, 0.40, 0.38),
		pass("2025-08-30", 1, 0.40, 0.38),
		pass("2025-09-04", 1, 0.40, 0.38),
		pass("2025-09-09", 1, 0.40, 0.38),
		pass("2025-09-14", 1, 0.40, 0.38),
		pass("2024-09-02", 2, 0.20, 0.18),
		pass("2026-09-02", 0, 0.30, 0.28),
	}
	r, _, ok := Analyse(result(opt), incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if r.Canopy.PriorMean == nil || *r.Canopy.PriorMean != 0.30 {
		t.Fatalf("prior mean = %v, want 0.30 (the two years weighted equally)", r.Canopy.PriorMean)
	}
}

func TestAParcelOfAFewPixelsIsNotRead(t *testing.T) {
	res := result(append(history(0.33), pass("2026-09-02", 0, 0.10, 0.30)))
	res.ParcelPixels = 4

	_, why, ok := Analyse(res, incident, "drought")
	if ok {
		t.Fatal("a four-pixel parcel produced a reading")
	}
	if !strings.Contains(why, "pixels") {
		t.Errorf("the reason does not say why: %q", why)
	}
}

func TestNothingNearTheIncidentIsAnAbsenceWithAReason(t *testing.T) {
	_, why, ok := Analyse(result(nil), incident, "drought")
	if ok || why == "" {
		t.Fatalf("ok = %v, why = %q; want an absence with a reason", ok, why)
	}
}

// --- soil ---------------------------------------------------------------------

// soilSeries is two years on one orbit swinging between a dry floor and a wet
// ceiling, with the pass nearest the incident set to `atIncident`.
func soilSeries(orbit int, offset, atIncident, before float64) []satellite.SoilPass {
	var out []satellite.SoilPass
	for k := 0; k < 60; k++ {
		vv := -16.0 + offset
		if k%3 == 0 {
			vv = -8.0 + offset
		}
		out = append(out, satellite.SoilPass{
			Date: fmt.Sprintf("2025-%02d-%02d", 1+k%12, 1+k%27), VVdB: vv,
			RelativeOrbit: i(orbit), Coverage: f(1),
		})
	}
	out = append(out,
		satellite.SoilPass{Date: "2026-07-25", VVdB: before + offset, RelativeOrbit: i(orbit), Coverage: f(1)},
		satellite.SoilPass{Date: "2026-09-01", VVdB: atIncident + offset, RelativeOrbit: i(orbit), Coverage: f(1)},
	)
	return out
}

func TestSoilNearItsWettestIsReportedWet(t *testing.T) {
	res := result(nil)
	res.Soil = soilSeries(63, 0, -8.2, -15)

	r, why, ok := Analyse(res, incident, "flood")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if has(r, FindingSurfaceWet) == nil {
		t.Fatalf("findings = %v, want surface_wet", codes(r))
	}
	if r.Soil.RelativeOrbit != 63 {
		t.Errorf("orbit = %d", r.Soil.RelativeOrbit)
	}
	if !strings.HasPrefix(r.Relevance, "Limited") {
		t.Errorf("flood relevance = %q", r.Relevance)
	}
}

// The same field reads several decibels apart from two viewing geometries. A
// floor from one and a ceiling from the other would call a change of orbit a
// change of moisture.
func TestSoilReferencesAreNeverMixedAcrossOrbits(t *testing.T) {
	res := result(nil)
	main := soilSeries(63, 0, -12, -12)
	// A second orbit reading 5 dB higher throughout, with fewer passes.
	other := soilSeries(136, 5, -7, -7)[:25]
	res.Soil = append(main, other...)

	r, _, ok := Analyse(res, incident, "flood")
	if !ok {
		t.Fatal("no reading")
	}
	if r.Soil.RelativeOrbit != 63 {
		t.Fatalf("orbit = %d, want the better-observed one", r.Soil.RelativeOrbit)
	}
	if r.Soil.WetDB > -7.5 {
		t.Fatalf("wet reference %.1f dB was taken from the other orbit", r.Soil.WetDB)
	}
}

func TestADenseCanopyMarksTheSoilIndexWeak(t *testing.T) {
	dense := pass("2026-09-02", 0, 0.33, 0.31)
	dense.NDVI = f(0.78)

	res := result(append(history(0.33), dense))
	res.Soil = soilSeries(63, 0, -8.2, -15)

	r, _, ok := Analyse(res, incident, "flood")
	if !ok {
		t.Fatal("no reading")
	}
	if !r.Soil.CanopyObscured {
		t.Fatal("a dense canopy was not flagged")
	}
	if fd := has(r, FindingSurfaceWet); fd == nil || fd.Confidence != ConfidenceLow {
		t.Fatalf("surface_wet under a dense canopy should be low confidence: %v", codes(r))
	}
}

func TestTooShortASoilHistoryIsNotRead(t *testing.T) {
	res := result(nil)
	res.Soil = soilSeries(63, 0, -8, -15)[:10]
	if _, _, ok := Analyse(res, incident, "flood"); ok {
		t.Fatal("ten passes produced a dry floor and a wet ceiling")
	}
}

// --- properties of the whole reading ------------------------------------------

// Nothing in this package produces a damage figure. The reading must not carry
// a field that a later reader could mistake for one.
func TestAReadingCarriesNoDamageFigure(t *testing.T) {
	opt := append(history(0.33), pass("2026-08-30", 0, 0.20, 0.30))
	res := result(opt)
	res.Soil = soilSeries(63, 0, -8.2, -15)
	r, _, ok := Analyse(res, incident, "drought")
	if !ok {
		t.Fatal("no reading")
	}
	raw, _ := json.Marshal(r)
	for _, word := range []string{"damage", "loss", "percent"} {
		if strings.Contains(strings.ToLower(string(raw)), `"`+word) {
			t.Fatalf("a reading field is named like a damage figure: %s", raw)
		}
	}
}

// The same measurement must always read the same way. Orbits are held in a
// map, and a choice that followed map order would make an assessment differ
// between two runs over identical evidence.
func TestTheReadingIsDeterministic(t *testing.T) {
	res := result(append(history(0.33), pass("2026-08-30", 0, 0.20, 0.30)))
	res.Soil = append(soilSeries(63, 0, -8.2, -15), soilSeries(136, 3, -8.2, -15)...)

	first, _, _ := Analyse(res, incident, "drought")
	for k := 0; k < 20; k++ {
		again, _, _ := Analyse(res, incident, "drought")
		if !reflect.DeepEqual(first, again) {
			t.Fatal("two analyses of the same measurement differ")
		}
	}
}

func TestASyntheticMeasurementIsLabelled(t *testing.T) {
	res := result(append(history(0.33), pass("2026-08-30", 0, 0.20, 0.30)))
	res.Missions = []string{"SYNTHETIC"}
	r, _, ok := Analyse(res, incident, "drought")
	if !ok || !r.Synthetic {
		t.Fatal("a synthetic measurement was not labelled")
	}
}

func TestTheWindowIsClippedToSowing(t *testing.T) {
	from, to, ok := Window("2026-07-20", incident)
	if !ok || from != "2026-07-20" || to != "2026-10-03" {
		t.Fatalf("window = %s..%s", from, to)
	}
	// A sowing date before the sixty days leaves the sixty days.
	from, _, _ = Window("2026-05-01", incident)
	if from != "2026-07-05" {
		t.Fatalf("from = %s, want 2026-07-05", from)
	}
	if _, _, ok := Window("", "not a date"); ok {
		t.Fatal("a bad incident date produced a window")
	}
}

func TestRelevanceIsStatedForEveryCalamity(t *testing.T) {
	for _, c := range []string{
		"flood", "cyclone", "hailstorm", "unseasonal_rain", "drought",
		"pest_attack", "landslide", "cloudburst", "frost", "fire", "",
	} {
		if Relevance(c) == "" {
			t.Errorf("no relevance for %q", c)
		}
	}
}

// A reading with nothing notable still says "no findings" as an empty list.
// null would leave every reader to guess whether it meant none or not computed.
func TestFindingsAreAnEmptyListNotNull(t *testing.T) {
	opt := append(history(0.32), pass("2026-09-02", 0, 0.265, 0.27))
	r, _, ok := Analyse(result(opt), incident, "flood")
	if !ok {
		t.Fatal("a measurement near the incident was discarded")
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), `"findings":null`) {
		t.Fatalf("findings serialised as null: %s", raw)
	}
}
