package baseline

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Threshold yield: the denominator this platform cannot derive.
//
// # Why it is looked up rather than computed
//
// Every settlement in an Indian crop scheme rests on a threshold yield --
// typically the average of the best five of the last seven years for that
// crop in that notified unit, established by Crop Cutting Experiments. It is
// a statutory figure produced by the Department of Agriculture. A platform
// that derived its own from satellite imagery would be inventing a number
// that looks like the official one and is not, which is worse than having
// none.
//
// # Why the absence is reported rather than filled
//
// Where no figure has been issued, an assessment says so. The alternative --
// quietly substituting a satellite proxy and presenting the result in the
// same units -- produces a percentage an officer would reasonably read as a
// shortfall against threshold yield when it is nothing of the kind.

// ThresholdYield is an issued figure and where it came from.
type ThresholdYield struct {
	Crop     string `json:"crop"`
	Season   string `json:"season"`
	CropYear string `json:"crop_year"`

	YieldKgPerHa float64 `json:"yield_kg_per_ha"`

	// Basis is how the issuing body arrived at it, in their terms. Two
	// districts may use different bases in one year, and an assessment that
	// does not record which cannot be reproduced.
	Basis  string `json:"basis"`
	Source string `json:"source"`

	// Unit names the jurisdiction the figure was issued for, which may be
	// above the parcel's village -- a taluka figure where circles are not
	// mapped, which is most of Maharashtra.
	UnitCode  string `json:"unit_code"`
	UnitName  string `json:"unit_name"`
	UnitLevel string `json:"unit_level"`

	IssuedAt string `json:"issued_at"`
}

// ThresholdYieldFor finds the figure covering a village, if one was issued.
//
// The notified unit is a circle where circles are mapped and a taluka where
// they are not, so the search walks up from the village and takes the most
// specific figure that exists. Taking the least specific would apply a
// district average to a circle that has its own.
func (s *Store) ThresholdYieldFor(
	ctx context.Context, crop, villageCode, season, cropYear string,
) (*ThresholdYield, bool, error) {
	if s.pool == nil {
		return nil, false, errors.New("no database")
	}
	if crop == "" || villageCode == "" {
		return nil, false, nil
	}

	var t ThresholdYield
	err := s.pool.QueryRow(ctx, `
		WITH village AS (
			SELECT id, ancestor_ids
			FROM jurisdictions
			WHERE level = 'village' AND code = $2
		)
		SELECT ty.crop, ty.season, ty.crop_year, ty.yield_kg_per_ha,
		       ty.basis, ty.source,
		       j.code, j.name, j.level::text,
		       to_char(ty.issued_at, 'YYYY-MM-DD')
		FROM threshold_yields ty
		JOIN jurisdictions j ON j.id = ty.jurisdiction_id
		CROSS JOIN village v
		WHERE lower(ty.crop) = lower($1)
		  AND ty.season = $3
		  AND ty.crop_year = $4
		  AND (ty.jurisdiction_id = v.id OR ty.jurisdiction_id = ANY (v.ancestor_ids))
		-- Most specific first: a circle figure beats the taluka one above it.
		ORDER BY array_position(
			ARRAY['village', 'circle', 'sub_district', 'district', 'state'],
			j.level::text)
		LIMIT 1`,
		crop, villageCode, season, cropYear).
		Scan(&t.Crop, &t.Season, &t.CropYear, &t.YieldKgPerHa,
			&t.Basis, &t.Source,
			&t.UnitCode, &t.UnitName, &t.UnitLevel, &t.IssuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the threshold yield: %w", err)
	}
	return &t, true, nil
}

// Denominators is what an assessment had to measure against.
//
// Recorded with the assessment so that a figure can be reproduced during an
// appeal. A percentage derived against three prior seasons and a corroborated
// neighbourhood is a different claim from the same percentage derived against
// nothing, and a year later nobody will remember which this was.
type Denominators struct {
	History *History        `json:"parcel_history,omitempty"`
	Peer    *Peer           `json:"neighbourhood,omitempty"`
	Yield   *ThresholdYield `json:"threshold_yield,omitempty"`

	// Absent names each denominator that could not be obtained, and why. It
	// is the field an officer should read first: an assessment with all three
	// absent is a judgement, and should be read as one.
	Absent []string `json:"absent,omitempty"`
}

// Grounded reports whether the figure rests on anything outside the claim
// itself.
func (d *Denominators) Grounded() bool {
	if d == nil {
		return false
	}
	if d.Yield != nil {
		return true
	}
	if d.History != nil && d.History.Verdict != VerdictUnknown {
		return true
	}
	if d.Peer != nil && d.Peer.Verdict != PeerInconclusive {
		return true
	}
	return false
}

// Summary is one sentence for an officer, before any detail.
func (d *Denominators) Summary() string {
	if d == nil || !d.Grounded() {
		return "This assessment has no baseline: the percentage is not measured " +
			"against what this land normally does, what the surrounding fields " +
			"did, or an issued threshold yield."
	}

	var parts []string
	if d.History != nil {
		switch d.History.Verdict {
		case VerdictBelowNormal:
			parts = append(parts, "the canopy stayed below what this parcel usually reaches")
		case VerdictAboveNormal:
			parts = append(parts, "the canopy was better than this parcel usually reaches")
		case VerdictNormal:
			parts = append(parts, "the canopy reached about what this parcel usually reaches")
		}
	}
	if d.Peer != nil {
		switch d.Peer.Verdict {
		case PeerRegional:
			parts = append(parts, "the surrounding fields fell too")
		case PeerLocal:
			parts = append(parts, "the surrounding fields did not fall")
		case PeerContradicted:
			parts = append(parts, "the surrounding fields fell and this one did not")
		case PeerNoLoss:
			parts = append(parts, "no fall is visible on this parcel or around it")
		}
	}
	if d.Yield != nil {
		parts = append(parts, fmt.Sprintf(
			"an issued threshold yield of %.0f kg per hectare applies (%s)",
			d.Yield.YieldKgPerHa, d.Yield.UnitName))
	}

	switch len(parts) {
	case 0:
		return "No baseline was available."
	case 1:
		return "Measured against: " + parts[0] + "."
	default:
		out := "Measured against: "
		for i, p := range parts {
			switch {
			case i == 0:
				out += p
			case i == len(parts)-1:
				out += ", and " + p
			default:
				out += ", " + p
			}
		}
		return out + "."
	}
}
