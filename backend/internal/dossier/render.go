package dossier

import (
	"embed"
	"html/template"
	"io"
	"strconv"
	"strings"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// view is one language's rendering of the whole document.
//
// The template is written once against flat strings and executed twice, so
// the Marathi report and the English report cannot differ in structure -- only
// in the words. A template with language conditionals in it would be two
// layouts pretending to be one, and the day they diverged nobody would notice
// until a farmer's copy was missing a section the officer's copy had.
type view struct {
	Lang Lang

	// LangTag and StampKey exist so the template never has to compare a typed
	// value or match on wording. A template that keyed off the English stamp
	// text would silently stop styling the Marathi half, which is the half
	// nobody reviewing the code reads.
	LangTag  string
	StampKey string

	Heading     string
	Stamp       string
	Reference   string
	Supersedes  string
	GeneratedAt string

	FarmerName   string
	Place        string
	SurveyNumber string
	Crop         string
	Calamity     string
	OccurredAt   string
	Triage       string

	DenominatorKnown   bool
	DenominatorValue   string
	DenominatorBasis   string
	DenominatorWorking []string
	DenominatorAbsent  string

	LossFigure string
	LossKnown  bool
	DecidedBy  []string
	DecidedAt  string

	Cover      coverView
	Signatures signatureView

	Headline []rowView
	Sections []sectionView

	Digest string

	// Column headings, in this language.
	ColParameter, ColObserved, ColSource string
	ColReference, ColEffect, ColEvidence string

	// Standing labels the template needs in words.
	LabelWorking, LabelCaveats, LabelNotAvailable string
	LabelDenominator, LabelAssessedLoss           string
	LabelNoFigureYet, LabelDigest                 string
	LabelReference, LabelSupersedes               string
	LabelFarmer, LabelPlace, LabelSurvey          string
	LabelCrop, LabelEvent, LabelWhatDecided       string
}

type sectionView struct {
	Number   string
	Title    string
	Preamble string
	Rows     []rowView
	Notes    []string

	// Figure is the section's drawing and plates, localised.
	Figure *figureView

	// ShowEvidence is false when no row in the section cites anything.
	//
	// A column that is empty for every row of a section still takes its width,
	// which leaves a dead band down the right of the page and squeezes the
	// prose that the report exists to carry. Dropping it is not hiding
	// anything: there was nothing in it.
	ShowEvidence bool

	// EvidenceSpan is what an absent row's message spans, which changes with
	// the column above.
	EvidenceSpan int
}

type rowView struct {
	Parameter, Observed, Source string
	Reference, Effect, Evidence string
	Role                        string
	RoleKey                     string
	Working, Caveats            []string
	Absent                      bool
	AbsentWhy                   string
}

var printTemplate = template.Must(
	template.New("assessment_report.gohtml").Funcs(template.FuncMap{
		"pct": func(v float64) string {
			return strconv.FormatFloat(v, 'f', -1, 64)
		},
	}).ParseFS(templateFS, "templates/*.gohtml"))

// Print writes the report as a printable HTML page: Marathi in full, then the
// same report again in English.
//
// HTML with print styles rather than a generated PDF, for the reason
// internal/report gives and one more. A PDF library is a dependency, a
// font-embedding problem and a layout engine to fight; a browser has all
// three, prints to PDF on any officer's machine, and renders on a phone in a
// field. The extra reason here is Devanagari: shaping conjuncts correctly is
// exactly the thing PDF libraries get wrong, and a clipped Marathi glyph is a
// different word.
func (d *Dossier) Print(w io.Writer) error {
	return printTemplate.Execute(w, struct {
		Fonts   template.CSS
		Stack   template.CSS
		Marathi view
		English view
	}{
		Fonts:   fontFaces(),
		Stack:   FontStack,
		Marathi: d.view(Marathi),
		English: d.view(English),
	})
}

func (d *Dossier) view(l Lang) view {
	v := view{
		Lang:        l,
		LangTag:     map[Lang]string{Marathi: "mr", English: "en"}[l],
		StampKey:    stampKey(d.State),
		Heading:     pick(l, "पीक नुकसान मूल्यांकन अहवाल", "Crop Loss Assessment Report"),
		Stamp:       d.State.Label().In(l),
		Reference:   d.Reference,
		Supersedes:  d.Supersedes,
		GeneratedAt: day(d.GeneratedAt),

		FarmerName:   orDash(d.FarmerName, "—"),
		Place:        d.Place.In(l),
		SurveyNumber: orDash(d.SurveyNumber, "—"),
		Crop:         d.Crop.In(l),
		Calamity:     d.Calamity.In(l),
		OccurredAt:   day(d.OccurredAt),
		Triage:       d.Triage.In(l),

		Digest: d.InputsDigest,

		ColParameter: pick(l, "निकष", "Parameter"),
		ColObserved:  pick(l, "निरीक्षित मूल्य", "Observed value"),
		ColSource:    pick(l, "स्रोत व पद्धत", "Source and method"),
		ColReference: pick(l, "अपेक्षित / संदर्भ", "Expected / reference"),
		ColEffect:    pick(l, "अंदाजावरील परिणाम", "Effect on the estimate"),
		ColEvidence:  pick(l, "पुरावा", "Evidence"),

		LabelWorking:      pick(l, "गणित", "Working"),
		LabelCaveats:      pick(l, "मर्यादा", "Caveats"),
		LabelNotAvailable: pick(l, "उपलब्ध नाही", "Not obtained"),
		LabelDenominator:  pick(l, "संदर्भ उत्पादन", "Threshold yield"),
		LabelAssessedLoss: pick(l, "निर्धारित नुकसान", "Assessed loss"),
		LabelNoFigureYet: pick(l,
			"अंतिम निर्णय झालेला नाही",
			"No decision has been reached yet"),
		LabelDigest:     pick(l, "नोंदींचा पडताळणी क्रमांक", "Inputs digest"),
		LabelReference:  pick(l, "अहवाल क्रमांक", "Report number"),
		LabelSupersedes: pick(l, "बदलून आलेला अहवाल", "Superseded by"),

		LabelFarmer: pick(l, "शेतकरी", "Farmer"),
		LabelPlace:  pick(l, "गाव, तालुका, जिल्हा", "Village, taluka, district"),
		LabelSurvey: pick(l, "सर्व्हे क्रमांक", "Survey number"),
		LabelCrop:   pick(l, "पीक", "Crop"),
		LabelEvent:  pick(l, "घटना", "Event"),

		// The heading over the summary table. It names what the rows below it
		// are, which is the one thing a farmer reading only the first page has
		// to understand: these, and not the rest, are what set the figure.
		LabelWhatDecided: pick(l,
			"आकडा कशावरून ठरला", "What decided the figure"),
	}

	if d.Denominator.Known {
		v.DenominatorKnown = true
		v.DenominatorValue = strconv.FormatFloat(d.Denominator.ThresholdYield, 'f', 0, 64) +
			" " + d.Denominator.Unit.In(l)
		v.DenominatorBasis = d.Denominator.Basis.In(l)
		v.DenominatorWorking = d.Denominator.Working.In(l)
	} else {
		v.DenominatorAbsent = d.Denominator.AbsentWhy.In(l)
	}

	if d.AssessedLossPercent != nil {
		v.LossKnown = true
		v.LossFigure = strconv.FormatFloat(*d.AssessedLossPercent, 'f', 0, 64) + "%"
	}
	v.DecidedBy = d.DecidedBy.In(l)
	if d.DecidedAt != nil {
		v.DecidedAt = day(*d.DecidedAt)
	}

	v.Cover = d.cover(l)
	v.Signatures = d.signatures(l)

	for _, r := range d.Headline {
		v.Headline = append(v.Headline, rowViewOf(r, l))
	}
	for _, s := range d.Sections {
		sv := sectionView{
			Number:   s.Number,
			Title:    s.Title.In(l),
			Preamble: s.Preamble.In(l),
			Notes:    s.Notes.In(l),
			Figure:   figureViewOf(s.Figure, l),
		}
		for _, r := range s.Rows {
			rv := rowViewOf(r, l)
			if strings.TrimSpace(rv.Evidence) != "" && rv.Evidence != emDash {
				sv.ShowEvidence = true
			}
			sv.Rows = append(sv.Rows, rv)
		}

		sv.EvidenceSpan = 4
		if sv.ShowEvidence {
			sv.EvidenceSpan = 5
		}

		v.Sections = append(v.Sections, sv)
	}

	return v
}

func rowViewOf(r Row, l Lang) rowView {
	return rowView{
		Parameter: r.Parameter.In(l),
		Observed:  r.Observed.In(l),
		Source:    r.Source.In(l),
		Reference: r.Reference.In(l),
		Effect:    r.Effect.In(l),
		Evidence:  r.Evidence.In(l),
		Role:      r.Role.Label().In(l),
		RoleKey:   roleKey(r.Role),
		Working:   r.Working.In(l),
		Caveats:   r.Caveats.In(l),
		Absent:    r.Absent,
		AbsentWhy: r.AbsentWhy.In(l),
	}
}

// roleKey is the class name the stylesheet colours the marker by. Kept
// separate from the printed label so the styling does not depend on the
// wording, in either language.
func roleKey(r Role) string {
	switch r {
	case Determines:
		return "determines"
	case Corroborates:
		return "corroborates"
	case Context:
		return "context"
	default:
		return "excluded"
	}
}

func pick(l Lang, mr, en string) string { return T(mr, en).In(l) }

// emDash is what a row prints when a field has nothing to say. It is not
// evidence, so a column of them is still an empty column.
const emDash = "—"

// figureView is a Figure with every Text resolved to one language.
type figureView struct {
	SVG     template.HTML
	Caption string
	Legend  []legendView
	Plates  []plateView
}

type legendView struct {
	Key   string
	Label string
}

type plateView struct {
	Src     template.URL
	Title   string
	Meta    string
	Finding string
}

func figureViewOf(f *Figure, l Lang) *figureView {
	if f == nil {
		return nil
	}
	out := &figureView{SVG: f.SVG, Caption: f.Caption.In(l)}
	for _, item := range f.Legend {
		out.Legend = append(out.Legend, legendView{
			Key: item.Key, Label: item.Label.In(l),
		})
	}
	for _, p := range f.Plates {
		out.Plates = append(out.Plates, plateView{
			Src:     p.Src,
			Title:   p.Title.In(l),
			Meta:    p.Meta.In(l),
			Finding: p.Finding.In(l),
		})
	}
	return out
}
