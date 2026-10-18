package officeragent

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Handing an officer their own figures back in the format they asked for.
//
// # Why this is not the fill endpoint with more encoders bolted on
//
// Filling a format starts from a document the office designed: the model reads its
// headings, this code writes the values, and the officer's own layout survives
// because that layout is the reason they sent the file. Nothing about that works
// for a format nobody supplied.
//
// So an export starts from the other end. The officer -- or the assistant drafting
// on their behalf -- names the columns they want out of the one closed vocabulary
// in fields.go, and this produces a document in it. The two paths meet at RowsFor,
// which is the only way rows enter either of them.
//
// # Why the caller cannot hand us rows
//
// Because that is what makes the scope hold. ExportRequest has no field for a
// claim, a row, a report id or an rbac.Scope: it carries a format, a choice of
// columns out of a fixed list, and the same three filters the listing takes. The
// rows are fetched here, by RowsFor, from the scope passed as an argument -- the
// caller's own, the same store call the console's list uses. An export therefore
// cannot contain a claim the officer could not already open, and it cannot because
// there is no parameter through which one could arrive, rather than because
// something downstream checks.
//
// farmer_id and Aadhaar follow from the same place. They are absent from Fields,
// every format here renders Fields and nothing else, and widening the vocabulary
// is the one change that would put them in a document.
//
// # Why nothing here writes
//
// An export reads claims and produces bytes. There is no store call but the read,
// no state recorded against a claim, and nothing for the officer to undo: a
// document they discard is a document that never existed. The agent's limits --
// read and draft, never approve -- are unchanged by giving it a file format.

// Format is one kind of document this can produce.
type Format string

// The formats, as an officer names them.
const (
	FormatXLSX Format = "xlsx"
	FormatCSV  Format = "csv"
	FormatPDF  Format = "pdf"
	FormatDOCX Format = "docx"
	FormatPPTX Format = "pptx"
)

// ExportFormats is every format, in the order a refusal lists them.
var ExportFormats = []Format{
	FormatXLSX, FormatCSV, FormatPDF, FormatDOCX, FormatPPTX,
}

// MaxExportRows bounds one document, and is refused rather than applied.
//
// # Why there is no caller-supplied limit
//
// Because a limit and a cap together can only mean silent truncation, and a
// statement of a district's claims that stops at row one thousand without saying so
// is the worst thing this could hand an officer. It looks complete. It gets signed.
// The village whose claims fell off the end is simply absent, and nobody reading
// the document can tell.
//
// So the filters are how an export is narrowed -- by event, by state, by the rung
// it is waiting at -- and asking for more than this many claims is answered with a
// refusal that names the number and says what to narrow by. One thousand rows is
// already about thirty A4 pages and a Chromium conversion well inside the
// converter's hundred-and-twenty-second timeout; past it the officer wants an
// extract from the database, not a document.
const MaxExportRows = 1000

// MaxExportBytes is the backstop, not the bound.
//
// The row cap is what keeps a document reasonable. This catches the case the row
// cap cannot see -- a thousand claims whose text fields are all unusually long --
// before a document no viewer would open is written to the wire.
const MaxExportBytes = 32 << 20

// ErrPDFUnavailable means the converter is not configured or would not convert.
//
// Its own answer because the officer can act on it: every other format is produced
// in this process and still works. Unlike the printed report, which degrades to the
// HTML it was always going to be printed from, an export has nothing honest to
// degrade to -- a workbook named .pdf is a worse answer than a refusal.
var ErrPDFUnavailable = errors.New("the PDF converter is not available")

// ErrUnknownFormat means the caller asked for something this does not produce.
var ErrUnknownFormat = errors.New("that format is not one this produces")

// ErrNoFields means every column the caller chose was dropped.
var ErrNoFields = errors.New("no columns were chosen")

// TooManyRowsError is the refusal an unbounded request gets.
type TooManyRowsError struct{ Limit int }

func (e TooManyRowsError) Error() string {
	return fmt.Sprintf("more than %d claims match, which is more than one "+
		"document should carry; narrow it by event, by state, or by the "+
		"approval level it is waiting at", e.Limit)
}

// TooLargeError is the refusal a document that overran the backstop gets.
type TooLargeError struct {
	Bytes int
	Limit int
}

func (e TooLargeError) Error() string {
	return fmt.Sprintf("that export came to %d bytes, past the %d-byte limit; "+
		"ask for fewer columns or narrow the claims", e.Bytes, e.Limit)
}

