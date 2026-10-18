package officeragent

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Filling an officer's own report format with real figures.
//
// # Why the model maps and does not fill
//
// An officer arrives with a spreadsheet somebody in their office designed: columns
// in their own order, headings in their own words, sometimes Marathi, sometimes
// abbreviated past recognition. Working out that "बाधित क्षेत्र" and "Affected Area
// (Ha)" and "Area damaged" are the same field is a language problem, and that is
// what the model is for.
//
// Putting the numbers in is not a language problem. So the model is shown the
// headings and nothing else, answers with a mapping from heading to field name, and
// this code writes the values. Three things follow, all of them worth having:
//
//   - No claim data reaches the model to fill a report. Only column headings do,
//     and a blank format carries nobody's name.
//   - A wrong mapping is visible. It shows up as a column of the wrong figures,
//     which an officer spots immediately, rather than as a plausible number
//     quietly invented in the right place.
//   - The arithmetic is ours. A model asked to total a column will sometimes
//     total it wrongly, and a wrong total on a signed form is the worst artefact
//     this system could produce.
//
// # What is not attempted
//
// Rewriting the officer's PDF. A PDF is a layout rather than a form, and editing
// one generically means a font-embedding problem and a layout engine to fight --
// the same reasoning that keeps this service rendering print-styled HTML and
// letting the browser make the PDF. A PDF format is read for its columns and the
// answer comes back as a workbook.

// MaxTemplateBytes bounds an uploaded format.
//
// Two megabytes is a generous spreadsheet of headings. A format is a handful of
// rows; anything larger is a populated report somebody sent by mistake, and reading
// it would mean holding a stranger's data in memory to no purpose.
const MaxTemplateBytes = 2 << 20

// MaxTemplateColumns bounds how wide a format may be.
//
// Beyond this the mapping prompt grows without limit and the model's answer becomes
// unreliable in the middle of it. Sixty columns is already an unusual report.
const MaxTemplateColumns = 60

// ErrNoHeadings means the upload had no readable header row.
var ErrNoHeadings = errors.New("no column headings were found in that file")

// Template is an officer's report format, as far as this needs to understand it.
type Template struct {
	// Sheet the headings were found on.
	Sheet string

	// HeaderRow is 1-indexed, as a person counts rows in a spreadsheet.
	HeaderRow int

	// Headings in column order, trimmed. Empty strings are kept so the column
	// positions stay true: a blank column in the middle of a format is a column.
	Headings []string
}

// ReadTemplate finds the header row of an uploaded workbook.
//
// # Why the header row is searched for rather than assumed
//
// Because it is almost never row one. Real formats open with a title, the name of
// the office, the season, a blank line, and then the headings. Assuming row one
// produces a mapping built from the report's title, which then maps nothing.
//
// The row with the most non-empty, non-numeric cells in the first fifteen wins.
// Headings are words; a row of numbers is data that has strayed above the header,
// and a title row is one long cell rather than many short ones.
func ReadTemplate(r io.Reader) (*Template, error) {
	f, err := excelize.OpenReader(io.LimitReader(r, MaxTemplateBytes))
	if err != nil {
		return nil, fmt.Errorf("that file could not be read as a workbook: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, ErrNoHeadings
	}

	best := &Template{}
	bestScore := 0

	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		for i, row := range rows {
			if i >= 15 {
				break
			}
			score := 0
			for _, cell := range row {
				text := strings.TrimSpace(cell)
				if text == "" || isNumeric(text) {
					continue
				}
				score++
			}
			if score > bestScore {
				bestScore = score
				best = &Template{
					Sheet:     sheet,
					HeaderRow: i + 1,
					Headings:  trimmed(row),
				}
			}
		}
	}

	// One heading is a title, not a header row. A format worth filling has
	// columns.
	if bestScore < 2 {
		return nil, ErrNoHeadings
	}
	if len(best.Headings) > MaxTemplateColumns {
		best.Headings = best.Headings[:MaxTemplateColumns]
	}
	return best, nil
}

// Fill writes rows beneath the template's header row and returns the workbook.
//
// The officer's own file is edited rather than a new one built from it: their
// column widths, their merged title, their office's letterhead and their sheet
// names are the reason they sent this format instead of asking for a CSV.
//
// [fields] is the mapping the model produced -- one field name per column, in
// column order, empty for a column this cannot fill. [rows] supplies a value per
// field name per claim.
func Fill(
	r io.Reader, t *Template, fields []string, rows []map[string]any,
) (*excelize.File, error) {
	f, err := excelize.OpenReader(io.LimitReader(r, MaxTemplateBytes))
	if err != nil {
		return nil, fmt.Errorf("that file could not be read as a workbook: %w", err)
	}

	for i, row := range rows {
		// Beneath the headings, in order. Anything already below them is
		// overwritten: a format often ships with one example row, and an example
		// left in place among real figures is a figure somebody will read.
		at := t.HeaderRow + 1 + i
		for col, field := range fields {
			if field == "" {
				continue
			}
			value, ok := row[field]
			if !ok || value == nil {
				// Left as it is rather than written blank. A column this cannot
				// fill keeps whatever the format put there, which is usually a
				// formula the office relies on.
				continue
			}
			ref, err := excelize.CoordinatesToCellName(col+1, at)
			if err != nil {
				continue
			}
			if err := f.SetCellValue(t.Sheet, ref, value); err != nil {
				return nil, fmt.Errorf("writing %s: %w", ref, err)
			}
		}
	}
	return f, nil
}

