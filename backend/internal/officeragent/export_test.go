package officeragent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Giving an officer their figures back in five formats.
//
// Three things are worth testing here and the rest is encoding. First, that the
// document an officer opens is readable: a Marathi name has to survive the whole
// way through a zip of XML and come out declared as a complex script, which is
// the failure that produces a page of empty boxes. Second, that a request that
// would produce a document nobody can use -- past the row cap, or naming a column
// that does not exist -- is refused in words rather than quietly trimmed. Third,
// that nothing but the caller's own scope decides what is in it, which is checked
// against the shape of the request type and not only against behaviour.
//
// None of this needs a database. The store is stood in for by a fake that applies
// the same scope predicate the real one does; the predicate itself is the report
// store's own test, with its own database.

// exportDistrictA and exportDistrictB are two postings, so that an export can be
// shown not to cross between them.
const (
	exportDistrictA = "11111111-1111-1111-1111-111111111111"
	exportDistrictB = "22222222-2222-2222-2222-222222222222"
)

// exportClaim is one claim in the fake store, with the posting it sits under.
type exportClaim struct {
	jurisdiction string
	summary      report.Summary
}

// exportStore stands in for report.Store.
//
// It applies scope the way the real store's predicate does -- a claim is visible
// when its posting is one the caller holds -- and it records what was asked of it,
// so that a test can say not only that the right rows came back but that nothing
// but the scoped list was ever called.
type exportStore struct {
	claims []exportClaim

	sawScopes []rbac.Scope
	sawFilter report.Filter
	calls     []string
}

func (s *exportStore) List(
	_ context.Context, f report.Filter,
) (page.Result[report.Summary], error) {
	s.calls = append(s.calls, "List")
	s.sawScopes = append(s.sawScopes, f.Scope)
	s.sawFilter = f

	var out []report.Summary
	for _, c := range s.claims {
		visible := false
		for _, id := range f.Scope.JurisdictionIDs {
			if id == c.jurisdiction {
				visible = true
			}
		}
		if !visible {
			continue
		}
		if f.State != "" && f.State != c.summary.State {
			continue
		}
		out = append(out, c.summary)
		if f.Page.Limit > 0 && len(out) >= f.Page.Limit {
			break
		}
	}
	return page.Result[report.Summary]{Items: out, Total: len(out)}, nil
}

func (s *exportStore) Get(
	context.Context, string, rbac.Scope,
) (*report.Report, error) {
	s.calls = append(s.calls, "Get")
	return nil, errors.New("not found")
}

func (s *exportStore) ByReference(
	context.Context, string, rbac.Scope,
) (*report.Report, error) {
	s.calls = append(s.calls, "ByReference")
	return nil, errors.New("not found")
}

// exportFixture is one claim in each of two districts, in Marathi, which is how
// the trial districts' records actually read.
func exportFixture() *exportStore {
	occurred := time.Date(2026, 9, 14, 16, 40, 0, 0, time.UTC)
	reported := time.Date(2026, 9, 16, 6, 15, 0, 0, time.UTC)
	return &exportStore{claims: []exportClaim{
		{exportDistrictA, report.Summary{
			Reference: "CLR-2026-496-00001", FarmerName: "सुनीता पाटील",
			VillageName: "वैराग", SubDistrictName: "बार्शी",
			CircleName: "वैराग", CalamityType: "गारपीट",
			LossCategory: "standing_crop", State: "submitted",
			OccurredAt: occurred, ReportedAt: reported,
			Lines: 2, AreaSownHa: 1.6, AreaAffectedHa: 1.12,
			DamagePercent: 70,
		}},
		{exportDistrictB, report.Summary{
			Reference: "CLR-2026-497-00044", FarmerName: "दुसऱ्या जिल्ह्यातील शेतकरी",
			VillageName: "अन्य गाव", SubDistrictName: "अन्य तालुका",
			CalamityType: "पूर", State: "approved",
			OccurredAt: occurred, ReportedAt: reported,
			Lines: 1, AreaSownHa: 0.8, AreaAffectedHa: 0.4,
			DamagePercent: 50,
		}},
	}}
}

func exportOfficer() rbac.Scope {
	return rbac.Scope{JurisdictionIDs: []string{exportDistrictA}}
}

// exportConverter stands in for the Chromium service, and keeps the page it was
// given so a test can read what was actually sent to be converted.
type exportConverter struct {
	html []byte
	ref  string
	err  error
}

