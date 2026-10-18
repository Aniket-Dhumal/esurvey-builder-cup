package dossier

import (
	"html/template"
	"time"
)

// Role is what a parameter did to the loss figure.
//
// # Why every row carries one
//
// A table laid out like a lab report implies, by layout alone, that every row
// fed the result. In this system almost none of them did: the loss figure is
// set by a quadrat count and by officers, and NDVI, backscatter, rainfall and
// slope corroborate it or explain the conditions around it.
//
// assessment.Satellite says so in its own doc comment -- "They are not a
// damage measurement" -- and printing NDVI at the same visual weight as a
// plant count would make the report contradict the code it was built from.
// That is a lie told through typography, and it is the kind an appeal finds.
//
// So the role is on the row, in the document, in both languages. If the
// system ever starts deriving the figure from something else, the marker
// changes with it and the change is visible on the page rather than buried in
// a scoring function.
type Role int

const (
	// Determines: this entered the loss figure directly.
	Determines Role = iota
	// Corroborates: independent evidence consistent, or not, with the figure.
	Corroborates
	// Context: explains the conditions, carries no weight on its own.
	Context
	// Excluded: available and deliberately not used. The reason is required.
	Excluded
)

// Label is how the role prints.
func (r Role) Label() Text {
	switch r {
	case Determines:
		return T("निर्णायक", "Determines")
	case Corroborates:
		return T("पुष्टीकरण", "Corroborates")
	case Context:
		return T("संदर्भ", "Context")
	case Excluded:
		return T("वगळले", "Excluded")
	}
	return T("", "")
}

// Row is one parameter: what was observed, where it came from, what it means
// for the estimate, and what stands behind it.
type Row struct {
	// Parameter is the name as it prints.
	Parameter Text

	// Observed is the value, already formatted for reading. Numbers are
	// rendered here rather than carried as floats, because a figure's
	// precision is part of what it claims -- "0.68" and "0.6800" say
	// different things about the instrument.
	Observed Text

	// Source is the instrument, dataset or person, and the method.
	Source Text

	// Reference is what the observation should be compared against: a peer
	// median, a threshold, an expectation from the crop model.
	Reference Text

	// Effect is what this value means for the loss estimate, in prose.
	//
	// This is the column the report exists for and the one nothing in the
	// system produced before. A number without it is a number the farmer
	// cannot act on and an appellate officer cannot weigh.
	Effect Text

	// Evidence is what a reader would have to fetch to check the row: a
	// capture id, a pass date, an officer's name.
	Evidence Text

	Role Role

	// Working is the arithmetic in words.
	//
	// fieldwork.Derived.Working already does this and its comment is the rule
	// for the whole report: an officer should read a derivation rather than a
	// result. A number nobody can check is a number an appeal discards.
	Working Texts

	// Caveats are what this observation cannot support.
	Caveats Texts

	// Absent marks a parameter that was not obtained, with AbsentWhy saying
	// so in words.
	//
	// A blank cell in a report reads as withheld. A lab report writes "not
	// tested", and so does this one: a clouded window, an unavailable radar
	// pass, a claim with no quadrat count are all findings, and each changes
	// how much weight the rest of the document carries.
	Absent    bool
	AbsentWhy Text
}

// Absent builds a row for a parameter that could not be obtained.
func AbsentRow(parameter, why Text, role Role) Row {
	return Row{
		Parameter: parameter,
		Observed:  T("मिळाले नाही", "Not obtained"),
		Role:      role,
		Absent:    true,
		AbsentWhy: why,
	}
}

// Section is one numbered part of the report.
type Section struct {
	// Number is the printed numbering, as a string because the summary has
	// none and the appeal section is not arithmetic.
	Number string

	Title Text

	// Preamble governs how the rows beneath it must be read. The context
	// notes section needs one; most do not.
	Preamble Text

	Rows []Row

	// Notes are sentences that belong to the section rather than to any one
	// row.
	Notes Texts

	// Figure is a drawing printed above the rows: the map of the walk, the
	// frames from each clip. Rendered ahead of the table because a reader
	// looks at a picture first whatever the order on the page, and a caption
	// under a figure they have already formed a view about is too late.
	Figure *Figure
}

// Figure is a drawing and what it is of.
type Figure struct {
	// SVG is drawn markup this package produced. Never anything a caller
	// supplied: it is emitted into the document unescaped.
	SVG template.HTML

	// Plates are images, each with its own finding. Used for the frames
	// lifted from the four clips.
	Plates []Plate

	Caption Text

	// Legend explains what each mark on the drawing claims, which for this
	// report is the difference between a measurement and an assumption.
	Legend []LegendItem
}

