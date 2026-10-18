package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// AdjudicationEntry is one report waiting on an adjudicator.
//
// # Why this queue has to exist
//
// The platform could already adjudicate a report and could already show one
// once you knew its reference. What it could not do is answer "which reports
// are waiting on me" for the officer whose job that is. An arbitration path
// nobody can find is an arbitration path that does not run: reports sit in
// verified, the farmer hears nothing, and the only way to notice is for
// someone to open reports one at a time.
type AdjudicationEntry struct {
	ReportID   string `json:"report_id"`
	Reference  string `json:"reference"`
	FarmerName string `json:"farmer_name"`

	VillageName    string    `json:"village_name"`
	VillageLGDCode string    `json:"village_lgd_code"`
	CalamityType   string    `json:"calamity_type"`
	OccurredAt     time.Time `json:"occurred_at"`
	State          string    `json:"state"`

	// The two findings that must be reconciled, and how far apart they are.
	// Null where only one officer has filed: that report is waiting on the
	// second verifier, not on an adjudicator, and the queue says which.
	FirstPercent  *float64 `json:"first_percent"`
	SecondPercent *float64 `json:"second_percent"`
	Divergence    *float64 `json:"divergence"`

	// Findings filed so far, of the two expected.
	FindingsFiled int `json:"findings_filed"`

	// Route is what Reconcile would decide on these numbers. Computed here so
	// the queue can be sorted by what actually needs a human, rather than
	// making an officer open each one to find out.
	Route Route `json:"route"`

	// Rejected counts findings that rejected the claim outright. A rejection
	// is not a number to average, so a report carrying one always reaches an
	// adjudicator regardless of divergence.
	Rejected int `json:"rejected"`

	// WaitingSince is when the last finding landed. What the queue sorts by:
	// the farmer has been waiting since then.
	WaitingSince time.Time `json:"waiting_since"`
	WaitingDays  int       `json:"waiting_days"`
}

// AdjudicationQueue lists reports needing arbitration inside a scope.
//
// Scoped like every other read. An adjudicator sees their own district's
// disagreements and nobody else's.
func (s *Store) AdjudicationQueue(
	ctx context.Context, scope rbac.Scope, p page.Params,
) (page.Result[AdjudicationEntry], error) {
	if scope.Empty() {
		return page.Wrap([]AdjudicationEntry{}, 0, p), nil
	}

	jur := nullableUUIDs(scope.JurisdictionIDs)
	farmer := nullString(scope.FarmerID)
	search := ""
	if p.Query != "" {
		search = page.Like(p.Query)
	}

	const filed = `filed AS (
			SELECT v.report_id,
			       count(*) AS n,
			       count(*) FILTER (WHERE v.outcome = 'rejected') AS rejected,
			       min(v.damage_percent) AS lo,
			       max(v.damage_percent) AS hi,
			       max(v.submitted_at) AS last_at
			FROM report_verifications v
			GROUP BY v.report_id
		)`

	var total int
	if err := s.pool.QueryRow(ctx, `WITH `+filed+`
		SELECT count(*) FROM crop_loss_reports r
		JOIN filed ON filed.report_id = r.id
		WHERE `+scopePredicate("$1", "$2")+`
		  AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')
		  AND NOT EXISTS (SELECT 1 FROM report_adjudications a WHERE a.report_id = r.id)
		  AND ($3::text = '' OR r.reference ILIKE $3 OR r.farmer_name ILIKE $3
		                     OR r.village_name ILIKE $3)`,
		jur, farmer, search).Scan(&total); err != nil {
		return page.Result[AdjudicationEntry]{}, fmt.Errorf("counting the adjudication queue: %w", err)
	}
	if total == 0 {
		return page.Wrap([]AdjudicationEntry{}, 0, p), nil
	}

	rows, err := s.pool.Query(ctx, `
		WITH `+filed+`
		SELECT r.id, r.reference, r.farmer_name,
		       r.village_name, r.village_lgd_code,
		       r.calamity_type::text, r.occurred_at, r.state,
		       filed.lo, filed.hi, filed.n, filed.rejected, filed.last_at
		FROM crop_loss_reports r
		JOIN filed ON filed.report_id = r.id
		WHERE `+scopePredicate("$1", "$2")+`
		  -- Already adjudicated, closed or withdrawn is not a queue entry.
		  AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')
		  AND NOT EXISTS (
		      SELECT 1 FROM report_adjudications a WHERE a.report_id = r.id
		  )
		  AND ($3::text = '' OR r.reference ILIKE $3 OR r.farmer_name ILIKE $3
		                     OR r.village_name ILIKE $3)
		ORDER BY filed.last_at, r.id
		LIMIT $4 OFFSET $5`,
		jur, farmer, search, p.Limit, p.Offset)
	if err != nil {
		return page.Result[AdjudicationEntry]{}, fmt.Errorf("reading the adjudication queue: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	out := []AdjudicationEntry{}
	for rows.Next() {
		var e AdjudicationEntry
		var lo, hi *float64
		if err := rows.Scan(&e.ReportID, &e.Reference, &e.FarmerName,
			&e.VillageName, &e.VillageLGDCode, &e.CalamityType,
			&e.OccurredAt, &e.State,
			&lo, &hi, &e.FindingsFiled, &e.Rejected, &e.WaitingSince); err != nil {
			return page.Result[AdjudicationEntry]{}, err
		}

		e.FirstPercent, e.SecondPercent = lo, hi
		if lo != nil && hi != nil && e.FindingsFiled >= 2 {
			d := *hi - *lo
			e.Divergence = &d
		}
		e.Route = routeFor(e)
		e.WaitingDays = int(now.Sub(e.WaitingSince).Hours() / 24)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return page.Result[AdjudicationEntry]{}, err
	}
	return page.Wrap(out, total, p), nil
}

// routeFor is what Reconcile would decide on these numbers.
//
// The thresholds live in one place -- the constants Reconcile uses -- so this
// cannot drift from the decision the adjudicator will actually be shown when
// they open the report.
func routeFor(e AdjudicationEntry) Route {
	switch {
	case e.Rejected > 0:
		// A rejection is not a number to average.
		return RouteArbitration
	case e.FindingsFiled < 2 || e.Divergence == nil:
		// Waiting on the second verifier, not on an adjudicator.
		return RouteConsensus
	case *e.Divergence >= ArbitrationThreshold:
		return RouteEscalation
	case *e.Divergence > ConsensusThreshold:
		return RouteArbitration
	default:
		return RouteConsensus
	}
}
