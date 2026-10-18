package report

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The printed form is the artefact an officer and a farmer sign, and it is the
// one output of this platform that leaves it on paper. A template that panics
// on a sparse report, or renders a farmer's name as markup, or drops a
// calamity nobody translated, fails at the moment somebody is standing in a
// field waiting for a receipt.

// full is a report with every optional field populated.
func full() *Report {
	signed := time.Date(2026, 3, 2, 10, 15, 0, 0, ist)
	acked := time.Date(2026, 3, 3, 16, 40, 0, 0, ist)

	return &Report{
		ID:                   "11111111-1111-1111-1111-111111111111",
		Reference:            "CLR-2026-27-000123",
		SurveyEventReference: "SE-2026-27-001",

		FarmerName:    "सौ. सुनीता पवार",
		FarmerAddress: "मु.पो. वडगाव",
		FarmerMobile:  "9876543210",

		Place: Place{
			VillageCode: "562100", VillageName: "वडगाव",
			CircleCode: "C-11", CircleName: "वडगाव मंडळ",
			SubDistrictCode: "4021", SubDistrictName: "हवेली",
			DistrictCode: "521", DistrictName: "पुणे",
		},

		Season:       "kharif",
		CropYear:     "2026-27",
		LossCategory: "standing_crop",
		CalamityType: "hailstorm",
		OccurredAt:   time.Date(2026, 3, 1, 16, 40, 0, 0, ist),

		ReportedAt:      time.Date(2026, 3, 2, 9, 0, 0, 0, ist),
		ReportedChannel: "farmer_app",

		DeclarationAccepted: true,
		DocumentsPromised:   []string{"7_12", "8a"},

		FarmerSignatureKind: "thumb",
		FarmerSignedAt:      &signed,

		AcknowledgedByName: "श्री. म. जगताप",
		AcknowledgedAt:     &acked,

		State: "acknowledged",

		Items: []Item{
			{LineNo: 1, SurveyNumber: "42", SubSurveyNumber: "1/अ",
				CropName: "सोयाबीन", AreaSownHa: 1.6, AreaAffectedHa: 1.2,
				DamagePercent: 70},
			{LineNo: 2, SurveyNumber: "43", CropName: "तूर",
				AreaSownHa: 0.4, AreaAffectedHa: 0.1, DamagePercent: 25},
		},

		CreatedAt: time.Date(2026, 3, 2, 9, 0, 0, 0, ist),
	}
}

func render(t *testing.T, r *Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.Print(&buf); err != nil {
		t.Fatalf("printing: %v", err)
	}
	return buf.String()
}

func TestThePrintedFormCarriesWhatItIsAboutToBeSignedFor(t *testing.T) {
	out := render(t, full())

	// Not an exhaustive layout check — the things whose absence would make the
	// paper useless as a record.
	for _, want := range []string{
		"CLR-2026-27-000123", // the reference the farmer quotes to ask about it
		"SE-2026-27-001",     // the notification it was filed under
		"सौ. सुनीता पवार",
		"वडगाव", "हवेली", "पुणे",
		"सोयाबीन", "तूर",
		"42", "43",
		"श्री. म. जगताप", // who acknowledged it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed form does not carry %q", want)
		}
	}
}

func TestTheFormSaysWhatHappenedInBothLanguages(t *testing.T) {
	// Marathi for the people signing, English for the offices that aggregate.
	out := render(t, full())

	for _, want := range []string{"गारपीट", "Hailstorm", "खरीप", "Kharif"} {
		if !strings.Contains(out, want) {
			t.Errorf("the form is missing %q", want)
		}
	}
}

func TestASparseReportPrintsWithoutPanicking(t *testing.T) {
	// The ordinary shape of a draft: no signature, no acknowledgement, no
	// lines yet. Every one of those is a nil pointer or an empty slice, and a
	// template that dereferences them fails on the first draft anybody prints.
	r := &Report{
		Reference:  "CLR-2026-27-000001",
		FarmerName: "अनामिक",
		State:      "draft",
		Place:      Place{VillageName: "वडगाव"},
		OccurredAt: time.Date(2026, 3, 1, 0, 0, 0, 0, ist),
		ReportedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, ist),
		CreatedAt:  time.Date(2026, 3, 1, 0, 0, 0, 0, ist),
	}

	out := render(t, r)
	if !strings.Contains(out, "CLR-2026-27-000001") {
		t.Error("the reference is missing from a draft")
	}
}

