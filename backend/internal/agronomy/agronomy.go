// Package agronomy holds what is known about how a crop grows, so that the
// assessment can reason about a stage rather than a date.
//
// # Why this is a table and not model knowledge
//
// The panel already knows, roughly, that hail at pod-fill is worse than hail
// at seedling. Recalled knowledge cannot be audited: an officer cannot ask
// where a coefficient came from, a farmer cannot be shown it, and it changes
// silently when the model does. A table can be printed, argued with, and
// signed off by an agricultural university.
//
// # What is provisional here
//
// The thermal-time thresholds and the susceptibility matrix below are drawn
// from published ranges for these crops. They are indicative, not the state's
// own figures, and every value carries that provenance to the panel and to
// the screen. They are structured as data precisely so that MPKV Rahuri or
// the Department of Agriculture can replace them without touching code.
//
// Nothing here may move a damage figure on its own. It informs the phenology
// specialist, which reports its reasoning and its limitations.
package agronomy

import "strings"

// Provenance labels where a figure came from, and travels with it.
const (
	// ProvenancePublished means a published agronomic range, not a figure
	// issued by the state for settlement purposes.
	ProvenancePublished = "published_range"
	// ProvenanceOfficial is reserved for tables supplied by the Department of
	// Agriculture or a state agricultural university.
	ProvenanceOfficial = "state_issued"
)

// Stage is one phenological stage, bounded by accumulated thermal time.
type Stage struct {
	Key    string `json:"key"`
	NameEn string `json:"name_en"`
	NameMr string `json:"name_mr"`

	// FromGDD is the accumulated growing degree days at which this stage
	// begins, counted from sowing. ToGDD bounds it; the last stage's ToGDD is
	// physiological maturity.
	FromGDD float64 `json:"from_gdd"`
	ToGDD   float64 `json:"to_gdd"`

	// Recoverable says whether the plant can make up damage sustained here.
	//
	// This is the single most consequential field in the package. A crop
	// defoliated in the vegetative stage regrows and may yield near normally;
	// the same damage at flowering or grain fill is permanent. An assessment
	// that ignores it treats those as the same loss.
	Recoverable bool `json:"recoverable"`
}

// Duration is a variety class. Short and long duration varieties of one crop
// are at different stages on the same date, which is why the crop name alone
// is not enough to place a claim in its cycle.
type Duration struct {
	Key       string  `json:"key"`
	NameEn    string  `json:"name_en"`
	NameMr    string  `json:"name_mr"`
	DaysMin   int     `json:"days_min"`
	DaysMax   int     `json:"days_max"`
	MaturityG float64 `json:"maturity_gdd"`
}

// Crop is the agronomic model for one crop.
type Crop struct {
	Key    string `json:"key"`
	NameEn string `json:"name_en"`
	NameMr string `json:"name_mr"`

	// BaseTempC is the temperature below which development effectively stops,
	// and CutoffTempC the temperature above which it no longer accelerates.
	// Both are needed: a plain mean-minus-base sum overstates development
	// through a Vidarbha May.
	BaseTempC   float64 `json:"base_temp_c"`
	CutoffTempC float64 `json:"cutoff_temp_c"`

	// HeatStressC is the daily maximum above which flowering is impaired, and
	// ColdStressC the minimum below which the crop suffers. Zero means the
	// threshold is not established for this crop and no claim is made.
	HeatStressC float64 `json:"heat_stress_c,omitempty"`
	ColdStressC float64 `json:"cold_stress_c,omitempty"`

	Stages    []Stage    `json:"stages"`
	Durations []Duration `json:"durations"`

	// HarvestedOrgan is what is actually taken off the field.
	//
	// It decides whether a reproductive stage exists at all. Sugarcane is
	// harvested as stem and onion as bulb: both go from vegetative growth
	// straight to filling the organ that is sold, and neither has a flowering
	// stage that damage can be timed against. A crop harvested as seed, fibre
	// or fruit does, and losing it there is permanent.
	HarvestedOrgan string `json:"harvested_organ"`

	// Seasons this crop is grown in, for a sanity check against the report.
	Seasons []string `json:"seasons"`

	// Aliases are the other names this crop is filed under -- Marathi,
	// transliterations, and the spellings that appear in AgriStack records.
	Aliases []string `json:"-"`

	Provenance string `json:"provenance"`
}

// Harvested organs.
const (
	OrganSeed  = "seed"
	OrganFibre = "fibre"
	OrganFruit = "fruit"
	OrganBulb  = "bulb"
	OrganStem  = "stem"
)

// FlowersForYield reports whether this crop's yield passes through a flower.
//
// Where it does, damage at flowering is permanent and the stage must exist in
// the table. Where it does not -- cane, onion -- there is no flowering stage
// to time damage against, and demanding one would be modelling a plant that
// is not there.
func (c *Crop) FlowersForYield() bool {
	switch c.HarvestedOrgan {
	case OrganSeed, OrganFibre, OrganFruit:
		return true
	}
	return false
}

// MaturityGDD is the thermal time to physiological maturity.
func (c *Crop) MaturityGDD() float64 {
	if len(c.Stages) == 0 {
		return 0
	}
	return c.Stages[len(c.Stages)-1].ToGDD
}

// StageAt returns the stage an accumulated thermal time falls in.
//
// Past maturity it returns the last stage: a crop standing in the field after
// maturity is exactly the post-harvest-loss case the form has a category for,
// and returning nothing would lose that.
func (c *Crop) StageAt(gdd float64) (Stage, bool) {
	if len(c.Stages) == 0 {
		return Stage{}, false
	}
	for _, s := range c.Stages {
		if gdd >= s.FromGDD && gdd < s.ToGDD {
			return s, true
		}
	}
	if gdd >= c.MaturityGDD() {
		return c.Stages[len(c.Stages)-1], true
	}
	return c.Stages[0], true
}

// DurationFor returns a named variety class, or the medium one.
//
// Medium rather than nothing: a farmer who does not know their variety is the
// ordinary case, and the middle of the range is the least wrong assumption.
// The caller is told which was used so it can say so.
func (c *Crop) DurationFor(key string) (Duration, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, d := range c.Durations {
		if d.Key == key {
			return d, true
		}
	}
	for _, d := range c.Durations {
		if d.Key == "medium" {
			return d, false
		}
	}
	if len(c.Durations) > 0 {
		return c.Durations[0], false
	}
	return Duration{}, false
}

// Find matches a crop name as it appears in a record.
//
// Records carry "Soybean", "soyabean", "सोयाबीन" and "SOYABEAN" for one crop,
// because they are typed by different people into different systems. A lookup
// that only matched the canonical spelling would silently drop the crop model
// for most real rows, and the assessment would quietly lose its stage
// reasoning without anything saying so.
func Find(name string) (*Crop, bool) {
	q := normalise(name)
	if q == "" {
		return nil, false
	}
	for i := range crops {
		c := &crops[i]
		if normalise(c.Key) == q || normalise(c.NameEn) == q || normalise(c.NameMr) == q {
			return c, true
		}
		for _, a := range c.Aliases {
			if normalise(a) == q {
				return c, true
			}
		}
	}
	return nil, false
}

// All returns every modelled crop, for a form or an admin screen.
func All() []Crop {
	out := make([]Crop, len(crops))
	copy(out, crops)
	return out
}

func normalise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Spaces, hyphens and underscores are noise between systems, not meaning.
	s = strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(s)
	return s
}