// UnknownFieldError names a column the vocabulary does not have.
//
// Refused rather than skipped. A skipped column is a column of blanks in the middle
// of a signed statement, and the caller -- a console, or the assistant drafting for
// it -- asked for something it can be told about.
type UnknownFieldError struct{ Name string }

func (e UnknownFieldError) Error() string {
	return fmt.Sprintf("there is no column called %q", e.Name)
}

func (e UnknownFieldError) Unwrap() error { return ErrNoFields }

// PDFConverter turns a print-styled page into a PDF.
//
// The interface is the three-line shape of internal/pdf.Renderer, so this package
// uses the converter the printed report already goes through -- Chromium behind
// HTTP, with Noto Devanagari installed -- rather than acquiring a second opinion
// about how to draw a matra. A nil converter is an unconfigured deployment.
type PDFConverter interface {
	Render(ctx context.Context, html []byte, reference string) ([]byte, error)
}

// ExportRequest is what the officer, or the assistant drafting for them, asked for.
//
// Note what is not here: no rows, no claim, no report id, no scope. See the package
// comment above -- that absence is the scope guarantee.
type ExportRequest struct {
	Format Format

	// Fields names the columns, in the order they are wanted, out of Fields.
	// Empty means DefaultExportFields.
	Fields []string

	// Title heads the document and names the file. Empty takes a default.
	Title string

	// Note is the line of prose the assistant drafted -- what this export is,
	// who asked for it, what it leaves out. It appears above the table in the
	// PDF, the Word document and the deck, and nowhere in the spreadsheet or
	// the CSV: a line of prose above a header row is a line that breaks every
	// tool that reads those two formats.
	Note string

	// The same three narrowings the listing and the fill endpoint take, and the
	// only way to make an export smaller.
	EventID       string
	State         string
	AwaitingLevel string

	// Search is free text, as the console's own search box takes it.
	Search string
}

// Export is the finished document.
type Export struct {
	Bytes       []byte
	ContentType string

	// Filename is descriptive and already stripped of anything structural. It
	// is not yet safe for a header -- see the api package, which is where a
	// Content-Disposition is written and therefore where it is quoted.
	Filename string

	Rows   int
	Fields []string
}

// DefaultExportFields is what an officer gets when they do not say.
//
// The columns a taluka statement actually carries: who, where, what happened, how
// much of it, and where the claim has reached. Not every field in the vocabulary --
// a sixteen-column document is one nobody reads.
var DefaultExportFields = []string{
	"serial", "reference", "farmer_name", "village", "sub_district",
	"calamity", "occurred_on", "area_sown_ha", "area_affected_ha",
	"damage_percent", "state",
}

// exportHeadings are the column headings, Marathi first.
//
// The same decision as the printed report's labels: the statement is read and
// signed in Marathi, and aggregated by district and state offices that read
// English. One heading carrying both means an officer and the office above them
// are reading the same column, which matters when a figure is queried a season
// later.
//
// Keyed by field name rather than carried on Field, so that widening the
// vocabulary is still one edit in fields.go -- and a field added there without a
// heading here fails its test rather than appearing as a blank column.
var exportHeadings = map[string][2]string{
	"serial":           {"अ.क्र.", "Sr."},
	"reference":        {"नोंद क्रमांक", "Claim no."},
	"farmer_name":      {"शेतकऱ्याचे नाव", "Farmer"},
	"village":          {"गाव", "Village"},
	"circle":           {"मंडळ", "Circle"},
	"sub_district":     {"तालुका", "Taluka"},
	"calamity":         {"आपत्ती", "Calamity"},
	"loss_category":    {"नुकसानाचा प्रकार", "Loss category"},
	"occurred_on":      {"नुकसानाची तारीख", "Date of loss"},
	"reported_on":      {"दावा दाखल तारीख", "Date filed"},
	"filed_late":       {"विलंबाने दाखल", "Filed late"},
	"state":            {"स्थिती", "Status"},
	"area_sown_ha":     {"पेरणी क्षेत्र (हे.)", "Area sown (ha)"},
	"area_affected_ha": {"बाधित क्षेत्र (हे.)", "Area affected (ha)"},
	"damage_percent":   {"नुकसान %", "Loss %"},
	"lines":            {"पीक नोंदी", "Crop lines"},
}