func TestAReportWithNoLinesDoesNotDivideByZero(t *testing.T) {
	// WeightedDamagePercent divides by the sown area. A report with no lines
	// has none, and NaN on a signed form is worse than a blank.
	r := full()
	r.Items = nil

	out := render(t, r)
	if strings.Contains(out, "NaN") {
		t.Error("the form printed NaN")
	}
	if strings.Contains(strings.ToLower(out), "+inf") {
		t.Error("the form printed an infinity")
	}
}

func TestAFarmerNameIsNeverMarkup(t *testing.T) {
	// Names arrive from a registry and from a farmer typing into a phone. A
	// name that becomes a script tag is stored XSS on an officer's screen, and
	// the officer opens these all day.
	r := full()
	r.FarmerName = `<script>alert(1)</script>`
	r.FarmerAddress = `" onmouseover="alert(2)`
	r.Items[0].CropName = `<img src=x onerror=alert(3)>`

	out := render(t, r)

	for _, bad := range []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(3)>",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("markup survived into the page: %q", bad)
		}
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("the name was dropped rather than escaped")
	}
}

func TestAnUntranslatedValueStillPrintsSomething(t *testing.T) {
	// A calamity type added to the database before the label map catches up.
	// Printing the raw code is a small ugliness; printing an empty cell on a
	// form somebody signs is a record that does not say what happened.
	r := full()
	r.CalamityType = "locust_swarm"

	out := render(t, r)
	if !strings.Contains(out, "locust_swarm") {
		t.Error("an unlabelled calamity printed as nothing")
	}
}

func TestAnUnknownValueCannotInjectMarkupThroughTheFallback(t *testing.T) {
	// The fallback path returns template.HTML, which is unescaped by
	// definition. It escapes first; this is the test that it kept doing so.
	got := string(label("calamity", `<b>x</b>`))
	if strings.Contains(got, "<b>") {
		t.Errorf("the fallback emitted raw markup: %q", got)
	}
}

func TestTheDocumentListNamesTheDocuments(t *testing.T) {
	got := string(joinDocs([]string{"7_12", "8a"}))
	for _, want := range []string{"७/१२ उतारा", "7/12 extract", "८-अ उतारा"} {
		if !strings.Contains(got, want) {
			t.Errorf("the document list is missing %q: %s", want, got)
		}
	}
}

func TestNoDocumentsPromisedPrintsADash(t *testing.T) {
	// Distinguishable from a document whose name is blank.
	got := string(joinDocs(nil))
	if !strings.Contains(got, "—") {
		t.Errorf("an empty document list rendered as %q", got)
	}
}

func TestAnUnknownDocumentIsEscaped(t *testing.T) {
	got := string(joinDocs([]string{`<b>forged</b>`}))
	if strings.Contains(got, "<b>") {
		t.Errorf("an unknown document emitted raw markup: %q", got)
	}
}

func TestTimesPrintInTheZoneTheFormIsReadIn(t *testing.T) {
	// A hailstorm at 16:40 local is not a useful fact rendered as 11:10 UTC on
	// a form an officer signs.
	r := full()
	r.OccurredAt = time.Date(2026, 3, 1, 11, 10, 0, 0, time.UTC) // 16:40 IST

	out := render(t, r)
	if !strings.Contains(out, "16:40") {
		t.Error("the incident time did not print in IST")
	}
	if strings.Contains(out, "11:10") {
		t.Error("the incident time printed in UTC")
	}
}

func TestAMissingTimePrintsADashRatherThanTheZeroYear(t *testing.T) {
	// An unacknowledged report has no acknowledgement time. Formatting nil as
	// a zero time would put 01-01-0001 on the form.
	r := full()
	r.AcknowledgedAt = nil
	r.FarmerSignedAt = nil

	// Matched as a whole date: a reference like CLR-2026-27-000123 contains
	// the digits "0001" and says nothing about times.
	out := render(t, r)
	if strings.Contains(out, "01-01-0001") {
		t.Error("a missing time printed as the zero year")
	}
	if !strings.Contains(out, "—") {
		t.Error("a missing time printed as neither a dash nor a date")
	}
}

