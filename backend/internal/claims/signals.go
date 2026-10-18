package claims

import (
	"fmt"
	"math"
)

// Turning the history into things worth an officer's attention.
//
// # The rule every signal here obeys
//
// A signal carries the innocent explanation next to the suspicious one, and
// the struct will not let it be built otherwise. This is not politeness. The
// patterns in this package are weak evidence individually and every one of
// them has a common lawful cause: land beside a nallah floods most years, a
// village with one report may be a village with one smartphone, and a claim
// rejected as "not traceable" says the officer could not find the parcel, not
// that the farmer invented it.
//
// An officer reading a flag with no counter-reading will discount the farmer,
// and the flags will be right often enough that the habit sticks. Writing the
// innocent reading into the record is the only mechanism that survives
// people being busy.

// Signal codes.
const (
	SignalRepeatParcelClaims  = "repeat_claims_same_parcel"
	SignalPriorRejections     = "prior_claims_rejected"
	SignalLoneClaimInVillage  = "lone_claim_in_village"
	SignalWellAboveNeighbours = "figure_above_village_median"
	SignalWellBelowNeighbours = "figure_below_village_median"
	SignalFirstClaim          = "no_prior_claims"
	SignalCorroborated        = "corroborated_by_neighbours"
)

// Thresholds, named so they can be argued with rather than found in a
// conditional.
const (
	// repeatYears is how many separate years of claims on one parcel starts
	// being a pattern rather than a run of bad luck.
	repeatYears = 3

	// corroboratingReports is how many independent filings from one village
	// against one event make the event itself hard to doubt.
	corroboratingReports = 5

	// medianGapPoints is how far a figure has to sit from its neighbours'
	// before the distance is worth remarking on. Twenty-five points is wide
	// on purpose: adjacent fields genuinely differ, and a narrow band would
	// flag most of a village.
	medianGapPoints = 25
)

func signalsFor(l Lookup, h *History) []Signal {
	var out []Signal

	if h.Parcel.DistinctYears >= repeatYears {
		out = append(out, Signal{
			Code:     SignalRepeatParcelClaims,
			Severity: SeverityWarning,
			Detail: fmt.Sprintf(
				"this parcel has been claimed on in %d separate years (%d reports on record)",
				h.Parcel.DistinctYears, len(h.Parcel.Claims)),
			Innocent: "land that floods, or sits where hail collects, is damaged " +
				"most years. A parcel with a long claim history may simply be a " +
				"parcel in a bad place, and its owner may have been right every time.",
		})
	}

	// Rejections are only worth raising when they outnumber confirmations:
	// one rejection among several confirmations says nothing.
	if h.Farmer.Rejected >= 2 && h.Farmer.Rejected > h.Farmer.Confirmed {
		out = append(out, Signal{
			Code:     SignalPriorRejections,
			Severity: SeverityWarning,
			Detail: fmt.Sprintf(
				"%d of this farmer's earlier claims were rejected or untraceable, "+
					"against %d confirmed", h.Farmer.Rejected, h.Farmer.Confirmed),
			Innocent: "\"not traceable\" means an officer could not find the parcel " +
				"or the farmer on the day they visited, which is a record-keeping " +
				"failure as often as anything else. A rejection is also the " +
				"ordinary outcome where a loss was real but not caused by the " +
				"calamity that was notified.",
		})
	}

	switch {
	case h.Village.ReportsInVillage >= corroboratingReports:
		out = append(out, Signal{
			Code:     SignalCorroborated,
			Severity: SeverityInfo,
			Detail: fmt.Sprintf(
				"%d other reports were filed from %s against the same event",
				h.Village.ReportsInVillage, villageName(h)),
			Innocent: "independent corroboration: the event is attested by people " +
				"who do not control each other's fields.",
		})
	case h.Village.ReportsInEvent > 0 && h.Village.ReportsInVillage == 0:
		out = append(out, Signal{
			Code:     SignalLoneClaimInVillage,
			Severity: SeverityInfo,
			Detail: fmt.Sprintf(
				"no other report was filed from this village against %s, though "+
					"%d were filed elsewhere under it",
				h.Village.EventReference, h.Village.ReportsInEvent),
			Innocent: "a village where one person filed is very often a village " +
				"where one person has a handset and heard about the notification. " +
				"Being first is not being alone.",
		})
	}

	if m := h.Village.MedianPercentInVillage; m != nil && h.Village.ReportsInVillage >= 3 {
		gap := l.DeclaredPercent - *m
		switch {
		case gap >= medianGapPoints:
			out = append(out, Signal{
				Code:     SignalWellAboveNeighbours,
				Severity: SeverityWarning,
				Detail: fmt.Sprintf(
					"this claim states %.0f%% where the village median for the same "+
						"event is %.0f%%", l.DeclaredPercent, *m),
				Innocent: "damage is genuinely uneven across a village -- a field " +
					"at the bottom of a slope, or in the path of a hail swathe, " +
					"loses far more than its neighbours. The figure is a reason to " +
					"look at this field, not a reason to doubt it.",
			})
		case gap <= -medianGapPoints:
			out = append(out, Signal{
				Code:     SignalWellBelowNeighbours,
				Severity: SeverityInfo,
				Detail: fmt.Sprintf(
					"this claim states %.0f%%, well below the village median of %.0f%% "+
						"for the same event", l.DeclaredPercent, *m),
				Innocent: "a farmer understating their loss is the common case here, " +
					"not a suspicious one. It is recorded so that an officer can " +
					"check the farmer has not undersold what they are entitled to.",
			})
		}
	}

	if len(h.Parcel.Claims) == 0 && len(h.Farmer.Claims) == 0 {
		out = append(out, Signal{
			Code:     SignalFirstClaim,
			Severity: SeverityInfo,
			Detail:   "this is the first claim on record for this farmer and this parcel",
			Innocent: "the platform is new. Almost every claim will be a first " +
				"claim for some time, and this says nothing about the farmer.",
		})
	}

	return out
}

func villageName(h *History) string {
	if h.Village.VillageName != "" {
		return h.Village.VillageName
	}
	return "this village"
}

// Attention reports whether anything here is worth a second look.
//
// Deliberately not a score. A count of warnings is a number somebody will
// eventually sort by, and sorting farmers by suspicion is a different product
// from the one this is.
func (h *History) Attention() bool {
	for _, s := range h.Signals {
		if s.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

// Summary is one line for the top of an officer's screen.
func (h *History) Summary() string {
	switch {
	case h == nil:
		return "No claim history was available."
	case len(h.Parcel.Claims) == 0 && len(h.Farmer.Claims) == 0 &&
		h.Village.ReportsInVillage == 0:
		return "Nothing on record: no earlier claim for this farmer or this " +
			"parcel, and no other report from this village against this event."
	}

	s := fmt.Sprintf(
		"%d earlier claim(s) on this parcel, %d by this farmer",
		len(h.Parcel.Claims), len(h.Farmer.Claims))
	if h.Village.ReportsInVillage > 0 {
		s += fmt.Sprintf("; %d other report(s) from %s against the same event",
			h.Village.ReportsInVillage, villageName(h))
	}
	if m := h.Village.MedianPercentInVillage; m != nil {
		s += fmt.Sprintf("; the village median for this event is %.0f%%",
			math.Round(*m))
	}
	return s + "."
}