// Heading is the bilingual heading for one field.
func Heading(field string) string {
	h, ok := exportHeadings[field]
	if !ok {
		// A field with no heading is a defect caught by a test, not a reason to
		// refuse an officer their export at runtime.
		return field
	}
	return h[0] + " / " + h[1]
}

// ParseFormat reads a format the caller named.
//
// Matched by name against the list and refused otherwise, with the supported names
// in the message: an officer who typed "xls" or "sheet" needs to be told what to
// type instead, and a console wiring this up needs the list from the server rather
// than from a comment.
func ParseFormat(raw string) (Format, error) {
	want := Format(strings.ToLower(strings.TrimSpace(raw)))
	for _, f := range ExportFormats {
		if f == want {
			return f, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrUnknownFormat, SupportedFormatList())
}

// SupportedFormatList names the formats, for a refusal.
func SupportedFormatList() string {
	names := make([]string, 0, len(ExportFormats))
	for _, f := range ExportFormats {
		names = append(names, string(f))
	}
	return "supported formats are " + strings.Join(names, ", ")
}

// contentTypes are what each format is served as.
var contentTypes = map[Format]string{
	FormatXLSX: "application/vnd.openxmlformats-officedocument." +
		"spreadsheetml.sheet",
	// The charset is stated because this is the one format whose bytes a
	// spreadsheet will guess at. See the byte-order mark below.
	FormatCSV: "text/csv; charset=utf-8",
	FormatPDF: "application/pdf",
	FormatDOCX: "application/vnd.openxmlformats-officedocument." +
		"wordprocessingml.document",
	FormatPPTX: "application/vnd.openxmlformats-officedocument." +
		"presentationml.presentation",
}

// ContentType is what a format is served as.
func ContentType(f Format) string { return contentTypes[f] }

// BuildExport reads the officer's claims and renders them.
//
// [scope] is the caller's own, and is the only thing that decides which claims are
// in the document: it goes to RowsFor, which puts it on the filter it passes to the
// store. [conv] may be nil, which makes a PDF unavailable and every other format
// unaffected.
func BuildExport(
	ctx context.Context,
	reports Reports,
	scope rbac.Scope,
	conv PDFConverter,
	req ExportRequest,
) (*Export, error) {
	chosen, err := resolveFields(req.Fields)
	if err != nil {
		return nil, err
	}

	if req.Format == FormatPDF && conv == nil {
		// Found out before the claims are read: a refusal the officer can act on
		// immediately beats one that arrives after a thousand rows were fetched.
		return nil, ErrPDFUnavailable
	}

	// One past the cap, so that "more than the cap matched" is a fact rather than
	// a guess. RowsFor is where the scope is applied, and the only way rows reach
	// any of the writers below.
	rows, err := RowsFor(ctx, reports, scope, report.Filter{
		EventID:       req.EventID,
		State:         req.State,
		AwaitingLevel: req.AwaitingLevel,
		Page:          page.Params{Query: strings.TrimSpace(req.Search)},
	}, MaxExportRows+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxExportRows {
		return nil, TooManyRowsError{Limit: MaxExportRows}
	}

	doc := document{
		Title:    exportTitle(req.Title),
		Note:     strings.TrimSpace(req.Note),
		Fields:   chosen,
		Rows:     rows,
		Prepared: time.Now().In(istZone),
	}

	var raw []byte
	switch req.Format {
	case FormatXLSX:
		raw, err = doc.xlsx()
	case FormatCSV:
		raw, err = doc.csv()
	case FormatPDF:
		raw, err = doc.pdf(ctx, conv)
	case FormatDOCX:
		raw, err = doc.docx()
	case FormatPPTX:
		raw, err = doc.pptx()
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownFormat, SupportedFormatList())
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxExportBytes {
		return nil, TooLargeError{Bytes: len(raw), Limit: MaxExportBytes}
	}

	names := make([]string, 0, len(chosen))
	for _, f := range chosen {
		names = append(names, f.Name)
	}

	return &Export{
		Bytes:       raw,
		ContentType: contentTypes[req.Format],
		Filename:    exportFilename(doc.Title, doc.Prepared, req.Format),
		Rows:        len(rows),
		Fields:      names,
	}, nil
}

// document is one export's content, before it is any particular format.
type document struct {
	Title    string
	Note     string
	Fields   []Field
	Rows     []map[string]any
	Prepared time.Time
}

// headings is the header row, bilingual.
func (d document) headings() []string {
	out := make([]string, 0, len(d.Fields))
	for _, f := range d.Fields {
		out = append(out, Heading(f.Name))
	}
	return out
}

// text is one row as strings, for the formats that hold only text.
func (d document) text(row map[string]any) []string {
	out := make([]string, 0, len(d.Fields))
	for _, f := range d.Fields {
		out = append(out, cellText(row[f.Name]))
	}
	return out
}

// csv writes the rows as comma-separated UTF-8, led by a byte-order mark.
//
// # Why the mark is there
//
// Because without it Excel on a Marathi officer's Windows machine does not read the
// file as UTF-8. It reads it in the system codepage, and every Devanagari name,
// village and calamity in the export becomes mojibake -- which does not merely look
// wrong, it makes the file useless to the person who asked for it, and it looks
// like our bug rather than like a missing three bytes.
//
// The mark costs nothing anywhere else: every other reader of a CSV either expects
// it or skips it.
func (d document) csv() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(byteOrderMark)

	w := csv.NewWriter(&buf)
	if err := w.Write(d.headings()); err != nil {
		return nil, fmt.Errorf("writing the headings: %w", err)
	}
	for _, row := range d.Rows {
		if err := w.Write(d.text(row)); err != nil {
			return nil, fmt.Errorf("writing a row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("writing the export: %w", err)
	}
	return buf.Bytes(), nil
}

// xlsx writes a plain workbook through the same excelize the fill path uses.
func (d document) xlsx() ([]byte, error) {
	f, err := PlainWorkbook(d.Title, d.headings(), d.Fields, d.Rows)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("writing the workbook: %w", err)
	}
	return buf.Bytes(), nil
}

// pdf renders the print-styled page and hands it to the converter.
func (d document) pdf(ctx context.Context, conv PDFConverter) ([]byte, error) {
	if conv == nil {
		return nil, ErrPDFUnavailable
	}
	html, err := d.printHTML()
	if err != nil {
		return nil, err
	}
	out, err := conv.Render(ctx, html, d.Title)
	if err != nil {
		// Folded into one answer whether the converter is absent, unreachable or
		// refused the document: all three leave the officer with no PDF, and the
		// thing they can do about it -- ask for another format -- is the same.
		return nil, fmt.Errorf("%w: %v", ErrPDFUnavailable, err)
	}
	return out, nil
}

// printHTML is the page the converter is given.
func (d document) printHTML() ([]byte, error) {
	var buf bytes.Buffer
	if err := exportPrintTemplate.Execute(&buf, exportPrintView{
		document: d,
		Headings: d.headings(),
		Body:     d.textRows(),
	}); err != nil {
		return nil, fmt.Errorf("rendering the page: %w", err)
	}
	return buf.Bytes(), nil
}

func (d document) textRows() [][]string {
	out := make([][]string, 0, len(d.Rows))
	for _, row := range d.Rows {
		out = append(out, d.text(row))
	}
	return out
}

type exportPrintView struct {
	document
	Headings []string
	Body     [][]string
}

// devanagariFontStack is the font stack the printed report uses, repeated.
//
// Not invented here and not allowed to drift: these three names, in this order,
// are what makes Marathi render rather than appear as a row of empty boxes --
// "Nirmala UI" on the Windows machines these documents are read on, "Noto Sans
// Devanagari" in the converter's container, "Mangal" on the older installs still
// in taluka offices. The converter has Noto Devanagari installed for exactly this
// reason.
//
// It is repeated rather than imported because the report's copy lives inside a CSS
// literal in an embedded template, with nothing to parameterise. If that stack
// changes, this one changes with it: see internal/report/templates and
// internal/pdf.
const devanagariFontStack = `"Nirmala UI", "Noto Sans Devanagari", "Mangal", ` +
	`system-ui, -apple-system, "Segoe UI", sans-serif`

// exportPrintTemplate is A4 portrait, like every other document this service
// produces.
//
// # Why it is not turned on its side for a wide table
//
// Because these end up in a taluka file, on a treasury tag, beside the printed
// claim and the assessment report -- all of which are A4 portrait. A statement
// that has to be rotated to be read is a statement that gets filed sideways and
// photocopied wrong. A table wider than the page is made to fit instead: small
// type, fixed layout, and headings that repeat because the converter paginates a
// thead for us.
var exportPrintTemplate = template.Must(template.New("export").Parse(`<!doctype html>
<html lang="mr">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
  :root {
    --ink: #16181c;
    --muted: #5d6470;
    --rule: #c9ced8;
    --rule-strong: #7b838f;
    --tint: #f4f6f9;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    color: var(--ink);
    font-family: ` + devanagariFontStack + `;
    font-size: 8.5pt;
    line-height: 1.35;
  }
  header { border-bottom: 2px solid var(--rule-strong); padding-bottom: 6px; }
  h1 { margin: 0 0 3px; font-size: 13pt; line-height: 1.25; }
  .meta { color: var(--muted); font-size: 7.5pt; }
  .note {
    margin: 8px 0 0; padding: 6px 8px;
    background: var(--tint); border: 1px solid var(--rule);
    font-size: 8pt; line-height: 1.5;
  }
  table {
    width: 100%; border-collapse: collapse; margin-top: 10px;
    table-layout: fixed;
  }
  th, td {
    border: 1px solid var(--rule);
    padding: 3px 4px;
    text-align: left;
    vertical-align: top;
    word-wrap: break-word;
    overflow-wrap: anywhere;
  }
  thead th {
    background: var(--tint); font-size: 7.5pt; font-weight: 600;
  }
  /* The converter repeats a thead on every page by itself, which is the only
     reason a thirty-page statement is readable at all. */
  thead { display: table-header-group; }
  tr { page-break-inside: avoid; }
  .empty { color: var(--muted); padding: 14px 0; }
</style>
</head>
<body>
<header>
  <h1>{{.Title}}</h1>
  <div class="meta">{{len .Rows}} नोंदी / claims &middot;
    तयार / prepared {{.Prepared.Format "02-01-2006 15:04"}} IST</div>
</header>
{{if .Note}}<p class="note">{{.Note}}</p>{{end}}
{{if .Body}}
<table>
  <thead><tr>{{range .Headings}}<th>{{.}}</th>{{end}}</tr></thead>
  <tbody>
    {{range .Body}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}
  </tbody>
</table>
{{else}}
<p class="empty">या निकषांवर कोणताही दावा आढळला नाही /
  no claims matched these filters.</p>
{{end}}
</body>
</html>
`))

// resolveFields turns the names the caller chose into the vocabulary's own fields.
//
// Order is the caller's; membership is not. A name outside Fields is refused by
// name, which is what makes the vocabulary closed in practice rather than in a
// comment.
func resolveFields(names []string) ([]Field, error) {
	if len(names) == 0 {
		names = DefaultExportFields
	}
	byName := make(map[string]Field, len(Fields))
	for _, f := range Fields {
		byName[f.Name] = f
	}

	out := make([]Field, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		f, ok := byName[name]
		if !ok {
			return nil, UnknownFieldError{Name: name}
		}
		// A column asked for twice is one column. Printing it twice would look
		// like two different figures that happen to agree.
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, ErrNoFields
	}
	return out, nil
}

// cellText renders one value as the text formats need it.
//
// Floats without trailing zeros, for the same reason the printed report trims
// them: 1.6 hectares on a form somebody reads should not be 1.6000.
func cellText(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case bool:
		return yesNo(n)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// exportTitle is what heads the document.
func exportTitle(raw string) string {
	title := collapseSpace(raw)
	if title == "" {
		return "पीक नुकसान दावे / Crop loss claims"
	}
	// Bounded, because it reaches a filename and a page heading. A title longer
	// than this is a paragraph, and Note is where a paragraph belongs.
	return truncateRunes(title, 120)
}

// exportFilename is descriptive, dated and extensioned.
//
// Descriptive because an officer downloads several of these in a morning and a
// folder of "export(3).xlsx" is a folder of files nobody can tell apart. Dated for
// the same reason. Still not safe to put in a header -- that is the api package's
// job, where the header is written.
func exportFilename(title string, at time.Time, f Format) string {
	return fmt.Sprintf("%s %s.%s",
		truncateRunes(title, 80), at.Format("2006-01-02"), f)
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max]))
}

// byteOrderMark is the three bytes that tell Excel this file is UTF-8.
//
// Written as an escape rather than as the character itself so that it is visible
// in a diff and cannot be lost to an editor that strips a leading mark from a
// source file.
const byteOrderMark = "\ufeff"