func (c *exportConverter) Render(
	_ context.Context, html []byte, reference string,
) ([]byte, error) {
	c.html = html
	c.ref = reference
	if c.err != nil {
		return nil, c.err
	}
	return []byte("%PDF-1.7\n% a converted document\n"), nil
}

func exportFor(t *testing.T, f Format, req ExportRequest) *Export {
	t.Helper()
	req.Format = f
	out, err := BuildExport(context.Background(), exportFixture(),
		exportOfficer(), &exportConverter{}, req)
	if err != nil {
		t.Fatalf("%s export: %v", f, err)
	}
	return out
}

func TestEachFormatComesBackWithItsOwnContentTypeAndANonEmptyBody(t *testing.T) {
	// The whole point of the endpoint: an officer gets the format they asked for.
	// A body of zero bytes with the right content type is a download that opens
	// as a damaged file, which is worse than a refusal.
	for _, f := range ExportFormats {
		out := exportFor(t, f, ExportRequest{})
		if out.ContentType != ContentType(f) {
			t.Errorf("%s came back as %q", f, out.ContentType)
		}
		if len(out.Bytes) == 0 {
			t.Errorf("%s came back empty", f)
		}
		if !strings.HasSuffix(out.Filename, "."+string(f)) {
			t.Errorf("%s was named %q", f, out.Filename)
		}
		if out.Rows != 1 {
			t.Errorf("%s carried %d rows, want the one claim in scope",
				f, out.Rows)
		}
	}
}

func TestEachFormatComesBackAsTheKindOfFileItClaimsToBe(t *testing.T) {
	// A content type is an assertion. These four are checkable in the first
	// bytes, and a workbook served as a PDF is the sort of mistake that only
	// shows up on an officer's machine.
	for f, magic := range map[Format]string{
		FormatXLSX: "PK\x03\x04",
		FormatDOCX: "PK\x03\x04",
		FormatPPTX: "PK\x03\x04",
		FormatPDF:  "%PDF",
	} {
		out := exportFor(t, f, ExportRequest{})
		if !bytes.HasPrefix(out.Bytes, []byte(magic)) {
			t.Errorf("%s does not begin like one: %q", f, firstFewBytes(out.Bytes))
		}
	}
}

func TestACsvExportCarriesTheByteOrderMarkExcelNeeds(t *testing.T) {
	// Without it Excel on a Windows machine reads the file in the system
	// codepage, and every Devanagari name in it becomes mojibake. The export is
	// then useless to the officer who asked for it, and looks like our bug.
	out := exportFor(t, FormatCSV, ExportRequest{})

	if !bytes.HasPrefix(out.Bytes, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatalf("no byte-order mark: the file begins %q",
			firstFewBytes(out.Bytes))
	}
	if !strings.Contains(string(out.Bytes), "सुनीता पाटील") {
		t.Error("the farmer's name did not reach the CSV")
	}
	if !strings.Contains(out.ContentType, "charset=utf-8") {
		t.Errorf("content type %q does not state the charset", out.ContentType)
	}
}

func TestAnUnsupportedFormatIsRefusedAndTheMessageNamesWhatIsSupported(t *testing.T) {
	// An officer who typed "xls" or "sheet" needs to be told what to type
	// instead. A bare refusal leaves them guessing at a list only this file has.
	for _, asked := range []string{"xls", "sheet", "odt", "html", "", "exe"} {
		_, err := ParseFormat(asked)
		if err == nil {
			t.Fatalf("%q was accepted as a format", asked)
		}
		if !errors.Is(err, ErrUnknownFormat) {
			t.Fatalf("%q was refused as %v", asked, err)
		}
		for _, f := range ExportFormats {
			if !strings.Contains(err.Error(), string(f)) {
				t.Errorf("refusing %q did not mention %s: %v", asked, f, err)
			}
		}
	}
}

func TestEveryNamedFormatIsOneSomethingCanActuallyProduce(t *testing.T) {
	// A format listed in a refusal and not implemented would be advice that
	// cannot be followed.
	for _, f := range ExportFormats {
		if ContentType(f) == "" {
			t.Errorf("%s has no content type", f)
		}
		if _, err := ParseFormat(string(f)); err != nil {
			t.Errorf("%s is listed but not parseable: %v", f, err)
		}
	}
}

