// Package calibration compares what the farmer said, what the model said,
// and what the officer found.
//
// # Why this exists before it is useful
//
// This platform has no ground truth. Nobody has photographed a damaged field,
// had it assessed by an officer, and recorded both against a measured yield,
// so no damage figure it produces can be shown to be accurate -- only
// defensible. Building that dataset is a field exercise, not a software
// change.
//
// What it does have, from the first day it ran, is three independent opinions
// about the same loss: the farmer's declaration, the panel's assessment, and
// the officer's finding. Those have been joinable since the schema was
// written and nothing has ever read them. In a season they become the only
// calibration set this platform will ever get for free, and its value
// compounds with time -- which is exactly the kind of thing that never gets
// built unless it is built early, while it does nothing.
//
// # The thing this package refuses to do
//
// An officer who saw the model's figure and then wrote a similar one has told
// us nothing about the model. The schema has recorded saw_model_assessment
// since verification was built, precisely for this, and every statistic here
// is reported separately for blind and informed findings. Pooling them would
// produce an agreement rate that improves as the platform shows officers more
// of its own answers, which is the most flattering possible way to be wrong.
package calibration

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Store reads the comparison.
type Store struct{ pool *db.Pool }

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// Sample is one loss, seen three ways.
type Sample struct {
	ReportReference string `json:"report_reference"`
	AssessmentID    string `json:"assessment_id,omitempty"`

	VillageCode string `json:"village_lgd_code"`
	VillageName string `json:"village_name"`
	CropName    string `json:"crop_name,omitempty"`
	Calamity    string `json:"calamity_type"`
	OccurredAt  string `json:"occurred_at"`

	// Declared is the farmer's own figure, weighted across their crop lines.
	Declared float64 `json:"declared_percent"`

	// Modelled is what the panel stated, or nil where it refused to state
	// one. A refusal is a result, not a gap: without ground imagery the
	// correct answer is that no figure can be supported.
	Modelled *float64 `json:"modelled_percent,omitempty"`

	// Officer is the finding that stands, and Outcome is what it was.
	Officer *float64 `json:"officer_percent,omitempty"`
	Outcome string   `json:"outcome,omitempty"`

	// SawModel is whether the officer had the model's figure in front of them
	// when they wrote theirs. The most important field here.
	SawModel bool `json:"officer_saw_model"`

	// Grounded is whether the assessment had a baseline to measure against.
	Grounded bool `json:"assessment_grounded"`
}

// ModelError is the panel's figure less the officer's, in points.
//
// Nil where either is absent. Positive means the model read the loss higher
// than the officer did.
func (s Sample) ModelError() *float64 {
	if s.Modelled == nil || s.Officer == nil {
		return nil
	}
	d := *s.Modelled - *s.Officer
	return &d
}

// FarmerError is the declaration less the officer's finding, in points.
func (s Sample) FarmerError() *float64 {
	if s.Officer == nil {
		return nil
	}
	d := s.Declared - *s.Officer
	return &d
}