func TestNumbersPrintTheWayAPersonWritesThem(t *testing.T) {
	// The area on the form is copied by hand into other registers. 1.6000
	// invites a transcription error that 1.6 does not.
	cases := []struct {
		in     float64
		places int
		want   string
	}{
		{1.6, 4, "1.6"},
		{1.0, 4, "1"},
		{0, 4, "0"},
		{0.125, 4, "0.125"},
		{70, 2, "70"},
		{70.5, 2, "70.5"},
		{2.0000, 4, "2"},
		{0.0001, 4, "0.0001"},
	}
	for _, c := range cases {
		if got := trim(c.in, c.places); got != c.want {
			t.Errorf("trim(%v, %d) = %q, want %q", c.in, c.places, got, c.want)
		}
	}
}

func TestTheWeightedPercentIsNotTheMeanOfTheLines(t *testing.T) {
	// A total loss on a tenth of a hectare and a scratch on four hectares do
	// not average to half. The template prints this number beside a signature.
	r := &Report{Items: []Item{
		{AreaSownHa: 0.1, AreaAffectedHa: 0.1, DamagePercent: 100},
		{AreaSownHa: 4.0, AreaAffectedHa: 0.1, DamagePercent: 5},
	}}

	// (0.1*100 + 0.1*5) / 4.1
	const want = 2.5609756097560976
	got := r.WeightedDamagePercent()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("weighted damage = %v, want %v", got, want)
	}

	// The mean of the line percentages would be 52.5, which is the number this
	// exists not to print.
	if got > 50 {
		t.Errorf("weighted damage = %v; that is the mean, not the weighting", got)
	}
}

func TestTheTotalsAreTheSumOfTheLines(t *testing.T) {
	r := full()
	if got := r.TotalSownHa(); got != 2.0 {
		t.Errorf("sown = %v, want 2.0", got)
	}
	if got := r.TotalAffectedHa(); got < 1.2999 || got > 1.3001 {
		t.Errorf("affected = %v, want 1.3", got)
	}
}

func TestTheFormIsAWholeHTMLPage(t *testing.T) {
	// It is opened in a browser and printed from there. A fragment renders
	// unstyled, which on a form means the print layout is gone.
	out := render(t, full())

	if !strings.Contains(strings.ToLower(out), "<!doctype html") {
		t.Error("the printed form is not a complete document")
	}
	if !strings.Contains(out, "@media print") {
		t.Error("the form carries no print styles")
	}
}

// The receipt a farmer is handed must not claim to come from a government.
//
// It did. The masthead read "महाराष्ट्र शासन · महसूल व कृषी विभाग / Government
// of Maharashtra · Revenue and Agriculture Department", which is a forged
// official record whatever the intent behind the template: a farmer holding
// the page cannot tell it from something the Collector issued, and neither
// could a court.
//
// The same correction was made to the assessment dossier some time ago and
// has a test of its own -- see TestTheDefaultMastheadClaimsNoAuthorityItLacks.
// This template was missed, which is exactly why the rule needs a test on
// both documents rather than on the one somebody happened to be looking at.
//
// A revenue office that adopts this platform sets its own masthead, on its
// own authority. It is not a string this repository ships with.
func TestThePrintedFormClaimsNoAuthorityItLacks(t *testing.T) {
	out := render(t, full())

	for _, claim := range []string{
		"Government of", "महाराष्ट्र शासन", "शासन निर्णय",
		"Revenue and Agriculture", "Revenue and Forest", "महसूल व कृषी विभाग",
	} {
		if strings.Contains(out, claim) {
			t.Errorf("the printed report claims authority it does not have: %q", claim)
		}
	}

	// And it still says what it is. A masthead stripped to nothing would pass
	// the check above and leave a farmer holding an unattributed page.
	for _, own := range []string{"eSurvey", "पीक नुकसान मूल्यांकन"} {
		if !strings.Contains(out, own) {
			t.Errorf("the printed report does not name the platform that made it: %q", own)
		}
	}
}
