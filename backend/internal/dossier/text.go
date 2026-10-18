// Package dossier builds the assessment report: what the system found, what
// each finding means for the loss estimate, and what evidence stands behind
// it. Marathi in full, then the same report again in English.
//
// See docs/ASSESSMENT_REPORT.md for the specification this implements.
package dossier

import "fmt"

// Lang is which half of the document is being rendered.
//
// There are two halves and never a mixed one. The Marathi report runs from
// summary to appeal, complete, and the English report follows it entire, so
// either can be detached and read on its own.
type Lang int

const (
	// Marathi is the language the farmer reads, and the half that is printed
	// first for that reason.
	Marathi Lang = iota
	// English is the half an appellate officer, an auditor or a court reads.
	English
)

// Text is one sentence in both languages.
//
// # Why a pair in the source rather than a translation at render time
//
// A report whose halves can drift is worse than a monolingual one. Only the
// English half tends to get proofread, and the Marathi half is the one the
// farmer actually reads -- so drift lands entirely on the person least able
// to catch it, in the document that decides their compensation.
//
// A pair in the source cannot drift silently. Changing one language without
// the other does not compile into a plausible sentence; it produces a
// visibly half-written record that a reviewer sees immediately.
//
// It also rules out machine translation at render time, which would put an
// unreviewed sentence about somebody's crop loss on a government form.
type Text struct {
	MR string
	EN string
}

// T builds a bilingual sentence.
func T(mr, en string) Text { return Text{MR: mr, EN: en} }

// Tf builds a bilingual sentence from two format strings and one set of
// arguments.
//
// The arguments are shared deliberately: they are the numbers, dates and
// names being reported, and those are the same fact in both languages. Two
// argument lists would allow the halves to report different values, which is
// precisely the drift this type exists to prevent.
func Tf(mrFormat, enFormat string, args ...any) Text {
	return Text{
		MR: fmt.Sprintf(mrFormat, args...),
		EN: fmt.Sprintf(enFormat, args...),
	}
}

// In returns the half being rendered.
//
// A language with nothing written for it falls back to the other rather than
// rendering an empty cell. An untranslated sentence is a gap somebody should
// fix; a blank cell in a report means "withheld", which is a different and
// worse claim. Showing the English in the Marathi report is visibly a
// shortcoming. Showing nothing is not.
func (t Text) In(l Lang) string {
	switch l {
	case Marathi:
		if t.MR != "" {
			return t.MR
		}
		return t.EN
	default:
		if t.EN != "" {
			return t.EN
		}
		return t.MR
	}
}

// Empty reports whether nothing was written in either language.
func (t Text) Empty() bool { return t.MR == "" && t.EN == "" }

// Texts is a list of bilingual sentences, for working and caveats.
type Texts []Text

// In renders every sentence in one language.
func (ts Texts) In(l Lang) []string {
	if len(ts) == 0 {
		return nil
	}
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.In(l))
	}
	return out
}