func TestAnExportPastTheRowCapIsRefusedRatherThanTruncated(t *testing.T) {
	// Decided deliberately. A statement that stopped at row one thousand without
	// saying so looks complete, gets signed, and the village whose claims fell
	// off the end is simply absent -- so the refusal names the number and says
	// what to narrow by instead.
	store := &exportStore{}
	for i := 0; i < MaxExportRows+1; i++ {
		store.claims = append(store.claims, exportClaim{
			exportDistrictA,
			report.Summary{
				Reference: fmt.Sprintf("CLR-2026-496-%05d", i+1),
				State:     "submitted",
			},
		})
	}

	_, err := BuildExport(context.Background(), store, exportOfficer(), nil,
		ExportRequest{Format: FormatCSV})

	var tooMany TooManyRowsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("a district's whole season came back as %v, not a refusal", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(MaxExportRows)) {
		t.Errorf("the refusal does not name the limit: %v", err)
	}
	if !strings.Contains(err.Error(), "narrow") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

func TestAnExportAtExactlyTheRowCapIsStillProduced(t *testing.T) {
	// The cap is a limit, not a margin. Refusing the thousandth claim would make
	// the number in the message a lie.
	store := &exportStore{}
	for i := 0; i < MaxExportRows; i++ {
		store.claims = append(store.claims, exportClaim{
			exportDistrictA,
			report.Summary{
				Reference: fmt.Sprintf("CLR-2026-496-%05d", i+1),
				State:     "submitted",
			},
		})
	}

	out, err := BuildExport(context.Background(), store, exportOfficer(), nil,
		ExportRequest{Format: FormatCSV, Fields: []string{"reference"}})
	if err != nil {
		t.Fatalf("an export of exactly the cap was refused: %v", err)
	}
	if out.Rows != MaxExportRows {
		t.Fatalf("rows %d, want %d", out.Rows, MaxExportRows)
	}
}

func TestAColumnOutsideTheVocabularyIsRefusedByName(t *testing.T) {
	// The vocabulary is closed, and this is what makes it closed in practice.
	// Skipping an unknown column would put a column of blanks in the middle of a
	// statement somebody signs.
	for _, asked := range []string{"farmer_id", "aadhaar", "mobile", "bank_account"} {
		_, err := BuildExport(context.Background(), exportFixture(),
			exportOfficer(), nil,
			ExportRequest{Format: FormatCSV, Fields: []string{"reference", asked}})

		var unknown UnknownFieldError
		if !errors.As(err, &unknown) {
			t.Fatalf("%q was accepted as a column: %v", asked, err)
		}
		if !strings.Contains(err.Error(), asked) {
			t.Errorf("the refusal does not name %q: %v", asked, err)
		}
	}
}

func TestTheExportVocabularyStillExcludesTheIdentifiers(t *testing.T) {
	// Read against the vocabulary rather than the prose. farmer_id and Aadhaar are
	// absent from fields.go by design, every format here renders Fields and
	// nothing else, and widening it is the one change that would put them in a
	// document an officer mails on.
	for _, f := range Fields {
		switch f.Name {
		case "farmer_id", "aadhaar", "aadhaar_number", "mobile", "farmer_mobile",
			"bank_account", "ifsc":
			t.Fatalf("%q is in the agent's vocabulary; an export would carry it",
				f.Name)
		}
	}
	for _, name := range DefaultExportFields {
		if !FieldNames()[name] {
			t.Fatalf("the default export asks for %q, which is not a field", name)
		}
	}
}

func TestAnExportRequestHasNoWayToCarryAClaimOrAScope(t *testing.T) {
	// The structural half of the scope guarantee, checked against the type rather
	// than against behaviour: there is no parameter through which a claim outside
	// the caller's posting could arrive, and no parameter that could substitute a
	// scope. A field added here that carried either would fail this before it
	// reached review.
	scopeType := reflect.TypeOf(rbac.Scope{})
	summaryType := reflect.TypeOf(report.Summary{})
	filterType := reflect.TypeOf(report.Filter{})

	rt := reflect.TypeOf(ExportRequest{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		switch f.Type {
		case scopeType, summaryType, filterType:
			t.Fatalf("ExportRequest.%s carries a %s; the rows must come from "+
				"RowsFor and the scope from the caller", f.Name, f.Type)
		}
		switch f.Name {
		case "Scope", "Rows", "Claims", "FarmerID", "ReportID", "ReportIDs":
			t.Fatalf("ExportRequest.%s is a way round the scoped read", f.Name)
		}
	}
}

func TestAnExportContainsNoClaimOutsideTheCallersScope(t *testing.T) {
	// The behavioural half. The fake store applies the same rule the real one
	// does -- a claim is visible when its posting is one the caller holds -- so
	// this needs no database and does not skip. The predicate itself is tested
	// against a real database in the report store's own scope tests.
	store := exportFixture()
	out, err := BuildExport(context.Background(), store, exportOfficer(),
		&exportConverter{}, ExportRequest{Format: FormatCSV})
	if err != nil {
		t.Fatal(err)
	}

	body := string(out.Bytes)
	if !strings.Contains(body, "CLR-2026-496-00001") {
		t.Error("the officer's own claim is missing from their export")
	}
	if strings.Contains(body, "CLR-2026-497-00044") ||
		strings.Contains(body, "दुसऱ्या जिल्ह्यातील शेतकरी") {
		t.Fatal("a claim from another district reached the export")
	}

	if len(store.sawScopes) != 1 {
		t.Fatalf("the store was listed %d times", len(store.sawScopes))
	}
	got := store.sawScopes[0]
	if len(got.JurisdictionIDs) != 1 || got.JurisdictionIDs[0] != exportDistrictA {
		t.Fatalf("the store saw scope %+v, want the caller's own", got)
	}
}

func TestAnExportOnlyEverReadsAndOnlyThroughTheScopedList(t *testing.T) {
	// An export writes a file and nothing else. There is no store call here but
	// the read, so a write added later shows up as an extra call in this list.
	store := exportFixture()
	for _, f := range ExportFormats {
		store.calls = nil
		if _, err := BuildExport(context.Background(), store, exportOfficer(),
			&exportConverter{}, ExportRequest{Format: f}); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(store.calls) != 1 || store.calls[0] != "List" {
			t.Fatalf("%s called %v, want one scoped List", f, store.calls)
		}
	}
}

func TestTheFiltersAreTheOnesTheClaimListAlreadyTakes(t *testing.T) {
	// So that narrowing an export means the same thing as narrowing the console's
	// own queue, rather than a second filter language to keep in step.
	store := exportFixture()
	if _, err := BuildExport(context.Background(), store, exportOfficer(), nil,
		ExportRequest{
			Format: FormatCSV, State: "submitted",
			EventID: "ev-1", AwaitingLevel: "tehsildar", Search: "वैराग",
		}); err != nil {
		t.Fatal(err)
	}
	f := store.sawFilter
	if f.State != "submitted" || f.EventID != "ev-1" ||
		f.AwaitingLevel != "tehsildar" || f.Page.Query != "वैराग" {
		t.Fatalf("the store saw filter %+v", f)
	}
}

func TestEveryFieldInTheVocabularyHasABilingualHeading(t *testing.T) {
	// A field added to fields.go without a heading here would appear as a column
	// headed by its own identifier -- "area_affected_ha" above a column of
	// hectares on a form an officer signs.
	for _, f := range Fields {
		h, ok := exportHeadings[f.Name]
		if !ok {
			t.Errorf("%q has no heading", f.Name)
			continue
		}
		if strings.TrimSpace(h[0]) == "" || strings.TrimSpace(h[1]) == "" {
			t.Errorf("%q is headed %q, which is missing a language", f.Name, h)
		}
		if !strings.ContainsFunc(h[0], isDevanagariRune) {
			t.Errorf("%q has no Marathi heading: %q", f.Name, h[0])
		}
	}
}

func TestMarathiSurvivesIntoEveryFormat(t *testing.T) {
	// The failure this is here for is a document of empty boxes. The bytes are
	// the easy half -- they are UTF-8 and go in correctly whatever one does --
	// so each format is read back out and looked at.
	const name = "सुनीता पाटील"
	const village = "वैराग"

	csvOut := exportFor(t, FormatCSV, ExportRequest{})
	if !strings.Contains(string(csvOut.Bytes), name) {
		t.Error("the CSV lost the farmer's name")
	}

	book := exportFor(t, FormatXLSX, ExportRequest{})
	f, err := excelize.OpenReader(bytes.NewReader(book.Bytes))
	if err != nil {
		t.Fatalf("the workbook will not open: %v", err)
	}
	defer f.Close()
	got, err := f.GetCellValue("Claims", "C2")
	if err != nil {
		t.Fatal(err)
	}
	if got != name {
		t.Errorf("the workbook holds %q in C2, want %q", got, name)
	}

	word := exportZipEntry(t, exportFor(t, FormatDOCX, ExportRequest{}).Bytes,
		"word/document.xml")
	if !strings.Contains(word, name) || !strings.Contains(word, village) {
		t.Error("the Word document lost the Marathi text")
	}

	deck := exportZipEntry(t, exportFor(t, FormatPPTX, ExportRequest{}).Bytes,
		"ppt/slides/slide2.xml")
	if !strings.Contains(deck, name) {
		t.Error("the deck lost the Marathi text")
	}
}

func TestTheWordDocumentDeclaresTheComplexScriptFontAndNotOnlyTheLatinOne(t *testing.T) {
	// w:cs is the load-bearing attribute. A run that names only w:ascii leaves
	// Devanagari to the complex-script default, and on a machine whose default
	// has no Devanagari that is a row of empty boxes rather than a name.
	raw := exportFor(t, FormatDOCX, ExportRequest{}).Bytes

	for _, part := range []string{"word/document.xml", "word/styles.xml"} {
		body := exportZipEntry(t, raw, part)
		for _, want := range []string{
			`w:cs="Nirmala UI"`, `w:ascii="Nirmala UI"`,
			`w:bidi="mr-IN"`, `<w:szCs `,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not declare %s", part, want)
			}
		}
	}

	// And a bold heading must be bold in both scripts, or the Marathi half of
	// every heading reads as body text.
	body := exportZipEntry(t, raw, "word/document.xml")
	if !strings.Contains(body, `<w:b/><w:bCs/>`) {
		t.Error("the document's headings are bold in Latin only")
	}
}

func TestTheWordDocumentIsARealTableAndNotAPageOfTextBoxes(t *testing.T) {
	// The reason the format was asked for. An officer adds a column, deletes a
	// village and circulates it; a grid of text boxes looks right and cannot be
	// edited, which would be the wrong answer dressed as the right one.
	body := exportZipEntry(t, exportFor(t, FormatDOCX, ExportRequest{}).Bytes,
		"word/document.xml")

	for _, want := range []string{"<w:tbl>", "<w:tblGrid>", "<w:tr>", "<w:tc>",
		"<w:tblHeader/>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the document has no %s", want)
		}
	}
	if strings.Contains(body, "<w:txbxContent>") {
		t.Error("the document is built out of text boxes")
	}
	if n := strings.Count(body, "<w:tr>"); n != 2 {
		t.Errorf("%d rows, want a heading row and the one claim", n)
	}
}

