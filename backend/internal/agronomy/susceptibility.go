package agronomy

import "strings"

// How badly a calamity hurts, given where the crop is in its cycle.
//
// # Why this is a matrix over stage classes and not over stages
//
// A full crop × stage × calamity table would have several hundred cells, and
// almost all of them would be invented. The agronomic pattern that actually
// generalises is coarser and defensible: what matters is whether the plant is
// still building tissue it can replace, or is building the organ that gets
// harvested. Soybean at flowering and wheat at anthesis behave alike under
// hail for the same reason, and saying so in one row is more honest than
// filling two hundred cells to look thorough.
//
// Where a crop genuinely departs from its class -- cotton's indeterminate
// habit lets it re-flower after early boll loss in a way a determinate cereal
// cannot -- that is an exception below, not a whole extra dimension.

// Stage classes.
const (
	ClassEstablishment = "establishment"
	ClassVegetative    = "vegetative"
	ClassReproductive  = "reproductive"
	ClassFill          = "fill"
	ClassMaturity      = "maturity"
)

// Severity levels, in order.
const (
	SeverityLow      = "low"
	SeverityModerate = "moderate"
	SeverityHigh     = "high"
	SeveritySevere   = "severe"
)

// stageClass maps a stage key to its class.
//
// Derived rather than stored on each stage, so the crop table cannot drift
// out of step with the matrix: a stage key that is not here has no class, and
// the lookup says it does not know rather than guessing.
var stageClass = map[string]string{
	"emergence":     ClassEstablishment,
	"establishment": ClassEstablishment,
	"germination":   ClassEstablishment,
	"bud_burst":     ClassEstablishment,

	"vegetative":   ClassVegetative,
	"tillering":    ClassVegetative,
	"squaring":     ClassVegetative,
	"shoot_growth": ClassVegetative,

	"flowering":          ClassReproductive,
	"anthesis":           ClassReproductive,
	"silking":            ClassReproductive,
	"boot":               ClassReproductive,
	"panicle_initiation": ClassReproductive,
	"jointing":           ClassReproductive,
	"bulb_initiation":    ClassReproductive,

	"pod_development":   ClassFill,
	"seed_fill":         ClassFill,
	"grain_fill":        ClassFill,
	"boll_development":  ClassFill,
	"pegging":           ClassFill,
	"berry_development": ClassFill,
	"bulb_development":  ClassFill,
	"grand_growth":      ClassFill,

	"maturity":     ClassMaturity,
	"boll_opening": ClassMaturity,
	"ripening":     ClassMaturity,
	"maturation":   ClassMaturity,
}

// Class returns the stage's class, or an empty string when it has none.
func (s Stage) Class() string { return stageClass[s.Key] }

// Susceptibility is how much a calamity costs at a stage, and why.
type Susceptibility struct {
	Calamity string `json:"calamity"`
	Stage    string `json:"stage"`
	Class    string `json:"stage_class"`
	Level    string `json:"level"`

	// Recoverable is the stage's own flag. A crop that can regrow is a
	// different assessment from one that cannot, whatever the severity.
	Recoverable bool `json:"recoverable"`

	// Reason is a sentence an officer can read and a farmer can be told. The
	// point of a table over recalled knowledge is that the reason is fixed
	// and can be argued with.
	Reason string `json:"reason"`

	Provenance string `json:"provenance"`
}

type cell struct {
	level  string
	reason string
}

