package fieldwork

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Store persists quadrat counts.
type Store struct{ pool *db.Pool }

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// Errors.
var (
	ErrNothingMeasured = errors.New("a measurement has to measure something")
	ErrBadQuadrat      = errors.New("invalid quadrat")
)

// Record writes one quadrat.
//
// Validated here as well as in the schema. The database catches an inverted
// count whatever writes it, which is where that rule belongs; this catches it
// with a sentence an officer standing in a field can act on, which a check
// constraint cannot produce.
func (s *Store) Record(ctx context.Context, m *Measurement) (string, error) {
	if m.QuadratSideM <= 0 || m.QuadratSideM > 10 {
		return "", fmt.Errorf("%w: the frame was %.2f m across",
			ErrBadQuadrat, m.QuadratSideM)
	}
	if m.PlantsPresent == nil && m.OrganSampled == nil {
		return "", ErrNothingMeasured
	}
	if m.PlantsPresent != nil && m.PlantsLodged != nil && *m.PlantsLodged > *m.PlantsPresent {
		return "", fmt.Errorf(
			"%w: %d plants lodged of %d standing; a lodged plant is one of the "+
				"plants that are there", ErrBadQuadrat, *m.PlantsLodged, *m.PlantsPresent)
	}
	if m.OrganSampled != nil && m.OrganDamaged != nil && *m.OrganDamaged > *m.OrganSampled {
		return "", fmt.Errorf(
			"%w: %d damaged of %d sampled", ErrBadQuadrat, *m.OrganDamaged, *m.OrganSampled)
	}

	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO field_measurements (
			report_id, line_no, recorded_by, quadrat_side_m,
			plants_expected, plants_present, plants_lodged,
			organ_sampled, organ_damaged, soil_type, notes, capture_id
		) VALUES (
			$1::uuid, NULLIF($2, 0), $3::uuid, $4,
			$5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''),
			NULLIF($12, '')::uuid
		) RETURNING id::text`,
		m.ReportID, m.LineNo, m.RecordedBy, m.QuadratSideM,
		m.PlantsExpected, m.PlantsPresent, m.PlantsLodged,
		m.OrganSampled, m.OrganDamaged, m.SoilType, m.Notes, m.CaptureID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("recording a measurement: %w", err)
	}
	return id, nil
}

// ForReport returns every quadrat on a report, newest last.
//
// Ordered oldest first on purpose: quadrats are read as a sequence of frames
// across a field, and reversing that makes the walk harder to follow.
func (s *Store) ForReport(ctx context.Context, reportID string) ([]*Measurement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.report_id::text, COALESCE(m.line_no, 0),
		       m.recorded_by::text, COALESCE(u.full_name, ''),
		       to_char(m.recorded_at, 'YYYY-MM-DD"T"HH24:MI:SSZ'),
		       m.quadrat_side_m,
		       m.plants_expected, m.plants_present, m.plants_lodged,
		       m.organ_sampled, m.organ_damaged,
		       COALESCE(m.soil_type, ''), COALESCE(m.notes, ''),
		       COALESCE(m.capture_id::text, '')
		FROM field_measurements m
		LEFT JOIN users u ON u.id = m.recorded_by
		WHERE m.report_id = $1::uuid
		ORDER BY m.recorded_at, m.id`, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading measurements: %w", err)
	}
	defer rows.Close()

	out := []*Measurement{}
	for rows.Next() {
		var m Measurement
		if err := rows.Scan(&m.ID, &m.ReportID, &m.LineNo,
			&m.RecordedBy, &m.RecordedName, &m.RecordedAt, &m.QuadratSideM,
			&m.PlantsExpected, &m.PlantsPresent, &m.PlantsLodged,
			&m.OrganSampled, &m.OrganDamaged,
			&m.SoilType, &m.Notes, &m.CaptureID); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ForLine filters to one crop line, including measurements taken for the
// report as a whole.
//
// A whole-report quadrat counts towards every line: an officer who measured
// once on a single-crop report measured that crop, and dropping it because
// the line number is null would lose the only counts they took.
func (s *Store) ForLine(
	ctx context.Context, reportID string, lineNo int,
) ([]*Measurement, error) {
	all, err := s.ForReport(ctx, reportID)
	if err != nil {
		return nil, err
	}
	out := make([]*Measurement, 0, len(all))
	for _, m := range all {
		if m.LineNo == 0 || m.LineNo == lineNo {
			out = append(out, m)
		}
	}
	return out, nil
}

// MarkVerificationMeasured records that a finding rests on counts.
//
// The point of the whole table: an adjudicator facing two officers who
// disagree can see that one of them counted, which is a better basis for
// preferring a finding than which of them is senior.
func (s *Store) MarkVerificationMeasured(
	ctx context.Context, q db.Querier, reportID, officerID string,
) error {
	if q == nil {
		q = s.pool
	}
	// Set on the officer's own finding only. One officer's counts do not make
	// their colleague's independent finding a measured one.
	_, err := q.Exec(ctx, `
		UPDATE report_verifications
		SET measurements_taken = true
		WHERE report_id = $1::uuid AND officer_id = $2::uuid`,
		reportID, officerID)
	if err != nil {
		return fmt.Errorf("marking a verification as measured: %w", err)
	}
	return nil
}

// ExpectedStand suggests a plant count for a quadrat from a row spacing.
//
// A convenience for the form, not a source of truth: the officer confirms or
// overrides it, and what is stored is what they confirmed. Spacings vary by
// variety and by what the farmer actually did, and a figure the system
// asserted would be a denominator nobody chose.
func ExpectedStand(rowCM, plantCM, quadratSideM float64) (int, error) {
	if rowCM <= 0 || plantCM <= 0 || quadratSideM <= 0 {
		return 0, fmt.Errorf("%w: spacing %gcm x %gcm over a %gm frame",
			ErrBadQuadrat, rowCM, plantCM, quadratSideM)
	}
	sideCM := quadratSideM * 100
	rows := sideCM / rowCM
	perRow := sideCM / plantCM
	return int(rows * perRow), nil
}

// SoilTypes are the classes an officer picks from.
//
// A closed list because free text here becomes twelve spellings of "black
// cotton soil" and nothing can be aggregated. Marathi first: the person
// choosing is standing in the field.
var SoilTypes = []struct{ Key, Mr, En string }{
	{"black", "काळी", "Black cotton"},
	{"medium_black", "मध्यम काळी", "Medium black"},
	{"shallow_black", "उथळ काळी", "Shallow black"},
	{"red", "तांबडी", "Red"},
	{"laterite", "जांभी", "Laterite"},
	{"alluvial", "गाळाची", "Alluvial"},
	{"sandy", "वाळूमिश्रित", "Sandy"},
	{"saline", "क्षारपड", "Saline or alkaline"},
}

// ValidSoilType reports whether a key is one of the classes above.
func ValidSoilType(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return true // not recorded is allowed; a wrong one is not
	}
	for _, s := range SoilTypes {
		if s.Key == k {
			return true
		}
	}
	return false
}