func TestTheDeckDeclaresTheComplexScriptFontTooAndCarriesOneTablePerSlide(t *testing.T) {
	// PowerPoint's equivalent of w:cs is a:cs on every run, plus a Deva entry in
	// the theme's font scheme. The same failure and the same remedy.
	raw := exportFor(t, FormatPPTX, ExportRequest{}).Bytes

	theme := exportZipEntry(t, raw, "ppt/theme/theme1.xml")
	for _, want := range []string{`script="Deva"`, `<a:cs typeface="Nirmala UI"/>`} {
		if !strings.Contains(theme, want) {
			t.Errorf("the theme does not declare %s", want)
		}
	}

	slide := exportZipEntry(t, raw, "ppt/slides/slide2.xml")
	if !strings.Contains(slide, `<a:cs typeface="Nirmala UI"/>`) {
		t.Error("a slide's runs name no complex-script font")
	}
	if !strings.Contains(slide, "<a:tbl>") {
		t.Error("the data slide carries no table")
	}

	// A title slide and one table slide for the single claim.
	names := exportZipNames(t, raw)
	slides := 0
	for _, n := range names {
		if strings.HasPrefix(n, "ppt/slides/slide") &&
			strings.HasSuffix(n, ".xml") {
			slides++
		}
	}
	if slides != 2 {
		t.Errorf("%d slides, want a title slide and one table slide", slides)
	}
	// Every part a reader looks for first.
	for _, want := range []string{
		"[Content_Types].xml", "_rels/.rels", "ppt/presentation.xml",
		"ppt/_rels/presentation.xml.rels",
		"ppt/slideMasters/slideMaster1.xml", "ppt/slideLayouts/slideLayout1.xml",
		"ppt/slides/_rels/slide1.xml.rels", "ppt/tableStyles.xml",
	} {
		if !hasPart(names, want) {
			t.Errorf("the deck has no %s", want)
		}
	}
}

