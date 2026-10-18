package dossier

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
)

func fullInputs() Inputs {
	loss := 58.0
	return Inputs{
		State:       Final,
		Reference:   "ASMT-2026-000431",
		GeneratedAt: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC),
		Report: &report.Report{
			Reference:    "RPT-2026-000431",
			FarmerName:   "एक शेतकरी",
			CalamityType: "hailstorm",
			OccurredAt:   time.Date(2026, 8, 26, 11, 10, 0, 0, time.UTC),
			Place: report.Place{
				VillageName:     "Kurduwadi",
				SubDistrictName: "Barshi",
				DistrictName:    "Solapur",
			},
		},
		Item: &report.Item{
			LineNo: 1, SurveyNumber: "453", FarmID: "f1",
			CropName: "soybean", AreaSownHa: 1.6,
			AreaAffectedHa: 1.2, DamagePercent: 70,
		},
		OwnerExtentHa: 1.6,
		Boundary: &BoundaryProvenance{
			WalkedBy: "T. Patil", WalkedAt: "2026-07-02",
			Corners: 6, WorstAccuracyM: 7, AreaHa: 1.58,
		},
		Measurements:     []fieldwork.Measurement{fullMeasurement()},
		Weather:          fullWindow(),
		OfficialRainfall: barshiGauges(),
		Satellite:        fullSatellite(),
		SARFinding:       fullFinding(sar.FindingLodging),
		SARReading:       fullReading(),
		Denominators:     fullDenominators(),
		Signals: []claims.Signal{{
			Code:     "repeat_claimant",
			Severity: claims.SeverityInfo,
			Detail:   "This farmer filed in two of the last three seasons.",
			Innocent: "Two of the last three seasons had notified calamities in " +
				"this village, so most farmers here filed in both.",
		}},
		AI: &AIAssessment{
			LossPercent: &loss, Model: "gemini-3.1-pro-preview",
			PromptVersion: "v4", RanAt: "2026-08-30",
			Reasoning: "The canopy fall and the lodging signature agree.",
		},
		Findings: []OfficerFinding{
			{Role: "Talathi", Name: "T. Patil", LossPercent: &loss,
				RecordedAt: "2026-08-29", Blind: true},
		},
		Consensus: &Consensus{
			DeltaPoints: 3, Route: "approve",
			DecidedBy: "Collector, Solapur", DecidedAt: "2026-08-31",
		},
		Evidence: []EvidenceItem{{
			Kind: "damage_closeup", CapturedAt: "2026-08-27T09:12:00Z",
			AccuracyM: 6, Geofence: "inside the boundary",
			SHA256: "9f2b1c",
		}},
		AssessedLossPercent: &loss,
		Triage:              "AMBER_STANDARD_AUDIT",
		AppealWindow:        "15 days from the date of this report",
	}
}

func TestTheDefaultMastheadClaimsNoAuthorityItLacks(t *testing.T) {
	// It once read "Government of Maharashtra, Revenue and Forest Department"
	// and said it was made under Government Resolutions. A document carrying a
	// state masthead and a legal basis it does not have is a forged official
	// record whatever the intent, and a farmer holding one has no way to tell
	// it from a report the Collector actually issued.
	//
	// A revenue office that adopts this platform sets its own Issuer. That is
	// an officer's decision, taken on their own authority, and not a string
	// this package ships with.
	std := DefaultStandard()

	masthead := strings.Join([]string{
		std.Issuer.Government.MR, std.Issuer.Government.EN,
		std.Issuer.Department.MR, std.Issuer.Department.EN,
		strings.Join(std.LegalBasis.In(Marathi), " "),
		strings.Join(std.LegalBasis.In(English), " "),
	}, " ")

	for _, claim := range []string{
		"Government of", "महाराष्ट्र शासन", "शासन निर्णय",
		"Government Resolution", "Revenue and Forest",
	} {
		if strings.Contains(masthead, claim) {
			t.Errorf("the default masthead claims %q, which this platform is not", claim)
		}
	}

	if !strings.Contains(std.Issuer.Government.EN, "eSurvey") {
		t.Error("the default masthead should name the system that produced the report")
	}
	// And it must say plainly where the decision actually rests.
	if !strings.Contains(strings.Join(std.LegalBasis.In(English), " "),
		"competent authority") {
		t.Error("the basis must say the decision belongs to the competent authority")
	}
}