// The matrix. Calamity, then stage class.
var matrix = map[string]map[string]cell{
	"hailstorm": {
		ClassEstablishment: {SeverityModerate,
			"Seedlings are small targets and a stand can be re-sown if the window allows; loss is real but partly recoverable."},
		ClassVegetative: {SeverityModerate,
			"Leaf area is stripped and stems bruised, but a plant in vegetative growth regrows canopy and may still yield near normally."},
		ClassReproductive: {SeveritySevere,
			"Flowers and developing heads are shed outright, and the plant has no second flowering to compensate with."},
		ClassFill: {SeveritySevere,
			"Grain, pods and bolls are knocked to the ground and the plant cannot refill them; this is where hail costs the most."},
		ClassMaturity: {SeverityHigh,
			"A mature standing crop shatters and is lost on the ground; damage is high but so is the share already harvestable."},
	},
	"unseasonal_rain": {
		ClassEstablishment: {SeverityHigh,
			"Seed rots and a young stand is washed out or capped by crusting soil."},
		ClassVegetative: {SeverityLow,
			"Vegetative growth generally tolerates and often benefits from rain unless water stands."},
		ClassReproductive: {SeverityHigh,
			"Rain during flowering washes pollen and prevents set; the loss is invisible until harvest."},
		ClassFill: {SeverityHigh,
			"Wet grain sprouts in the head and quality is lost even where the crop stands."},
		ClassMaturity: {SeveritySevere,
			"A ready crop discolours, sprouts and rots; this is the classic post-harvest loss the form has a category for."},
	},
	"flood": {
		ClassEstablishment: {SeveritySevere,
			"Submergence kills a young stand within days and the field cannot be re-sown while it holds water."},
		ClassVegetative: {SeverityHigh,
			"Prolonged submergence kills roots; a crop that drains quickly may recover, one that does not will not."},
		ClassReproductive: {SeveritySevere,
			"Submergence at flowering prevents set entirely and the plant cannot flower again."},
		ClassFill: {SeveritySevere,
			"Grain fill stops and the crop lodges into standing water, where it rots rather than dries."},
		ClassMaturity: {SeveritySevere,
			"A mature crop in standing water is a total loss; it cannot be harvested and will not dry."},
	},
	"cyclone": {
		ClassEstablishment: {SeverityModerate,
			"Wind damage to a low young stand is limited, though accompanying rain may wash it out."},
		ClassVegetative: {SeverityModerate,
			"Plants are bent and defoliated but recover where the growing point survives."},
		ClassReproductive: {SeveritySevere,
			"Flowers are stripped and the canopy shredded at the one stage the plant cannot repeat."},
		ClassFill: {SeveritySevere,
			"Lodging at grain fill separates the plant from its water supply and the grain never fills."},
		ClassMaturity: {SeveritySevere,
			"A mature crop is flattened and shattered; what reaches the ground is not recoverable."},
	},
	"cloudburst": {
		ClassEstablishment: {SeveritySevere,
			"A young stand is washed out bodily, along with the seed and the topsoil holding it."},
		ClassVegetative: {SeverityHigh,
			"Soil is scoured from roots and plants are buried in silt where the water slows."},
		ClassReproductive: {SeveritySevere,
			"Physical damage and standing water together at the stage that cannot be repeated."},
		ClassFill: {SeveritySevere,
			"Lodging and inundation at fill; the crop is on the ground and wet."},
		ClassMaturity: {SeveritySevere,
			"A ready crop is lost on the ground."},
	},
	"drought": {
		ClassEstablishment: {SeveritySevere,
			"Without moisture the crop does not establish at all, and the season may be lost rather than the yield."},
		ClassVegetative: {SeverityModerate,
			"Growth stalls and the canopy is smaller, but a plant that survives to a break in the dry spell still yields."},
		ClassReproductive: {SeveritySevere,
			"Moisture stress at flowering aborts set outright; this is the single most costly moment for a dry spell."},
		ClassFill: {SeveritySevere,
			"Grain and pods fill only partially and the harvest is light even where the crop looks intact from the road."},
		ClassMaturity: {SeverityLow,
			"A crop that has reached maturity has already made its yield."},
	},
	"pest_attack": {
		ClassEstablishment: {SeverityHigh,
			"A young stand can be eaten out entirely, and a thin stand cannot be made up later."},
		ClassVegetative: {SeverityModerate,
			"Defoliation is made up by regrowth where the infestation is controlled."},
		ClassReproductive: {SeveritySevere,
			"Flower and bud feeders remove the yield before it forms, and the damage is easy to miss from outside the field."},
		ClassFill: {SeveritySevere,
			"Boll, pod and grain borers destroy the harvested organ directly."},
		ClassMaturity: {SeverityHigh,
			"Stored-grain and late pests reduce quality and weight in a crop otherwise made."},
	},
	"frost": {
		ClassEstablishment: {SeverityHigh,
			"Young tissue has no protection and a stand can be killed outright."},
		ClassVegetative: {SeverityModerate,
			"Leaves are scorched but growing points below the surface often survive."},
		ClassReproductive: {SeveritySevere,
			"Frost at flowering sterilises the flower; nothing sets and there is no second chance."},
		ClassFill: {SeveritySevere,
			"Filling grain is killed and shrivels; the head stands but is empty."},
		ClassMaturity: {SeverityLow,
			"A mature crop is largely past the reach of frost damage."},
	},
	"fire": {
		ClassEstablishment: {SeveritySevere, "Fire removes the crop; there is nothing left to recover."},
		ClassVegetative:    {SeveritySevere, "Fire removes the crop; there is nothing left to recover."},
		ClassReproductive:  {SeveritySevere, "Fire removes the crop; there is nothing left to recover."},
		ClassFill:          {SeveritySevere, "Fire removes the crop; there is nothing left to recover."},
		ClassMaturity:      {SeveritySevere, "Fire removes the crop; a mature crop burns most readily of all."},
	},
	"landslide": {
		ClassEstablishment: {SeveritySevere, "The land itself has moved; the crop and the soil are gone together."},
		ClassVegetative:    {SeveritySevere, "The land itself has moved; the crop and the soil are gone together."},
		ClassReproductive:  {SeveritySevere, "The land itself has moved; the crop and the soil are gone together."},
		ClassFill:          {SeveritySevere, "The land itself has moved; the crop and the soil are gone together."},
		ClassMaturity:      {SeveritySevere, "The land itself has moved; the crop and the soil are gone together."},
	},
}