func TestEveryPartOfAnOfficeFormatIsWellFormedXml(t *testing.T) {
	// The cheapest guard there is against hand-written OOXML. Word and
	// PowerPoint do not repair a malformed part -- they refuse the whole file,
	// with a message that says nothing about which part -- so every one of them
	// is parsed here, with the Marathi and the awkward title in place.
	for _, f := range []Format{FormatDOCX, FormatPPTX, FormatXLSX} {
		out := exportFor(t, f, ExportRequest{
			Title: `Barshi <w:p/> & "quoted"`,
			Note:  "A line the assistant drafted.",
		})
		for _, name := range exportZipNames(t, out.Bytes) {
			if !strings.HasSuffix(name, ".xml") &&
				!strings.HasSuffix(name, ".rels") {
				continue
			}
			body := exportZipEntry(t, out.Bytes, name)
			dec := xml.NewDecoder(strings.NewReader(body))
			for {
				_, err := dec.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s: %s is not well-formed XML: %v", f, name, err)
				}
			}
		}
	}
}

func TestTheDeckShowsOnlyAsManyColumnsAsCanBeReadFromTheBackOfARoom(t *testing.T) {
	// A deck is a summary for a review meeting; the statement is the spreadsheet
	// or the PDF. Fourteen columns on a slide is a slide nobody reads, so the
	// first few are shown and the title slide says that is what happened.
	all := make([]string, 0, len(Fields))
	for _, f := range Fields {
		all = append(all, f.Name)
	}

	out := exportFor(t, FormatPPTX, ExportRequest{Fields: all})
	table := exportZipEntry(t, out.Bytes, "ppt/slides/slide2.xml")
	if n := strings.Count(table, "<a:gridCol"); n != maxDeckColumns {
		t.Errorf("%d columns on the slide, want %d", n, maxDeckColumns)
	}

	title := exportZipEntry(t, out.Bytes, "ppt/slides/slide1.xml")
	if !strings.Contains(title, "showing the first") {
		t.Error("the deck trimmed its columns without saying so")
	}
}

