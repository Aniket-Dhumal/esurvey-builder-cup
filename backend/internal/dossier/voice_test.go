package dossier

import (
	"strings"
	"testing"
)

// What the Effect column is for.
//
// Every row of this document carries an Effect: one or two sentences telling the
// officer what the number beside it bears on. The column drifted, over many
// small and individually defensible edits, into telling them mostly what the
// number is *not*:
//
//	"Context, not a measure of loss."
//	"On its own it shows no loss."
//	"This is a fall in greenness, not a measure of damage."
//	"That is a susceptibility and not a loss: flat ground under a thriving
//	 crop is not damage. This figure adds nothing to the loss percentage."
//
// Each was added to head off a real misreading. Together they read as a document
// that does not believe its own evidence, and they crowd out the sentence the
// officer actually needs. Worse, every one of them restates the Role column
// printed on the same row: a row marked Context does not also need prose saying
// it is context.
//
// So: the Effect says what the figure bears on. A limit is stated where a
// specific misreading is genuinely available -- a harvest produces the same NDVI
// fall, lodged plants must not be added to missing ones -- and it comes second,
// once, in the row where it applies.
//
// These tests pin that. They are about the voice of a government document that
// decides whether somebody is paid, which is worth a test.

// disclaimerOnly are phrases that say only what a figure is not. Each one was
// in the document and was removed; a new one appearing means the column is
// drifting back.
var disclaimerOnly = []string{
	"not a measure of loss",
	"not a measure of damage",
	"it shows no loss",
	"adds nothing to the loss percentage",
	"determines nothing",
	"is not damage",
	"Context, not",
	"not a measurement.",
}

func TestTheEffectColumnDoesNotMerelyDisclaim(t *testing.T) {
	for _, s := range Build(fullInputs()).Sections {
		for _, r := range s.Rows {
			for _, lang := range []string{r.Effect.EN, r.Effect.MR} {
				for _, bad := range disclaimerOnly {
					if strings.Contains(lang, bad) {
						t.Errorf("§%s %q: %q says what the figure is not; say "+
							"what it bears on, and leave the limit to the Role "+
							"column unless a specific misreading needs naming",
							s.Number, r.Parameter.EN, bad)
					}
				}
			}
		}
	}
}

func TestNoEffectOpensWithADisclaimer(t *testing.T) {
	// The first sentence is the one that gets read. A row whose opening
	// sentence is about what the number cannot do has spent its only
	// guaranteed sentence on a caveat.
	opens := []string{
		"This is a fall in",
		"This is a reanalysis",
		"A lodged plant is not",
		"That is a susceptibility",
	}
	for _, s := range Build(fullInputs()).Sections {
		for _, r := range s.Rows {
			first := strings.SplitN(r.Effect.EN, ". ", 2)[0]
			for _, bad := range opens {
				if strings.HasPrefix(strings.TrimSpace(first), bad) {
					t.Errorf("§%s %q opens with %q", s.Number, r.Parameter.EN, first)
				}
			}
		}
	}
}

func TestTheLoadBearingLimitsAreStillThere(t *testing.T) {
	// The other half of the rule. Trimming the disclaimers must not take the
	// three that are doing real work, each of which prevents a specific and
	// documented misreading.
	rows := map[string]Row{}
	for _, s := range Build(fullInputs()).Sections {
		for _, r := range s.Rows {
			rows[r.Parameter.EN] = r
		}
	}

	for _, c := range []struct {
		row, must, why string
	}{
		{
			row:  "Relative vegetation loss (RRVL)",
			must: "harvest",
			why: "the single most available misreading in the document: a farmer " +
				"who harvested early produces the same curve as one who lost the " +
				"crop. The row must either offer that explanation or rule it out, " +
				"and TestTheHarvestAlternativeFollowsTheCalendar checks which",
		},
		{
			row:  "Lodged plants",
			must: "kept separate from stand loss",
			why:  "adding the two would count the same plant's loss twice",
		},
		{
			row:  "Total rainfall (reanalysis model — for comparison)",
			must: "read the state's figure above",
			why: "the model reports roughly twice the rain that fell over this " +
				"region, and an officer citing it loses the drought",
		},
	} {
		r, ok := rows[c.row]
		if !ok {
			t.Errorf("%q is not in the report at all", c.row)
			continue
		}
		if !strings.Contains(r.Effect.EN, c.must) {
			t.Errorf("%q no longer says %q.\nThis limit is load-bearing: %s.\nGot: %s",
				c.row, c.must, c.why, r.Effect.EN)
		}
	}
}

func TestEveryEffectSaysSomethingAffirmative(t *testing.T) {
	// A weaker but broader check: an Effect made only of negations tells the
	// reader nothing they can act on. Measured crudely -- at least one clause
	// with no negation in it -- because the alternative is a style guide nobody
	// runs.
	neg := func(s string) bool {
		for _, w := range []string{
			" not ", " no ", "cannot", "never", "nothing", "neither",
		} {
			if strings.Contains(strings.ToLower(s), w) {
				return true
			}
		}
		return false
	}

	for _, s := range Build(fullInputs()).Sections {
		for _, r := range s.Rows {
			if r.Absent || r.Effect.EN == "" {
				// An absent row's whole point is an absence.
				continue
			}
			var affirmative bool
			for _, clause := range strings.Split(r.Effect.EN, ". ") {
				if strings.TrimSpace(clause) != "" && !neg(clause) {
					affirmative = true
				}
			}
			if !affirmative {
				t.Errorf("§%s %q has no affirmative clause: %s",
					s.Number, r.Parameter.EN, r.Effect.EN)
			}
		}
	}
}

func TestBothLanguagesCarryTheSameNumberOfSentences(t *testing.T) {
	// Trimming a disclaimer in English and leaving it in Marathi is exactly the
	// drift the Text type exists to prevent, and it lands on the farmer -- the
	// reader least able to catch it. Sentence counts are a blunt proxy and
	// catch a whole clause dropped from one half.
	for _, s := range Build(fullInputs()).Sections {
		for _, r := range s.Rows {
			en := strings.Count(r.Effect.EN, ".") + strings.Count(r.Effect.EN, "।")
			mr := strings.Count(r.Effect.MR, ".") + strings.Count(r.Effect.MR, "।")
			if r.Effect.EN == "" || r.Effect.MR == "" {
				continue
			}
			if d := en - mr; d > 1 || d < -1 {
				t.Errorf("§%s %q: %d sentences in English, %d in Marathi\nEN: %s\nMR: %s",
					s.Number, r.Parameter.EN, en, mr, r.Effect.EN, r.Effect.MR)
			}
		}
	}
}
