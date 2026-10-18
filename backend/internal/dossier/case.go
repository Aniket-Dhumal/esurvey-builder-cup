package dossier

import (
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// Case is what this claim is about.
//
// # Why the sections need it
//
// Every instrument section could previously say only what its parameter means
// in general. §8 explained that flat ground holds water on a report about a
// drought, where slope decides nothing; §6 warned that a harvest produces the
// same fall in greenness on a crop that was six weeks from harvestable. Both
// sentences were true and neither was about the claim in front of the officer.
//
// The cause was structural: only Weather was told what had been claimed. The
// rest were handed a number and asked to explain it, so generic parameter
// documentation was the only thing they could write.
//
// With the case in hand a section can do the actual analysis: this reading, for
// this calamity, at this growth stage, against what the rain actually did. That
// is what an officer is reading the column for. Where a reading genuinely bears
// nothing on the claim, saying so plainly is also analysis -- and more use than
// a paragraph about what the parameter measures.
type Case struct {
	// Calamity is what was claimed, in the platform's own vocabulary.
	Calamity string

	Crop string

	// Stage is the growth stage the crop had reached when the loss is said to
	// have happened, and PercentOfCycle how far through its life it was.
	// Both empty or zero where no crop was known: a section must read as
	// sensibly then as it does with them.
	Stage          Text
	PercentOfCycle float64
	StageKnown     bool

	SowingDate   string
	IncidentDate string

	// Rain is the state's published rainfall, where any was loaded. Several
	// instruments only mean something read against whether the rain actually
	// failed: a canopy that held up through a 45-point deficit is a different
	// finding from one that held up through a normal season.
	Rain *weather.Official
}

// CaseOf assembles the case from what the report and the evidence carry.
func CaseOf(in Inputs) Case {
	c := Case{Rain: in.OfficialRainfall}

	if in.Report != nil {
		c.Calamity = in.Report.CalamityType
		c.IncidentDate = in.Report.OccurredAt.Format("2006-01-02")
	}
	if in.Thermal != nil {
		c.Crop = in.Thermal.Crop
		c.PercentOfCycle = in.Thermal.PercentOfCycle
		c.Stage = stageText(in.Thermal)
		c.StageKnown = in.Thermal.Stage != ""
	}
	if in.Sowing != nil && in.Sowing.DeclaredDate != "" {
		c.SowingDate = in.Sowing.DeclaredDate
	}
	return c
}

func stageText(t *agronomy.Thermal) Text {
	if t == nil {
		return Text{}
	}
	mr, en := t.StageMr, t.StageEn
	if en == "" {
		en = t.Stage
	}
	if mr == "" {
		mr = en
	}
	return T(mr, en)
}

// The families of calamity an instrument reading means different things for.
//
// Matched on substrings rather than an enum because the calamity vocabulary is
// the state's, arrives as free text on the claim, and grows: "unseasonal rain"
// and "untimely rain" are the same event under two names in two circulars. An
// unrecognised calamity falls through every predicate, and every section is
// written so that the fall-through says what the reading is without asserting
// what it means for an event this code does not know about.

// Drought is a slow loss of water over a season.
func (c Case) Drought() bool {
	return anyOf(c.Calamity, "drought", "dry spell", "dry-spell")
}

// Water is too much of it: flood, inundation, waterlogging, unseasonal rain.
func (c Case) Water() bool {
	return anyOf(c.Calamity,
		"flood", "inundat", "waterlog", "water log",
		"excess rain", "heavy rain", "unseasonal", "untimely")
}

// Wind is the sudden mechanical damage family: hail, storm, cyclone.
func (c Case) Wind() bool {
	return anyOf(c.Calamity, "hail", "storm", "cyclone", "wind", "gale")
}

// Known reports whether the calamity is one this document can reason about.
func (c Case) Known() bool {
	return c.Drought() || c.Water() || c.Wind()
}

// CalamityText is the claimed calamity for printing, or a neutral phrase.
func (c Case) CalamityText() Text {
	if c.Calamity == "" {
		return T("नोंदवलेली आपत्ती", "the calamity claimed")
	}
	// The vocabulary is English in the record. Rendering the Marathi half from
	// a lookup would need a translation per calamity, and an unlisted one would
	// print English inside a Marathi sentence anyway -- so the name is printed
	// as recorded in both halves, which is at least honest about its source.
	name := strings.ReplaceAll(c.Calamity, "_", " ")
	return T(name, name)
}

// RainFailed reports whether the state's own record shows the season short.
//
// Used by sections that are not about rainfall but are read differently
// depending on it: a green canopy through a 45-point deficit is a finding about
// irrigation, and through a normal season it is a finding about nothing.
func (c Case) RainFailed() bool {
	return c.Rain != nil && c.Rain.Season != nil &&
		c.Rain.Season.DeficitPoints() >= 25
}

// HarvestPlausible reports whether the crop was far enough through its cycle
// for an early harvest to explain a fall in greenness.
//
// The question the RRVL row turns on. A harvest is the standard alternative
// explanation for a collapse in canopy, and it is only available once there is
// something to harvest: a crop at a third of its cycle that lost its canopy did
// not lose it to a combine. Warning about harvest there sends an officer after
// an explanation the calendar rules out.
//
// False where no crop or stage was established, because then nothing is ruled
// out and the caveat stands.
func (c Case) HarvestPlausible() bool {
	return !c.StageKnown || c.PercentOfCycle >= 70
}

func anyOf(s string, subs ...string) bool {
	s = strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