func trimmed(row []string) []string {
	out := make([]string, len(row))
	for i, c := range row {
		out[i] = strings.TrimSpace(c)
	}
	return out
}

// isNumeric reports whether a cell is just a number.
//
// Used to tell a header row from a stray row of data. Deliberately crude: a
// heading like "2026-27" would be misread as data by a stricter test, and the
// scoring only needs to prefer words over figures rather than classify perfectly.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ',' && r != '-' && r != ' ' {
			return false
		}
	}
	return true
}

// PlainWorkbook builds a workbook from nothing, for an officer who supplied no
// format.
//
// The other half of this file's story. Fill edits an office's own document because
// their layout is the reason they sent it; this is for the officer who just wants
// the figures, and there is no layout to preserve -- so the headings are ours, the
// column order is the one they chose, and the numbers are written as numbers rather
// than as text that looks like numbers. An officer who totals a column in Excel
// gets a total.
//
// Written here rather than in a second writer of its own because the excelize
// handling, the cell-reference arithmetic and the reasoning about what reaches a
// spreadsheet all already live in this file.
func PlainWorkbook(
	title string, headings []string, fields []Field, rows []map[string]any,
) (*excelize.File, error) {
	f := excelize.NewFile()

	// A fixed English sheet name, and deliberately: a sheet name may not contain
	// a slash, which every bilingual heading in this export does, and a sheet
	// renamed to half a title is worse than one named plainly. The title is on
	// the file instead, where it is read.
	const sheet = "Claims"
	index, err := f.NewSheet(sheet)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("building the workbook: %w", err)
	}
	f.SetActiveSheet(index)
	// excelize starts a file with Sheet1, which would otherwise be an empty
	// first tab the officer opens onto.
	if err := f.DeleteSheet("Sheet1"); err != nil {
		f.Close()
		return nil, fmt.Errorf("building the workbook: %w", err)
	}
	f.SetDocProps(&excelize.DocProperties{Title: title}) //nolint:errcheck // cosmetic

	// The same font reasoning as every other format here: Devanagari is stored
	// correctly whatever the font, but a cell left to the workbook's default
	// renders as empty boxes on a machine whose default has no Devanagari. Naming
	// a font that does is three lines and the difference between a readable
	// statement and an unreadable one.
	head, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Family: devanagariFontName},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("building the workbook: %w", err)
	}
	body, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: devanagariFontName},
		Alignment: &excelize.Alignment{Vertical: "top"},
	})
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("building the workbook: %w", err)
	}

	for col, heading := range headings {
		ref, err := excelize.CoordinatesToCellName(col+1, 1)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("building the workbook: %w", err)
		}
		if err := f.SetCellValue(sheet, ref, heading); err != nil {
			f.Close()
			return nil, fmt.Errorf("writing %s: %w", ref, err)
		}
		name, err := excelize.ColumnNumberToName(col + 1)
		if err != nil {
			continue
		}
		if err := f.SetColStyle(sheet, name, body); err != nil {
			continue
		}
		// Wide enough for a bilingual heading and a village name, narrow enough
		// that eleven columns still print.
		width := float64(len([]rune(heading))) + 3
		if width < 12 {
			width = 12
		}
		if width > 34 {
			width = 34
		}
		_ = f.SetColWidth(sheet, name, name, width)
	}
	if err := f.SetCellStyle(sheet, "A1",
		lastHeadingCell(len(headings)), head); err != nil {
		f.Close()
		return nil, fmt.Errorf("styling the headings: %w", err)
	}
	// A thousand rows scrolled without the headings is a thousand rows nobody
	// can read the fourth column of.
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	for i, row := range rows {
		for col, field := range fields {
			value, ok := row[field.Name]
			if !ok || value == nil {
				continue
			}
			ref, err := excelize.CoordinatesToCellName(col+1, i+2)
			if err != nil {
				continue
			}
			if err := f.SetCellValue(sheet, ref, value); err != nil {
				f.Close()
				return nil, fmt.Errorf("writing %s: %w", ref, err)
			}
		}
	}
	return f, nil
}

// devanagariFontName is the one name a spreadsheet cell can carry.
//
// A cell style takes a single font rather than a stack, so this is the first
// entry of the stack the printed report uses -- the one present on the Windows
// machines these workbooks are opened on. See devanagariFontStack in export.go.
const devanagariFontName = "Nirmala UI"

func lastHeadingCell(columns int) string {
	if columns < 1 {
		columns = 1
	}
	ref, err := excelize.CoordinatesToCellName(columns, 1)
	if err != nil {
		return "A1"
	}
	return ref
}
