package event

import "strings"

// Does the crop on the farmer's registry record match the event?
//
// # Why a mismatch does not stop the survey
//
// It would be tidier if it did. A hailstorm declared over soybean and cotton
// has nothing to say about a sugarcane field, and letting that farmer through
// costs an officer a visit.
//
// It is refused as a design because of what the registry actually contains.
// AgriStack returns an empty crop_sown_data for every farmer this platform has
// been able to reach live, and where it does hold a crop it is the last season
// somebody entered rather than the one standing in the field. Refusing on a
// mismatch would therefore lock out the genuine claimant far more often than
// it would catch anybody, and it would do so silently, in an app, with no
// person to appeal to.
//
// So a mismatch is a discrepancy rather than a verdict: the survey proceeds,
// the report carries the flag, and the Talathi and the Krushi Sahayak settle
// it standing in the field — which is the only place it can honestly be
// settled, because they can see what is growing.
//
// # Why silence is treated differently from contradiction
//
// "The registry says nothing" and "the registry says something else" are
// different facts about a claim, and collapsing them would throw away the
// distinction an officer most needs. Both let the farmer through; only one is
// a disagreement.

// CropCheck is what the registry said about a parcel's crop, against the event.
type CropCheck struct {
	// Matched is true when the registry names a crop the event covers.
	Matched bool `json:"matched"`

	// Mismatch is true only for a genuine contradiction: the registry names a
	// crop and the event does not cover it.
	Mismatch bool `json:"mismatch"`

	// NoRegistryCrop is true when the registry holds no crop for this parcel.
	// The ordinary case on live AgriStack data.
	NoRegistryCrop bool `json:"no_registry_crop"`

	// RegistryCrop is what the registry said, for the officer to read.
	RegistryCrop string `json:"registry_crop,omitempty"`

	// EventCrops names what the event covers, empty when it covers all.
	EventCrops []string `json:"event_crops,omitempty"`
}

// Blocks reports whether this check should stop the survey.
//
// Always false, and a method rather than an absence so the decision is
// somewhere a reader can find it and argue with it. If a deployment ever gets
// a registry it can trust, this is the one line that changes.
func (c CropCheck) Blocks() bool { return false }

// MatchCrop compares a parcel's registry crop against the event's crops.
//
// registryCode and registryName are what crop_sown_data held for the parcel;
// either may be empty, which is the common case.
func MatchCrop(eventCrops []Crop, registryCode, registryName string) CropCheck {
	out := CropCheck{RegistryCrop: strings.TrimSpace(registryName)}
	for _, c := range eventCrops {
		out.EventCrops = append(out.EventCrops, c.Name)
	}

	code := strings.TrimSpace(registryCode)
	name := strings.TrimSpace(registryName)

	if code == "" && name == "" {
		out.NoRegistryCrop = true
		return out
	}

	// An event that names no crops covers all of them, so anything the
	// registry holds matches.
	if len(eventCrops) == 0 {
		out.Matched = true
		return out
	}

	for _, c := range eventCrops {
		// Code first: it is the identifier both systems agree on. The name is
		// a fallback for a registry record that carries one without the other,
		// compared case-insensitively because the two systems disagree about
		// capitalisation more often than about crops.
		if code != "" && strings.EqualFold(strings.TrimSpace(c.Code), code) {
			out.Matched = true
			return out
		}
		if name != "" && strings.EqualFold(strings.TrimSpace(c.Name), name) {
			out.Matched = true
			return out
		}
	}

	out.Mismatch = true
	return out
}