// LegendItem is one mark and what it means.
type LegendItem struct {
	// Key matches the CSS class on the mark it describes.
	Key   string
	Label Text
}

// Plate is one image printed in the report, with what was found in it.
type Plate struct {
	// Src is a data URI. The document is read offline, printed, and attached
	// to an appeal file; a link to a server is not evidence in a year.
	Src template.URL

	Title   Text
	Finding Text

	// Where and when, so a plate can be tied to the map above it.
	Meta Text
}

// Any reports whether the section has anything to print.
func (s Section) Any() bool {
	return len(s.Rows) > 0 || len(s.Notes) > 0 || !s.Preamble.Empty() ||
		s.Figure != nil
}

// State is whether the report carries a decided figure.
type State int

const (
	// Provisional: evidence gathered and the assessment run, but no officer
	// consensus yet.
	Provisional State = iota
	// Final: signed off, with the deciding officers named.
	Final
)

// Label is the stamp printed across the head of the document.
func (s State) Label() Text {
	if s == Final {
		return T("अंतिम अहवाल", "FINAL REPORT")
	}
	return T("तात्पुरता अहवाल", "PROVISIONAL REPORT")
}

// Denominator is the answer to "a percentage of what?".
//
// # Why this is a type of its own and prints before anything else
//
// docs/LOSS_PARAMETERS.md opens on the problem: damage_percent is a
// percentage of nothing defined. Sixty per cent lost from a crop already
// failing in a dry August is a different fact from sixty per cent off a crop
// heading for its best year, and no quantity of NDVI tables tells the two
// apart.
//
// A lab report is readable because each value sits beside its reference
// range. This is that reference range, and it goes at the top for the same
// reason a blood panel puts it in the next column rather than an appendix.
type Denominator struct {
	// Known is false when no threshold yield could be derived.
	Known bool

	ThresholdYield float64
	Unit           Text

	// Basis is how it was arrived at: best five of seven seasons, a peer
	// set, a crop-cutting experiment.
	Basis Text

	Working Texts

	// AbsentWhy is required when Known is false.
	//
	// The report then presents the loss as a judgement recorded by named
	// officers rather than as a measurement. That is what it is, and saying
	// so costs nothing the system ever actually had.
	AbsentWhy Text
}

// Dossier is the whole report, in one structure, rendered twice.
type Dossier struct {
	State State

	// Std is the issuing office, the authority, and who signs -- the
	// furniture that makes this a record rather than a printout. See
	// standard.go.
	Std Standard

	// Reference is the report's own reference, and Supersedes carries the
	// reference of the final report that replaced this one, so two printed
	// copies in a farmer's hand can always be told apart.
	Reference  string
	Supersedes string

	GeneratedAt time.Time

	// Claim identity, for the head of the document.
	FarmerName   string
	Place        Text
	SurveyNumber string
	Crop         Text
	Calamity     Text
	OccurredAt   time.Time

	Denominator Denominator

	// AssessedLossPercent is nil on a provisional report.
	//
	// Deliberately absent rather than estimated: a provisional number becomes
	// the number everybody remembers, and no later correction catches up with
	// it.
	AssessedLossPercent *float64
	DecidedBy           Texts
	DecidedAt           *time.Time

	// Triage is the audit tier the claim was routed at.
	Triage Text

	// Headline is the two or three parameters that most influenced the
	// outcome, for the farmer who reads only the first page.
	Headline []Row

	Sections []Section

	// InputsDigest ties the report to the exact inputs that produced it, so a
	// past report can be re-derived and shown to be unaltered. M7's exit
	// criterion, and a report an auditor cannot verify is one an appeal can
	// dismiss.
	InputsDigest string
}

// Section returns the section with a given number, and whether it exists.
func (d *Dossier) Section(number string) (Section, bool) {
	for _, s := range d.Sections {
		if s.Number == number {
			return s, true
		}
	}
	return Section{}, false
}

// Add appends a section if it has anything to say.
//
// An empty section is not printed as an empty heading: a reader counting
// headings would take the gap for a missing analysis rather than a section
// that had no rows. Where the absence itself matters, the builder adds an
// absent row instead, which says so in words.
func (d *Dossier) Add(s Section) {
	if !s.Any() {
		return
	}
	d.Sections = append(d.Sections, s)
}

// Determining returns the rows that actually set the figure.
//
// Used by the summary, and by tests that assert the document does not quietly
// grow a second determining section without anybody deciding it should.
func (d *Dossier) Determining() []Row {
	var out []Row
	for _, s := range d.Sections {
		for _, r := range s.Rows {
			if r.Role == Determines && !r.Absent {
				out = append(out, r)
			}
		}
	}
	return out
}
