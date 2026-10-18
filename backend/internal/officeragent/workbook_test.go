package officeragent

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Filling an office's own format, which is never shaped the way code would like.
//
// The interesting cases are all about the header row: real formats open with a
// title, the name of the office, the season and a blank line, and assuming row one
// produces a mapping built from the report's title.

// sheet builds a workbook from rows, as an office would send one.
func sheet(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()

	for i, row := range rows {
		for j, cell := range row {
			ref, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.SetCellValue("Sheet1", ref, cell); err != nil {
				t.Fatal(err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTheHeaderRowIsFoundBeneathATitle(t *testing.T) {
	// The ordinary shape of a real format: a title, the office, the season, a
	// blank line, then the headings. Row one is almost never the header.
	raw := sheet(t, [][]any{
		{"Crop Loss Statement — Kharif 2026"},
		{"Office of the Tehsildar, Barshi"},
		{},
		{"Sr.", "Claim No.", "Farmer", "Village", "Affected Area (Ha)", "% Loss"},
		{1, "CLR-2026-496-00001", "Example", "Barshi", 1.2, 70},
	})

	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.HeaderRow != 4 {
		t.Fatalf("header row %d, want 4", tmpl.HeaderRow)
	}
	if tmpl.Headings[1] != "Claim No." {
		t.Fatalf("headings %q", tmpl.Headings)
	}
}

func TestATitleAloneIsNotAHeaderRow(t *testing.T) {
	// One long cell is a title. A format worth filling has columns, and treating a
	// title as a header row would map the whole report onto one field.
	raw := sheet(t, [][]any{
		{"Crop Loss Statement — Kharif 2026"},
		{"Office of the Tehsildar, Barshi"},
	})
	if _, err := ReadTemplate(bytes.NewReader(raw)); err == nil {
		t.Fatal("a workbook of two titles was accepted as a format")
	}
}

func TestARowOfFiguresDoesNotWinOverTheHeadings(t *testing.T) {
	// Data that has strayed above the headings. Headings are words; a row of
	// numbers is not a header however many cells it fills.
	raw := sheet(t, [][]any{
		{1, 2, 3, 4, 5, 6, 7, 8},
		{"Sr.", "Claim No.", "Farmer", "Village"},
	})
	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.HeaderRow != 2 {
		t.Fatalf("header row %d, want 2 -- a row of figures won", tmpl.HeaderRow)
	}
}

func TestMarathiHeadingsAreReadAsHeadings(t *testing.T) {
	// The common case in the trial districts, and the reason a model does the
	// mapping at all.
	raw := sheet(t, [][]any{
		{"अ.क्र.", "नोंद क्रमांक", "शेतकऱ्याचे नाव", "गाव", "बाधित क्षेत्र"},
	})
	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Headings) != 5 || tmpl.Headings[3] != "गाव" {
		t.Fatalf("headings %q", tmpl.Headings)
	}
}

func TestBlankColumnsKeepTheirPlace(t *testing.T) {
	// A blank column in the middle of a format is a column. Dropping it would
	// shift every heading after it and put the figures one column left.
	raw := sheet(t, [][]any{
		{"Sr.", "", "Farmer", "", "Village"},
	})
	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Headings) != 5 {
		t.Fatalf("got %d headings, want 5: %q", len(tmpl.Headings), tmpl.Headings)
	}
	if tmpl.Headings[2] != "Farmer" {
		t.Fatalf("Farmer moved to %q", tmpl.Headings)
	}
}

func TestValuesAreWrittenBeneathTheHeadings(t *testing.T) {
	raw := sheet(t, [][]any{
		{"Crop Loss Statement"},
		{"Sr.", "Claim No.", "Farmer", "Affected Area (Ha)"},
	})
	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	rows := []map[string]any{
		Row(report.Summary{
			Reference:      "CLR-2026-496-00001",
			FarmerName:     "Harichndra Dhumal",
			AreaAffectedHa: 1.2,
			OccurredAt:     time.Now(),
			ReportedAt:     time.Now(),
		}, 1),
	}
	fields := []string{"serial", "reference", "farmer_name", "area_affected_ha"}

	out, err := Fill(bytes.NewReader(raw), tmpl, fields, rows)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	// One row below the headings, which were on row 2.
	for ref, want := range map[string]string{
		"A3": "1",
		"B3": "CLR-2026-496-00001",
		"C3": "Harichndra Dhumal",
		"D3": "1.2",
	} {
		got, err := out.GetCellValue(tmpl.Sheet, ref)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", ref, got, want)
		}
	}
}

func TestAnUnmappedColumnIsLeftExactlyAsItWas(t *testing.T) {
	// A column this cannot fill keeps whatever the office put there, which is
	// usually a formula they rely on. Writing a blank would delete it.
	raw := sheet(t, [][]any{
		{"Sr.", "Compensation (Rs.)", "Farmer"},
		{"", "=B2*100", ""},
	})
	tmpl, err := ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	before, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	wasThere, _ := before.GetCellValue(tmpl.Sheet, "B2")
	before.Close()

	out, err := Fill(bytes.NewReader(raw), tmpl,
		[]string{"serial", "", "farmer_name"},
		[]map[string]any{Row(report.Summary{FarmerName: "X"}, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	// Row 2 is the row beneath the headings on row 1, so it is the row written to.
	// The middle column had no field and must be untouched.
	got, _ := out.GetCellValue(tmpl.Sheet, "B2")
	if got != wasThere {
		t.Fatalf("an unmapped column was overwritten: %q became %q", wasThere, got)
	}
}

func TestSomethingThatIsNotAWorkbookIsRefused(t *testing.T) {
	if _, err := ReadTemplate(bytes.NewReader([]byte("%PDF-1.7\nnot a workbook"))); err == nil {
		t.Fatal("a PDF was accepted as a workbook")
	}
}

func TestTheVocabularyExcludesTheIdentifiersThatMustNotTravel(t *testing.T) {
	// The closed list is the data boundary for this feature: nothing outside it can
	// reach a filled report, which is why these are absent rather than filtered.
	names := FieldNames()
	for _, forbidden := range []string{
		"farmer_id", "aadhaar", "aadhaar_last4", "aadhaar_hash", "mobile_no",
	} {
		if names[forbidden] {
			t.Fatalf("%q is offered as a report field", forbidden)
		}
	}
	// And the name is present, because a report without it is useless to an
	// officer -- allowed deliberately.
	if !names["farmer_name"] {
		t.Fatal("farmer_name is missing; a filled report needs it")
	}
}

func TestEveryFieldCanProduceAValue(t *testing.T) {
	// A field in the vocabulary that cannot be filled is a column the model will
	// map and nothing will write.
	row := Row(report.Summary{OccurredAt: time.Now(), ReportedAt: time.Now()}, 1)
	for _, f := range Fields {
		if _, ok := row[f.Name]; !ok {
			t.Fatalf("field %q is offered but produces nothing", f.Name)
		}
	}
}