func TestAProvisionalReportCarriesNoFigure(t *testing.T) {
	// Not an estimate, not a range. A provisional number becomes the number
	// everybody remembers and no later correction catches up with it, so the
	// rule is enforced here rather than trusted to a caller who will be under
	// time pressure during a real event.
	in := fullInputs()
	in.State = Provisional

	d := Build(in)

	if d.AssessedLossPercent != nil {
		t.Errorf("a provisional report printed a figure: %v", *d.AssessedLossPercent)
	}
	if len(d.DecidedBy) != 0 {
		t.Error("a provisional report named a deciding officer")
	}

	var out bytes.Buffer
	if err := d.Print(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No decision has been reached yet") {
		t.Error("the provisional stamp should say no decision has been reached")
	}
}

func TestAFinalReportCarriesTheFigureAndWhoDecided(t *testing.T) {
	d := Build(fullInputs())

	if d.AssessedLossPercent == nil {
		t.Fatal("a final report must carry the figure")
	}
	if len(d.DecidedBy) == 0 {
		t.Error("a final report must name who decided")
	}
}

func TestTheWholeDocumentIsNeverHalfWritten(t *testing.T) {
	// The bilingual guarantee, over an assembled report rather than a single
	// builder: every section, every row, both shapes.
	for _, in := range []Inputs{fullInputs(), {State: Provisional}} {
		d := Build(in)
		for _, s := range d.Sections {
			checkSection(t, s)
		}
	}
}

func TestBothHalvesAreRenderedInFull(t *testing.T) {
	// The same report twice, not interleaved and not a parallel column, so
	// either half can be detached and read alone.
	var out bytes.Buffer
	if err := Build(fullInputs()).Print(&out); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	if strings.Count(html, `class="report"`) != 2 {
		t.Fatalf("expected two complete documents, found %d",
			strings.Count(html, `class="report"`))
	}
	if !strings.Contains(html, `lang="mr"`) || !strings.Contains(html, `lang="en"`) {
		t.Error("both halves should declare their language")
	}

	// The Marathi half comes first, because it is the one the farmer reads.
	if strings.Index(html, `lang="mr"`) > strings.Index(html, `lang="en"`) {
		t.Error("the Marathi half must be printed first")
	}

	for _, want := range []string{
		"Field measurement", "शेतातील प्रत्यक्ष मोजणी",
		"What this report cannot say", "या अहवालाच्या मर्यादा",
		"How to object", "आक्षेप कसा नोंदवावा",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered report is missing %q", want)
		}
	}
}

func TestTheLimitsSectionRestatesWhatWasAbsent(t *testing.T) {
	// Every absence is stated where it happened, but a reader working through
	// fifteen sections will not hold them all at once, and the combined
	// effect is the thing that matters.
	in := fullInputs()
	in.Measurements = nil
	in.Satellite = nil
	in.Denominators = nil

	d := Build(in)
	limits, ok := d.Section("15")
	if !ok {
		t.Fatal("§14 should always be present")
	}

	joined := strings.Join(limits.Notes.In(English), " ")
	for _, want := range []string{"§4", "§6"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the limits should restate the absence in %s: %q", want, joined)
		}
	}
	// And the two standing limits, true of every report until they are not.
	if !strings.Contains(joined, "not been validated") {
		t.Error("the limits must say the figures are unvalidated")
	}
	if !strings.Contains(joined, "AgriStack") {
		t.Error("the limits must say where the boundary came from")
	}
}

func TestTheSummaryLeadsOnlyWithWhatDecidedTheFigure(t *testing.T) {
	// A summary that led with a striking NDVI figure would put a
	// corroborating number on the page a farmer reads first, which is the
	// whole error the role markers exist to prevent.
	d := Build(fullInputs())

	if len(d.Headline) == 0 {
		t.Fatal("the summary should carry something")
	}
	for _, r := range d.Headline {
		if r.Role != Determines {
			t.Errorf("%q is on the first page and did not determine the figure",
				r.Parameter.EN)
		}
	}
}

func TestTheDigestCoversTheInputs(t *testing.T) {
	// M7's exit criterion has to become checkable by somebody who is not a
	// database administrator. Re-running Build over the recorded inputs must
	// produce the same digest; if it does not, either the inputs or the
	// report has moved.
	in := fullInputs()

	if Build(in).InputsDigest != Build(in).InputsDigest {
		t.Fatal("the same inputs produced two digests")
	}
	if Build(in).InputsDigest == "" {
		t.Fatal("a report with inputs has no digest")
	}

	changed := fullInputs()
	other := 61.0
	changed.AssessedLossPercent = &other

	if Build(in).InputsDigest == Build(changed).InputsDigest {
		t.Error("a changed loss figure left the digest unmoved, so the digest " +
			"guarantees nothing")
	}
}

