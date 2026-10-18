package api

import "testing"

// Which loss categories a farmer is offered.
//
// An option a farmer cannot complete must not be on the screen: the server
// refuses a category the event's calamity forbids, and a form that offered it
// anyway would let somebody choose it, walk four points of their field, and be
// refused on submission.

func codes(calamity string) []string {
	var out []string
	for _, o := range lossCategoriesFor(calamity) {
		out = append(out, o.Code)
	}
	return out
}

func TestADroughtIsOnlyEverAStandingCropLoss(t *testing.T) {
	// There is no harvested crop lying in a field to lose to a drought.
	got := codes("drought")
	if len(got) != 1 || got[0] != "standing_crop" {
		t.Errorf("drought offers %v", got)
	}
}

func TestUnseasonalRainIsOnlyEverAPostHarvestLoss(t *testing.T) {
	got := codes("unseasonal_rain")
	if len(got) != 1 || got[0] != "post_harvest" {
		t.Errorf("unseasonal rain offers %v", got)
	}
}

func TestHailOnCutOrStandingCropIsAChoiceTheFarmerMakes(t *testing.T) {
	// Hail ruins both, and only the farmer knows which their field held. This
	// is the case the dropdown exists for.
	got := codes("hailstorm")
	if len(got) != 2 || got[0] != "standing_crop" || got[1] != "post_harvest" {
		t.Errorf("hailstorm offers %v, want both in that order", got)
	}
}

func TestAnUnknownCalamityOffersNothingRatherThanEverything(t *testing.T) {
	// A calamity this build does not know is not a licence to offer both and
	// let the server refuse one of them.
	if got := codes("meteor"); got != nil {
		t.Errorf("an unknown calamity offered %v", got)
	}
	if got := codes(""); got != nil {
		t.Errorf("an event with no calamity offered %v", got)
	}
}

func TestEveryOfferedCategoryIsNamedInBothLanguages(t *testing.T) {
	// The handset renders what this sends. A blank Marathi label would print
	// an empty option in a farmer's own language.
	for _, calamity := range []string{"drought", "hailstorm", "cyclone"} {
		for _, o := range lossCategoriesFor(calamity) {
			if o.Mr == "" || o.En == "" || o.Code == "" {
				t.Errorf("%s offers an incomplete option: %+v", calamity, o)
			}
			if o.Mr == o.En {
				t.Errorf("%s: %q is not translated", calamity, o.En)
			}
		}
	}
}
