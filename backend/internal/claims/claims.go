// Package claims answers what has been claimed here before.
//
// # The gap this closes
//
// Every fact in this package has been in the database since the first report
// was filed, and nothing has ever read it. A parcel claimed on in four
// consecutive years, a farmer whose last three claims were rejected, a lone
// report from a village where nobody else filed against the same event: all
// recorded, none of it reaching the officer deciding the next one.
//
// # What this is for, and what it is emphatically not for
//
// Every pattern here has an innocent explanation and usually has one. Land
// beside a nallah floods most years, so the farmer beside it claims most
// years and is right every time. A village where one person filed may be a
// village where one person has a smartphone and the rest heard nothing. A
// rejected claim may have been rejected because an officer could not find the
// parcel on the day they visited.
//
// So nothing in this package decides anything. It produces signals with the
// innocent reading stated alongside the suspicious one, for a person to weigh.
// A system that routed on these without saying so would be scoring farmers,
// which is a different product and not one anybody asked for.
package claims

import (
	"context"
	"fmt"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Store reads claim history.
type Store struct{ pool *db.Pool }

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// PriorClaim is one earlier report touching the same land or farmer.
type PriorClaim struct {
	Reference    string `json:"reference"`
	CropYear     string `json:"crop_year"`
	Season       string `json:"season"`
	CalamityType string `json:"calamity_type"`
	OccurredAt   string `json:"occurred_at"`
	State        string `json:"state"`

	DeclaredPercent float64  `json:"declared_percent"`
	Outcome         string   `json:"outcome,omitempty"`
	OfficerPercent  *float64 `json:"officer_percent,omitempty"`
}

// ParcelRecord is what this land has been claimed for before.
type ParcelRecord struct {
	Claims []PriorClaim `json:"claims"`

	// DistinctYears matters more than the count: two claims in one year for
	// two calamities is one bad year, and four claims in four years is a
	// pattern.
	DistinctYears int `json:"distinct_years"`

	Confirmed  int `json:"confirmed"`
	Rejected   int `json:"rejected"`
	Unassessed int `json:"unassessed"`
}

// FarmerRecord is what this farmer has claimed, across their land.
type FarmerRecord struct {
	Claims        []PriorClaim `json:"claims"`
	DistinctYears int          `json:"distinct_years"`
	Confirmed     int          `json:"confirmed"`
	Rejected      int          `json:"rejected"`
	Unassessed    int          `json:"unassessed"`
}

// VillageContext is how this claim sits among its neighbours' claims for the
// same event.
//
// The most useful single number in this package. A calamity that damaged one
// field in a village and no other is possible; a calamity that damaged one
// field in a village where three hundred others filed is corroborated by
// three hundred independent people.
type VillageContext struct {
	EventReference string `json:"event_reference"`
	VillageName    string `json:"village_name"`

	ReportsInVillage int `json:"reports_in_village"`
	ReportsInEvent   int `json:"reports_in_event"`

	// MedianPercentInVillage is what everyone else said happened to them.
	// A single claim far above its neighbours' is worth a second look; one
	// sitting among them is ordinary.
	MedianPercentInVillage *float64 `json:"median_percent_in_village,omitempty"`

	ConfirmedInVillage int `json:"confirmed_in_village"`
	RejectedInVillage  int `json:"rejected_in_village"`
}

// Signal is something worth an officer's attention, with both readings.
type Signal struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`

	// Detail is what was found.
	Detail string `json:"detail"`

	// Innocent is the ordinary explanation, stated so that the signal cannot
	// be read as an accusation. Required: a signal without one is a finding
	// dressed as a flag.
	Innocent string `json:"innocent_explanation"`
}

// Severities. Nothing here is ever critical: none of it determines anything.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
)

// History is everything this package knows about a claim's context.
type History struct {
	Parcel  ParcelRecord   `json:"parcel"`
	Farmer  FarmerRecord   `json:"farmer"`
	Village VillageContext `json:"village"`
	Signals []Signal       `json:"signals"`
}

// Lookup is what identifies the claim being assessed.
type Lookup struct {
	ReportID     string
	FarmerID     string
	FarmID       string
	SurveyNumber string
	VillageCode  string
	EventID      string
	// DeclaredPercent is this claim's own figure, for the comparison against
	// the village.
	DeclaredPercent float64
}

// historyLimit bounds what is read and shown.
//
// Ten years of claims is more than an officer will read and more than is
// relevant: land changes hands, and a claim from a decade ago is about a
// different farm.
const historyLimit = 12

// For assembles the context around one claim.
//
// Every part is independent and any part may be unavailable. A failure in one
// does not lose the others, because a partial context is worth more to the
// officer than none.
func (s *Store) For(ctx context.Context, l Lookup) (*History, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("no database")
	}
	h := &History{}

	var errs []string
	if err := s.parcel(ctx, l, &h.Parcel); err != nil {
		errs = append(errs, "parcel history: "+err.Error())
	}
	if err := s.farmer(ctx, l, &h.Farmer); err != nil {
		errs = append(errs, "farmer history: "+err.Error())
	}
	if err := s.village(ctx, l, &h.Village); err != nil {
		errs = append(errs, "village context: "+err.Error())
	}

	h.Signals = signalsFor(l, h)

	if len(errs) > 0 && len(errs) == 3 {
		return h, fmt.Errorf("no claim context could be read: %s",
			strings.Join(errs, "; "))
	}
	return h, nil
}

const claimColumns = `
	r.reference, r.crop_year, r.season::text, r.calamity_type::text,
	to_char(r.occurred_at, 'YYYY-MM-DD'), r.state::text,
	i.damage_percent,
	COALESCE(v.outcome, ''), v.damage_percent`

// latestVerification is the most recent finding on a report, whatever tier it
// came from. A report verified at village level and then arbitrated shows the
// arbitration, which is the one that stands.
const latestVerification = `
	LEFT JOIN LATERAL (
		SELECT outcome, damage_percent
		FROM report_verifications
		WHERE report_id = r.id
		ORDER BY submitted_at DESC
		LIMIT 1
	) v ON true`

func scanClaims(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]PriorClaim, error) {
	defer rows.Close()
	out := []PriorClaim{}
	for rows.Next() {
		var c PriorClaim
		if err := rows.Scan(&c.Reference, &c.CropYear, &c.Season, &c.CalamityType,
			&c.OccurredAt, &c.State, &c.DeclaredPercent,
			&c.Outcome, &c.OfficerPercent); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func tally(claims []PriorClaim) (years, confirmed, rejected, unassessed int) {
	seen := map[string]bool{}
	for _, c := range claims {
		seen[c.CropYear] = true
		switch c.Outcome {
		case "confirmed", "partly_confirmed":
			confirmed++
		case "rejected", "not_traceable":
			rejected++
		default:
			unassessed++
		}
	}
	return len(seen), confirmed, rejected, unassessed
}

func (s *Store) parcel(ctx context.Context, l Lookup, out *ParcelRecord) error {
	out.Claims = []PriorClaim{}
	if l.FarmID == "" && l.SurveyNumber == "" {
		return nil
	}

	// Matched on the registry parcel where one is linked, and on the survey
	// number within the village where none is -- which is the ordinary case
	// while AgriStack returns no geometry for anybody.
	rows, err := s.pool.Query(ctx, `
		SELECT `+claimColumns+`
		FROM crop_loss_report_items i
		JOIN crop_loss_reports r ON r.id = i.report_id`+latestVerification+`
		-- The current report is excluded, and "excluded" has to survive the
		-- lookup having no current report: r.id <> NULL is NULL, not true, so
		-- a bare inequality would filter out every row whenever this is asked
		-- outside an assessment.
		WHERE ($1::uuid IS NULL OR r.id <> $1::uuid)
		  AND (
		        (NULLIF($2, '') IS NOT NULL AND i.farm_id = $2)
		     OR (NULLIF($3, '') IS NOT NULL AND i.survey_number = $3
		         AND r.village_lgd_code = $4)
		      )
		ORDER BY r.occurred_at DESC
		LIMIT $5`,
		nullUUID(l.ReportID), l.FarmID, l.SurveyNumber, l.VillageCode, historyLimit)
	if err != nil {
		return err
	}
	claims, err := scanClaims(rows)
	if err != nil {
		return err
	}
	out.Claims = claims
	out.DistinctYears, out.Confirmed, out.Rejected, out.Unassessed = tally(claims)
	return nil
}

func (s *Store) farmer(ctx context.Context, l Lookup, out *FarmerRecord) error {
	out.Claims = []PriorClaim{}
	if l.FarmerID == "" {
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+claimColumns+`
		FROM crop_loss_reports r
		JOIN LATERAL (
			SELECT damage_percent FROM crop_loss_report_items
			WHERE report_id = r.id ORDER BY damage_percent DESC LIMIT 1
		) i ON true`+latestVerification+`
		WHERE r.farmer_id = $1 AND ($2::uuid IS NULL OR r.id <> $2::uuid)
		ORDER BY r.occurred_at DESC
		LIMIT $3`,
		l.FarmerID, nullUUID(l.ReportID), historyLimit)
	if err != nil {
		return err
	}
	claims, err := scanClaims(rows)
	if err != nil {
		return err
	}
	out.Claims = claims
	out.DistinctYears, out.Confirmed, out.Rejected, out.Unassessed = tally(claims)
	return nil
}

func (s *Store) village(ctx context.Context, l Lookup, out *VillageContext) error {
	if l.EventID == "" || l.VillageCode == "" {
		return nil
	}

	// One query. This runs per assessment and the counts are cheap only
	// because they are counts.
	err := s.pool.QueryRow(ctx, `
		SELECT
			COALESCE(e.reference, ''),
			COALESCE(max(r.village_name) FILTER (
				WHERE r.village_lgd_code = $2), ''),
			count(*) FILTER (WHERE r.village_lgd_code = $2),
			count(*),
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY li.declared
			) FILTER (WHERE r.village_lgd_code = $2),
			count(*) FILTER (
				WHERE r.village_lgd_code = $2
				  AND v.outcome IN ('confirmed', 'partly_confirmed')),
			count(*) FILTER (
				WHERE r.village_lgd_code = $2
				  AND v.outcome IN ('rejected', 'not_traceable'))
		FROM crop_loss_reports r
		JOIN survey_events e ON e.id = r.survey_event_id
		LEFT JOIN LATERAL (
			SELECT max(damage_percent) AS declared
			FROM crop_loss_report_items WHERE report_id = r.id
		) li ON true`+latestVerification+`
		WHERE r.survey_event_id = $1::uuid
		  AND ($3::uuid IS NULL OR r.id <> $3::uuid)
		GROUP BY e.reference`,
		l.EventID, l.VillageCode, nullUUID(l.ReportID)).
		Scan(&out.EventReference, &out.VillageName,
			&out.ReportsInVillage, &out.ReportsInEvent,
			&out.MedianPercentInVillage,
			&out.ConfirmedInVillage, &out.RejectedInVillage)
	if err != nil {
		// No other report against this event is not an error; it is the
		// answer, and a common one early in an event's life.
		if strings.Contains(err.Error(), "no rows") {
			return nil
		}
		return err
	}
	return nil
}

func nullUUID(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
