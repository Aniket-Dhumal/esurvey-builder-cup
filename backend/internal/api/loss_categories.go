package api

import (
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Which loss categories a claim against an event may be filed under.
//
// # Why this is narrowed at all
//
// The two categories are not two labels for the same thing. A standing crop
// loss is measured on a crop still growing; a post-harvest loss is measured on
// a cut crop lying in the field, and only a few calamities can cause one --
// report.CalamitiesFor holds that rule and the server enforces it.
//
// Until now the handset hardcoded standing_crop for every claim, so a farmer
// whose harvested crop was ruined by unseasonal rain had no way to say so. The
// fix is not simply to offer both: offering a category the event's calamity
// forbids means a farmer chooses it, walks four points of their field, and is
// refused on submission. So the event carries the categories it actually
// permits and the form renders those.

// lossCategoryNames is how each category prints, in both languages.
//
// The same strings the console's form options endpoint serves, from one place
// so a category cannot be called two different things in Marathi.
var lossCategoryNames = map[report.LossCategory]event.LossCategoryOption{
	report.StandingCrop: {
		Code: string(report.StandingCrop),
		Mr:   "स्थानिक नैसर्गिक आपत्ती",
		En:   "Localised natural calamity",
	},
	report.PostHarvest: {
		Code: string(report.PostHarvest),
		Mr:   "काढणी पश्चात नुकसान",
		En:   "Post-harvest loss",
	},
}

// lossCategoriesFor returns the categories whose calamity list contains this
// event's calamity.
//
// Order is fixed rather than map order, so the form does not reshuffle itself
// between two fetches of the same event.
func lossCategoriesFor(calamity string) []event.LossCategoryOption {
	if calamity == "" {
		return nil
	}
	var out []event.LossCategoryOption
	for _, c := range []report.LossCategory{report.StandingCrop, report.PostHarvest} {
		if report.ValidCalamity(c, calamity) {
			out = append(out, lossCategoryNames[c])
		}
	}
	return out
}

// withLossCategories fills in what each event permits.
func withLossCategories(events []*event.Event) {
	for _, ev := range events {
		if ev == nil {
			continue
		}
		ev.LossCategories = lossCategoriesFor(ev.CalamityType)
	}
}