// Samples returns every report where an officer has recorded a finding.
//
// Scoped like everything else: a district officer calibrating against their
// own district is the useful case, and a national picture is a different
// permission.
func (s *Store) Samples(ctx context.Context, scope rbac.Scope, limit int) ([]Sample, error) {
	if scope.Empty() {
		return []Sample{}, nil
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.reference,
		       COALESCE(a.id::text, ''),
		       r.village_lgd_code, r.village_name,
		       COALESCE(a.crop_name, ''), r.calamity_type::text,
		       to_char(r.occurred_at, 'YYYY-MM-DD'),
		       -- The farmer's own figure, weighted by the area each line
		       -- claims. A mean of the lines would let a tenth of a hectare
		       -- at 100% outweigh four hectares at 10%.
		       COALESCE(
		           sum(i.area_affected_ha * i.damage_percent)
		             / NULLIF(sum(i.area_sown_ha), 0), 0),
		       a.damage_percent,
		       v.damage_percent, v.outcome, v.saw_model_assessment,
		       (a.baseline IS NOT NULL)
		FROM crop_loss_reports r
		JOIN crop_loss_report_items i ON i.report_id = r.id
		-- The finding that stands: the latest tier to have recorded one.
		JOIN LATERAL (
			SELECT damage_percent, outcome, saw_model_assessment
			FROM report_verifications
			WHERE report_id = r.id
			ORDER BY submitted_at DESC
			LIMIT 1
		) v ON true
		LEFT JOIN LATERAL (
			SELECT id, damage_percent, crop_name, baseline
			FROM assessments
			WHERE crop_loss_report_id = r.id
			ORDER BY created_at DESC
			LIMIT 1
		) a ON true
		WHERE `+scopePredicate+`
		GROUP BY r.id, r.reference, r.village_lgd_code, r.village_name,
		         r.calamity_type, r.occurred_at,
		         a.id, a.damage_percent, a.crop_name, a.baseline,
		         v.damage_percent, v.outcome, v.saw_model_assessment
		ORDER BY r.occurred_at DESC
		LIMIT $3`,
		nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID), limit)
	if err != nil {
		return nil, fmt.Errorf("reading calibration samples: %w", err)
	}
	defer rows.Close()

	out := []Sample{}
	for rows.Next() {
		var x Sample
		if err := rows.Scan(&x.ReportReference, &x.AssessmentID,
			&x.VillageCode, &x.VillageName, &x.CropName, &x.Calamity,
			&x.OccurredAt, &x.Declared, &x.Modelled,
			&x.Officer, &x.Outcome, &x.SawModel, &x.Grounded); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// scopePredicate restricts to the caller's jurisdiction, matching every other
// listing in the platform.
const scopePredicate = `(
	($1::uuid[] IS NOT NULL AND EXISTS (
		SELECT 1 FROM jurisdictions j
		WHERE j.level = 'village' AND j.code = r.village_lgd_code
		  AND (j.id = ANY ($1::uuid[]) OR j.ancestor_ids && $1::uuid[])
	))
	OR ($2::text IS NOT NULL AND r.farmer_id = $2)
)`

// Comparison is how two sets of figures relate.
type Comparison struct {
	N int `json:"n"`

	// MeanError is the average signed difference in points. Positive means
	// the first figure reads the loss higher than the second.
	MeanError float64 `json:"mean_error_points"`
	// MeanAbsError is the average size of the difference, which is the one
	// that says how far off a figure typically is.
	MeanAbsError float64 `json:"mean_absolute_error_points"`
	// MedianAbsError resists the one report where somebody typed 5 for 50.
	MedianAbsError float64 `json:"median_absolute_error_points"`

	// WithinTen is the share of figures inside ten points of each other.
	// Ten because compensation bands are wider than that, so a difference
	// smaller than a band is a difference that changed nothing.
	WithinTen float64 `json:"within_ten_points_percent"`
}

// Summary is the whole picture, split the way it has to be split.
type Summary struct {
	Reports int `json:"reports_with_a_finding"`

	// Blind and Informed are the model against the officer, separated by
	// whether the officer had seen the model's figure. Only Blind says
	// anything about the model.
	Blind    Comparison `json:"model_vs_blind_officer"`
	Informed Comparison `json:"model_vs_informed_officer"`

	// Farmer is the declaration against the officer's finding. Worth knowing
	// on its own: it is the number that decides whether farmers are
	// systematically over- or under-claiming, and it needs no model at all.
	Farmer Comparison `json:"farmer_vs_officer"`

	// Refused is how often the panel declined to state a figure. The
	// behaviour this platform most wants to keep, and the one nothing has
	// been measuring: a drift here is the earliest sign the guardrails have
	// loosened.
	AssessedReports int     `json:"reports_with_an_assessment"`
	Refused         int     `json:"assessments_that_stated_no_figure"`
	RefusedPercent  float64 `json:"refusal_rate_percent"`

	// Grounded is how many assessments had a baseline to measure against.
	Grounded        int     `json:"assessments_with_a_baseline"`
	GroundedPercent float64 `json:"grounded_percent"`

	// Notes are what an honest reader should know before quoting any of the
	// figures above.
	Notes []string `json:"notes"`
}

// minMeaningful is the sample size below which a statistic is arithmetic
// rather than evidence. Twenty is not a lot; below it a single report moves
// the mean by five points.
const minMeaningful = 20

// Summarise computes the comparison over a set of samples.
func Summarise(samples []Sample) Summary {
	s := Summary{Reports: len(samples)}

	var blind, informed, farmer []float64
	for _, x := range samples {
		if x.Modelled != nil || x.AssessmentID != "" {
			s.AssessedReports++
		}
		if x.AssessmentID != "" && x.Modelled == nil {
			s.Refused++
		}
		if x.Grounded {
			s.Grounded++
		}

		if d := x.ModelError(); d != nil {
			if x.SawModel {
				informed = append(informed, *d)
			} else {
				blind = append(blind, *d)
			}
		}
		if d := x.FarmerError(); d != nil {
			farmer = append(farmer, *d)
		}
	}

	s.Blind = compare(blind)
	s.Informed = compare(informed)
	s.Farmer = compare(farmer)

	if s.AssessedReports > 0 {
		s.RefusedPercent = round1(float64(s.Refused) / float64(s.AssessedReports) * 100)
		s.GroundedPercent = round1(float64(s.Grounded) / float64(s.AssessedReports) * 100)
	}

	s.Notes = notesFor(s)
	return s
}

func notesFor(s Summary) []string {
	var out []string

	switch {
	case s.Blind.N == 0:
		out = append(out, "No officer has recorded a finding without first "+
			"seeing the model's figure, so nothing here says anything about "+
			"the model. An officer who saw a figure and wrote a similar one "+
			"has told us only that they read it.")
	case s.Blind.N < minMeaningful:
		out = append(out, fmt.Sprintf(
			"%d blind findings. Below about %d a single report moves the mean "+
				"by several points, so read these as a direction rather than a "+
				"measurement.", s.Blind.N, minMeaningful))
	}

	if s.Informed.N > 0 && s.Blind.N > 0 &&
		s.Informed.MeanAbsError < s.Blind.MeanAbsError-2 {
		out = append(out, fmt.Sprintf(
			"Officers who saw the model's figure agreed with it more closely "+
				"(%.1f points) than officers who did not (%.1f). That is what "+
				"anchoring looks like, and it is a reason to keep showing the "+
				"model's figure after the finding rather than before it.",
			s.Informed.MeanAbsError, s.Blind.MeanAbsError))
	}

	if s.Farmer.N >= minMeaningful {
		switch {
		case s.Farmer.MeanError > 10:
			out = append(out, fmt.Sprintf(
				"Farmers' declarations run %.0f points above officers' findings "+
					"on average. That is a fact about how the form is understood "+
					"as much as about anybody's honesty -- a farmer asked for a "+
					"percentage of a loss they have not been told the denominator "+
					"of will estimate high.", s.Farmer.MeanError))
		case s.Farmer.MeanError < -10:
			out = append(out, fmt.Sprintf(
				"Farmers' declarations run %.0f points below officers' findings. "+
					"Farmers understating their loss is worth acting on: they may "+
					"be underselling what they are entitled to.",
				math.Abs(s.Farmer.MeanError)))
		}
	}

	if s.AssessedReports > 0 && s.RefusedPercent < 1 {
		out = append(out, "The panel stated a figure on almost every "+
			"assessment. Without ground imagery it is supposed to refuse, so a "+
			"refusal rate near zero is worth checking rather than celebrating.")
	}

	if s.AssessedReports > 0 && s.GroundedPercent < 50 {
		out = append(out, fmt.Sprintf(
			"Only %.0f%% of assessments had any baseline to measure against. "+
				"Until that rises, most of these percentages are judgements and "+
				"the comparison above is between two judgements.", s.GroundedPercent))
	}

	if len(out) == 0 {
		out = append(out, "Nothing here needs a caveat, which is unusual "+
			"enough to be worth checking.")
	}
	return out
}

func compare(diffs []float64) Comparison {
	c := Comparison{N: len(diffs)}
	if len(diffs) == 0 {
		return c
	}

	abs := make([]float64, len(diffs))
	var sum, sumAbs, within float64
	for i, d := range diffs {
		sum += d
		abs[i] = math.Abs(d)
		sumAbs += abs[i]
		if abs[i] <= 10 {
			within++
		}
	}
	sort.Float64s(abs)

	c.MeanError = round1(sum / float64(len(diffs)))
	c.MeanAbsError = round1(sumAbs / float64(len(diffs)))
	c.MedianAbsError = round1(median(abs))
	c.WithinTen = round1(within / float64(len(diffs)) * 100)
	return c
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableUUIDs(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}