// exception is a crop that genuinely departs from its class.
type exception struct {
	crop     string
	calamity string
	class    string
	cell     cell
}

var exceptions = []exception{
	{
		crop: "cotton", calamity: "hailstorm", class: ClassFill,
		cell: cell{SeverityHigh,
			"Cotton is indeterminate: bolls lost early in fill are partly made up by continued flowering, which a determinate cereal cannot do. The compensation is real but incomplete, and it needs weeks of season left to happen."},
	},
	{
		crop: "cotton", calamity: "pest_attack", class: ClassFill,
		cell: cell{SeveritySevere,
			"Pink bollworm feeds inside the boll, so the crop looks sound from outside and the loss is only found at picking. Visual assessment understates it."},
	},
	{
		crop: "rice", calamity: "flood", class: ClassVegetative,
		cell: cell{SeverityModerate,
			"Transplanted rice tolerates standing water that would kill an upland crop; only complete submergence over days causes the loss."},
	},
	{
		crop: "sugarcane", calamity: "flood", class: ClassFill,
		cell: cell{SeverityModerate,
			"Cane withstands inundation far better than a grain crop, though prolonged waterlogging costs sugar recovery rather than tonnage."},
	},
	{
		crop: "onion", calamity: "unseasonal_rain", class: ClassMaturity,
		cell: cell{SeveritySevere,
			"Rain on a bulb ready to lift causes rot in the field and in storage; the loss continues after the crop is out of the ground."},
	},
	{
		crop: "grape", calamity: "unseasonal_rain", class: ClassFill,
		cell: cell{SeveritySevere,
			"Rain during berry development cracks the berry and invites downy mildew; a table-grape crop is lost to quality long before it is lost to volume."},
	},
}

// SusceptibilityFor reports what a calamity costs a crop at a stage.
//
// Returns false when nothing is known -- an unmodelled crop, an unmapped
// stage, or a calamity with no row. The caller must say so rather than
// substitute a default, because a stated "moderate" that came from nowhere is
// worse than an admitted gap.
func SusceptibilityFor(crop *Crop, stageKey, calamity string) (Susceptibility, bool) {
	if crop == nil {
		return Susceptibility{}, false
	}
	stageKey = strings.ToLower(strings.TrimSpace(stageKey))
	calamity = strings.ToLower(strings.TrimSpace(calamity))

	class, ok := stageClass[stageKey]
	if !ok {
		return Susceptibility{}, false
	}
	byClass, ok := matrix[calamity]
	if !ok {
		return Susceptibility{}, false
	}
	c, ok := byClass[class]
	if !ok {
		return Susceptibility{}, false
	}

	for _, e := range exceptions {
		if e.crop == crop.Key && e.calamity == calamity && e.class == class {
			c = e.cell
			break
		}
	}

	var recoverable bool
	for _, s := range crop.Stages {
		if s.Key == stageKey {
			recoverable = s.Recoverable
			break
		}
	}

	return Susceptibility{
		Calamity:    calamity,
		Stage:       stageKey,
		Class:       class,
		Level:       c.level,
		Recoverable: recoverable,
		Reason:      c.reason,
		Provenance:  ProvenancePublished,
	}, true
}

// Calamities returns every calamity the matrix covers, so a caller can tell
// whether a gap is a missing row or a misspelled name.
func Calamities() []string {
	out := make([]string, 0, len(matrix))
	for k := range matrix {
		out = append(out, k)
	}
	return out
}