func TestAPdfExportIsRenderedAsPrintStyledHtmlAndSentToTheConverter(t *testing.T) {
	// The same path the assessment report takes, and for the same reason: the
	// converter has a real text-shaping engine and a Devanagari font, and no Go
	// PDF library has either.
	conv := &exportConverter{}
	out, err := BuildExport(context.Background(), exportFixture(),
		exportOfficer(), conv,
		ExportRequest{Format: FormatPDF, Title: "बार्शी तालुका",
			Note: "Drafted for the weekly review."})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes, []byte("%PDF")) {
		t.Fatalf("the converter's answer was not passed through: %q",
			firstFewBytes(out.Bytes))
	}

	html := string(conv.html)
	if !strings.Contains(html, "<!doctype html>") {
		t.Error("the converter was not given a whole document")
	}
	if !strings.Contains(html, `"Noto Sans Devanagari"`) {
		t.Error("the page does not name the converter's Devanagari font")
	}
	if !strings.Contains(html, "display: table-header-group") {
		t.Error("the headings will not repeat across pages")
	}
	if !strings.Contains(html, "सुनीता पाटील") {
		t.Error("the claim did not reach the page")
	}
	if !strings.Contains(html, "Drafted for the weekly review.") {
		t.Error("the drafted note did not reach the page")
	}
	if conv.ref != "बार्शी तालुका" {
		t.Errorf("the converter was given %q as the running header", conv.ref)
	}
}

func TestATitleThatClosesATagCannotReachTheRenderedPage(t *testing.T) {
	// The title is the caller's and it is interpolated into HTML, into XML and
	// into a filename. Escaped in all three; this is the one a template gets for
	// us, and it is checked rather than assumed.
	conv := &exportConverter{}
	if _, err := BuildExport(context.Background(), exportFixture(),
		exportOfficer(), conv, ExportRequest{
			Format: FormatPDF,
			Title:  `</style><script>fetch("//x")</script>`,
		}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conv.html), "<script>") {
		t.Fatal("a script tag from the title reached the page")
	}
}