func TestAnInnocentExplanationIsPrintedBesideEveryFlag(t *testing.T) {
	// claims.Signal makes Innocent required so a flag cannot be read as an
	// accusation. This is the document where being accused by implication
	// would be hardest to answer.
	d := Build(fullInputs())
	notes, ok := d.Section("10")
	if !ok {
		t.Fatal("§10 should be present when there are signals")
	}

	for _, r := range notes.Rows {
		if r.Effect.Empty() {
			t.Errorf("%q prints without its innocent explanation", r.Parameter.EN)
		}
		if r.Role == Determines {
			t.Errorf("%q is a context note and must not determine anything",
				r.Parameter.EN)
		}
	}
}

func TestAnEmptyClaimStillProducesAReadableReport(t *testing.T) {
	// The shape a provisional report takes on the day of an event, before any
	// officer has been to the field. It must not panic and must not print a
	// document that looks complete.
	var out bytes.Buffer
	d := Build(Inputs{State: Provisional, Reference: "ASMT-EMPTY"})

	if err := d.Print(&out); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	// A hand count is no longer asked for, so §4 says what the figure does
	// rest on instead of reporting a missing count on every report printed.
	if !strings.Contains(html, "not asked for in this system") {
		t.Error("it must say a hand count is not part of this system")
	}
	if !strings.Contains(html, "the panel&#39;s findings") {
		t.Error("and what the figure rests on instead")
	}
}