func TestATitleThatClosesATagCannotReachTheOfficeFormats(t *testing.T) {
	// The same question for the two formats written as XML by hand, where there
	// is no template doing it. An unescaped angle bracket there does not inject
	// anything -- it produces a document no reader will open, which is its own
	// kind of failure.
	for _, f := range []Format{FormatDOCX, FormatPPTX} {
		out := exportFor(t, f, ExportRequest{
			Title: `Barshi <w:p/> & "quoted" <a:t/>`,
		})
		part := "word/document.xml"
		if f == FormatPPTX {
			part = "ppt/slides/slide1.xml"
		}
		body := exportZipEntry(t, out.Bytes, part)
		for _, want := range []string{"&lt;w:p/&gt;", "&amp;", "&quot;quoted&quot;"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the title was not escaped to %s", f, want)
			}
		}
		if strings.Contains(body, `& "quoted"`) {
			t.Errorf("%s: a raw ampersand and quote reached the XML", f)
		}
	}
}

func TestAControlCharacterInAClaimDoesNotProduceAnUnopenableDocument(t *testing.T) {
	// Names arrive from registries older than this platform, and one of them
	// occasionally carries a control character. XML 1.0 cannot represent most of
	// them even as a numeric reference, so a document that kept one would not
	// open at all.
	store := exportFixture()
	store.claims[0].summary.FarmerName = "सुनीता\x01 पाटील"

	for _, f := range []Format{FormatDOCX, FormatPPTX} {
		out, err := BuildExport(context.Background(), store, exportOfficer(),
			nil, ExportRequest{Format: f})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		part := "word/document.xml"
		if f == FormatPPTX {
			part = "ppt/slides/slide2.xml"
		}
		if strings.ContainsRune(exportZipEntry(t, out.Bytes, part), 0x01) {
			t.Errorf("%s kept a control character in its XML", f)
		}
	}
}

func TestTheWorkbookWritesFiguresAsFiguresAndNotAsTextThatLooksLikeThem(t *testing.T) {
	// The reason a spreadsheet is worth producing at all. An officer who totals
	// the affected-area column has to get a total.
	out := exportFor(t, FormatXLSX, ExportRequest{
		Fields: []string{"reference", "area_affected_ha"},
	})
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatalf("the workbook will not open: %v", err)
	}
	defer f.Close()

	// Asked the way an officer asks it: put a total under the column and see
	// whether a total comes back. A figure stored as text sums to zero, which is
	// the failure this is here to catch, and it is invisible on screen.
	if err := f.SetCellFormula("Claims", "B4", "SUM(B2:B3)"); err != nil {
		t.Fatal(err)
	}
	total, err := f.CalcCellValue("Claims", "B4")
	if err != nil {
		t.Fatal(err)
	}
	if total != "1.12" {
		t.Errorf("the affected-area column totals to %q, want 1.12 -- the "+
			"figures are stored as text", total)
	}
	if got, _ := f.GetCellValue("Claims", "B2"); got != "1.12" {
		t.Errorf("affected area reads %q", got)
	}
	// And the headings are the bilingual ones, on the one sheet.
	if sheets := f.GetSheetList(); len(sheets) != 1 || sheets[0] != "Claims" {
		t.Errorf("sheets %v", sheets)
	}
	if got, _ := f.GetCellValue("Claims", "B1"); got != Heading("area_affected_ha") {
		t.Errorf("B1 reads %q", got)
	}
}

func TestAnExportWithNoColumnsAtAllIsRefused(t *testing.T) {
	_, err := BuildExport(context.Background(), exportFixture(), exportOfficer(),
		nil, ExportRequest{Format: FormatCSV, Fields: []string{" ", ""}})
	if !errors.Is(err, ErrNoFields) {
		t.Fatalf("an export of no columns came back as %v", err)
	}
}

func TestAColumnAskedForTwiceIsOneColumn(t *testing.T) {
	// Printed twice it would read as two figures that happen to agree.
	out := exportFor(t, FormatCSV, ExportRequest{
		Fields: []string{"reference", "village", "reference"},
	})
	if len(out.Fields) != 2 {
		t.Fatalf("columns %v", out.Fields)
	}
}