// TestWriteSample writes a rendered report to a file so it can be looked at
// in a browser. A layout nobody has seen is a layout nobody has checked, and
// the assertions above cannot tell whether a Devanagari conjunct is clipped
// or a six-column table has become unreadable at A4.
//
// Off by default: set ESURVEY_SAMPLE_REPORT to a path to write one.
func TestWriteSample(t *testing.T) {
	path := os.Getenv("ESURVEY_SAMPLE_REPORT")
	if path == "" {
		t.Skip("set ESURVEY_SAMPLE_REPORT to write a sample")
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := Build(fullInputs()).Print(f); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func TestTheReportCarriesItsOwnType(t *testing.T) {
	// The stack used to name faces and hope. A machine without any of them
	// falls back to something with no Devanagari coverage and the farmer's
	// half prints as empty boxes -- invisible on a laptop that has Nirmala UI,
	// total in an office that does not. The PDF converter also refuses every
	// remote fetch, so a linked webfont would simply not arrive.
	var out bytes.Buffer
	if err := Build(fullInputs()).Print(&out); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	if !strings.Contains(html, "data:font/woff2;base64,") {
		t.Fatal("no font is embedded; the document depends on the host's fonts")
	}
	if strings.Count(html, "@font-face") != 2 {
		t.Errorf("expected a Devanagari face and a Latin one, found %d",
			strings.Count(html, "@font-face"))
	}

	// One family, two unicode-ranges: a Devanagari codepoint takes the
	// Devanagari file and a Latin one takes the Latin file, in the middle of a
	// sentence, without the stylesheet knowing which language a cell holds --
	// which matters because most cells hold both.
	if !strings.Contains(html, "U+0900-097F") {
		t.Error("the Devanagari range is not declared")
	}
	if !strings.Contains(html, "U+0000-00FF") {
		t.Error("the Latin range is not declared")
	}
	if strings.Count(html, FamilyName) < 3 {
		t.Error("both faces should answer to one family name")
	}
}

func TestTheFontsAreBuiltOnceAndAreStable(t *testing.T) {
	// Base64 of 160 KB on every report would be work repeated to produce a
	// byte-for-byte identical result, and a report whose bytes moved between
	// renders would break the digest that makes it checkable.
	a, b := fontFaces(), fontFaces()
	if a != b {
		t.Error("the embedded font CSS is not stable between calls")
	}
	if len(a) < 100_000 {
		t.Errorf("the font CSS looks too small to contain both faces: %d bytes",
			len(a))
	}
}

func TestAnEmptyColumnIsNotPrinted(t *testing.T) {
	// A column empty for every row of a section still takes its width, which
	// leaves a dead band down the page and squeezes the prose the report
	// exists to carry. Dropping it hides nothing: there was nothing in it.
	//
	// The claim section cites no evidence -- it is what the farmer said. The
	// field measurement cites the photograph each count was taken against.
	d := Build(fullInputs())
	var out bytes.Buffer
	if err := d.Print(&out); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	if !strings.Contains(html, "no-ev") {
		t.Error("no section dropped its evidence column, though several cite nothing")
	}
	// And where evidence exists it must still be printed.
	if !strings.Contains(html, "Photograph c1") {
		t.Error("the quadrat's photograph reference was dropped")
	}
}

func TestWhatDeterminesTheFigureIsDecidedDeliberately(t *testing.T) {
	// The document's claim about its own evidence, pinned over a whole report.
	//
	// It changed once already, and for a reason: officers used to stand in the
	// field, so the model was a second opinion on an independent measurement
	// and was marked Corroborates. Nobody visits now. The officers read the
	// same photographs the model read, so the photographs are the observation
	// and the model's reading is part of how the figure was reached.
	//
	// If this list grows, the report has started claiming something new about
	// where its number comes from, and somebody should have decided that
	// rather than discovered it.
	want := map[string]bool{
		// Read off the photographs.
		"Stand loss":             true,
		"Lodged plants":          true,
		"Harvested organ damage": true,
		// What the percentage is a percentage of.
		"Issued threshold yield": true,
		// The reading of the evidence, by machine and by people.
		"The system's conclusion":     true,
		"Talathi":                     true,
		"Difference between findings": true,
		// The evidence itself, which with no field visit is the observation.
		"damage_closeup": true,
	}

	for _, r := range Build(fullInputs()).Determining() {
		if !want[r.Parameter.EN] {
			t.Errorf("%q is marked Determines and is not in the agreed set",
				r.Parameter.EN)
		}
	}
}

func TestWithNoFieldVisitThePhotographsAreTheObservation(t *testing.T) {
	// Not supporting material. Every figure in the document is a reading of
	// them, so marking them as merely corroborating would describe a
	// measurement that was never taken.
	d := Build(fullInputs())

	ev, ok := d.Section("14")
	if !ok || len(ev.Rows) == 0 {
		t.Fatal("the evidence section should list the photographs")
	}
	if ev.Rows[0].Role != Determines {
		t.Error("the photographs are what the assessment was made from")
	}

	// And a claim with none of them says so as the gravest absence it has.
	in := fullInputs()
	in.Evidence = nil
	empty, _ := Build(in).Section("14")
	if !strings.Contains(empty.Preamble.EN, "nothing for anybody to examine") {
		t.Errorf("a claim with no photographs must say what that means: %q",
			empty.Preamble.EN)
	}
}

func TestTheRenderedReportCarriesTheGaugeFigureInBothHalves(t *testing.T) {
	// The whole chain, through the templates: a figure that reaches the section
	// builder and not the rendered page helps nobody, and §5 is the section an
	// officer reads to decide whether the rain failed.
	var out bytes.Buffer
	if err := Build(fullInputs()).Print(&out); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	for _, want := range []string{
		// The state's figure, its normal, and its departure.
		"286.0 mm of a normal 516.6 mm",
		"55.4% of normal",
		"Season rainfall against normal",
		"हंगामातील पाऊस",
		// The unit it is for, in both halves, because it is not the parcel's.
		"Barshi taluka",
		"Barshi तालुका",
		// The model, marked as a model.
		"reanalysis model",
		"हवामान प्रतिमान",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered report is missing %q", want)
		}
	}

	// The gauge row must come before the model's total in the document, in each
	// half. A reader scanning down for a rainfall figure finds the first one.
	mr := html[strings.Index(html, `lang="mr"`):strings.Index(html, `lang="en"`)]
	en := html[strings.Index(html, `lang="en"`):]
	for name, half := range map[string]string{"Marathi": mr, "English": en} {
		gauge := strings.Index(half, "516.6")
		model := strings.Index(half, "reanalysis")
		if name == "Marathi" {
			model = strings.Index(half, "प्रतिमान")
		}
		if gauge < 0 || model < 0 {
			t.Errorf("%s half: gauge=%d model=%d; one of them is missing",
				name, gauge, model)
			continue
		}
		if gauge > model {
			t.Errorf("%s half prints the model's total above the state's gauges",
				name)
		}
	}
}