func TestAPdfIsRefusedWhenNoConverterIsConfiguredAndTheOthersAreNot(t *testing.T) {
	// A workbook named .pdf would be a worse answer than a refusal, and unlike
	// the printed report there is no HTML the officer was going to print anyway.
	// Every other format is produced in this process and must be unaffected.
	_, err := BuildExport(context.Background(), exportFixture(), exportOfficer(),
		nil, ExportRequest{Format: FormatPDF})
	if !errors.Is(err, ErrPDFUnavailable) {
		t.Fatalf("a PDF with no converter came back as %v", err)
	}

	for _, f := range []Format{FormatXLSX, FormatCSV, FormatDOCX, FormatPPTX} {
		if _, err := BuildExport(context.Background(), exportFixture(),
			exportOfficer(), nil, ExportRequest{Format: f}); err != nil {
			t.Errorf("%s needed a PDF converter: %v", f, err)
		}
	}
}

func TestAConverterThatRefusesTheDocumentReadsAsUnavailableAndNotAsAFault(t *testing.T) {
	conv := &exportConverter{err: errors.New("dial tcp 10.0.0.4:3000: refused")}
	_, err := BuildExport(context.Background(), exportFixture(), exportOfficer(),
		conv, ExportRequest{Format: FormatPDF})
	if !errors.Is(err, ErrPDFUnavailable) {
		t.Fatalf("a refused conversion came back as %v", err)
	}
}

func TestAnOfficerWithNoPostingExportsNothingRatherThanEverything(t *testing.T) {
	// An empty scope reaching the store lists nothing, which is correct. Checked
	// because the opposite -- an empty jurisdiction list read as "everywhere" --
	// is the exact defect the route scope table was written after.
	out, err := BuildExport(context.Background(), exportFixture(), rbac.Scope{},
		&exportConverter{}, ExportRequest{Format: FormatCSV})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows != 0 {
		t.Fatalf("an officer with no posting exported %d rows", out.Rows)
	}
	if strings.Contains(string(out.Bytes), "CLR-") {
		t.Fatal("a claim reached an export made with no posting")
	}
}

func TestAnExportWithNoMatchingClaimsSaysSoRatherThanComingBackBlank(t *testing.T) {
	// A document with a header row and nothing under it reads as a failure. The
	// formats with room for a sentence say which it is.
	store := exportFixture()
	store.claims = nil

	conv := &exportConverter{}
	if _, err := BuildExport(context.Background(), store, exportOfficer(), conv,
		ExportRequest{Format: FormatPDF}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conv.html), "no claims matched") {
		t.Error("an empty PDF does not say it is empty")
	}

	word := exportZipEntry(t, exportForStore(t, store, FormatDOCX).Bytes,
		"word/document.xml")
	if !strings.Contains(word, "no claims matched") {
		t.Error("an empty Word document does not say it is empty")
	}
}

func TestAFilenameIsDescriptiveAndDated(t *testing.T) {
	// An officer downloads several of these in a morning, and a folder of
	// "export(3).xlsx" is a folder of files nobody can tell apart.
	out := exportFor(t, FormatXLSX, ExportRequest{Title: "Barshi pending claims"})
	if !strings.Contains(out.Filename, "Barshi pending claims") {
		t.Errorf("filename %q", out.Filename)
	}
	if !strings.Contains(out.Filename, time.Now().In(istZone).Format("2006-01-02")) {
		t.Errorf("filename %q carries no date", out.Filename)
	}
}

// --- helpers ------------------------------------------------------------

func exportForStore(t *testing.T, store *exportStore, f Format) *Export {
	t.Helper()
	out, err := BuildExport(context.Background(), store, exportOfficer(),
		&exportConverter{}, ExportRequest{Format: f})
	if err != nil {
		t.Fatalf("%s export: %v", f, err)
	}
	return out
}

func exportZipEntry(t *testing.T, raw []byte, name string) string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the package will not open: %v", err)
	}
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("%s is not in the package; it holds %v", name, exportZipNames(t, raw))
	return ""
}

func exportZipNames(t *testing.T, raw []byte) []string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the package will not open: %v", err)
	}
	out := make([]string, 0, len(r.File))
	for _, f := range r.File {
		out = append(out, f.Name)
	}
	return out
}

func hasPart(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func firstFewBytes(b []byte) string {
	if len(b) > 24 {
		b = b[:24]
	}
	return string(b)
}

func isDevanagariRune(r rune) bool { return r >= 0x0900 && r <= 0x097f }
