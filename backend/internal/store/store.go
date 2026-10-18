// Package store persists assessments.
//
// This was an in-memory map while the input was a handful of mock parcels.
// Authorization is what moved it: a Talathi may see only their own village,
// and "which village" has to be something the query filters on. Loading every
// assessment and trimming the slice afterwards is one forgotten early return
// away from a leak, and it does not paginate either.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"strings"
)

// ErrNotFound is returned when no assessment matches -- including when one
// exists but lies outside the caller's jurisdiction.
//
// The two are deliberately indistinguishable. Answering 403 for a record that
// exists elsewhere and 404 for one that does not would let any officer map
// which survey numbers have been assessed across the state.
var ErrNotFound = errors.New("assessment not found")

// Assessment is one stored damage assessment.
type Assessment struct {
	ID           string `json:"id"`
	FarmerID     string `json:"farmer_id"`
	FarmID       string `json:"farm_id"`
	SurveyNumber string `json:"survey_number"`

	// Where the parcel sits, copied at assessment time. A boundary redrawn
	// next year must not change who was allowed to see a claim decided this
	// year.
	VillageLGDCode     string `json:"village_lgd_code"`
	SubDistrictLGDCode string `json:"sub_district_lgd_code,omitempty"`
	DistrictLGDCode    string `json:"district_lgd_code,omitempty"`
	StateLGDCode       string `json:"state_lgd_code,omitempty"`

	SowingDate   string `json:"sowing_date"`
	IncidentDate string `json:"incident_date"`

	// CropName is what the crop was believed to be. On mocked parcels this is
	// synthetic; see mock_files/README.md.
	CropName string `json:"crop_name,omitempty"`

	// OwnerExtentHectares is the owner's share, which is what a loss would be
	// apportioned over -- not the polygon area.
	OwnerExtentHectares float64 `json:"owner_extent_hectares"`

	// Assessment is the agent's judgement after backend verification.
	Assessment assessment.Verified `json:"assessment"`

	// Satellite and Terrain are the deterministic figures the assessment was
	// reasoned over. Stored so a past assessment stays reproducible.
	Satellite assessment.Satellite `json:"satellite"`
	Terrain   assessment.Terrain   `json:"terrain"`

	Triage string `json:"triage_tier"`

	// NDVISeries is every cloud-free observation the figures were derived
	// from. Kept because the anchors alone cannot show where the cloud gaps
	// fall, and a reviewer needs to see a 60-day blind stretch before
	// trusting anything either side of it.
	NDVISeries []satellite.NDVIPoint `json:"ndvi_series"`

	// Images are the report overlays. Held in their own table so that a
	// listing does not drag megabytes of base64 through the pool.
	Images *Images `json:"images,omitempty"`

	// CropLossReportID is the report whose evidence this assessment saw.
	CropLossReportID string `json:"crop_loss_report_id,omitempty"`

	// SurveyEventID is the calamity this assessment was run against.
	//
	// The column and its index have existed since survey events were built
	// and nothing ever wrote them: every assessment in the database has a
	// null event. An assessment is of a parcel *on a date because of a
	// declared calamity*, and an officer who picked an event on the console
	// reasonably expects the result to belong to it.
	SurveyEventID string `json:"survey_event_id,omitempty"`

	// Baseline is what the figure was measured against, and what could not be
	// obtained: prior seasons for this parcel, the surrounding fields, radar,
	// the season's weather, the crop's stage, an officer's counts and the
	// claim history.
	//
	// Stored because an assessment has to be reproducible at an appeal, and a
	// percentage derived against three prior seasons and a corroborated
	// neighbourhood is a different claim from the same percentage derived
	// against nothing. A year later nobody will remember which this was.
	//
	// Untyped for the same reason the wire contract is: the packet's own
	// package owns its shape, and this table should not need a migration
	// every time a source is added to it.
	Baseline json.RawMessage `json:"baseline,omitempty"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Images are the report overlays returned by the satellite engine.
type Images struct {
	NDVIMidDate   *satellite.Image `json:"ndvi_mid_date,omitempty"`
	NDVILastVisit *satellite.Image `json:"ndvi_last_visit,omitempty"`
	TerrainSlope  string           `json:"terrain_slope,omitempty"`
	TerrainAspect string           `json:"terrain_aspect,omitempty"`
}

// Summary is an Assessment without the payloads.
type Summary struct {
	ID              string    `json:"id"`
	FarmerID        string    `json:"farmer_id"`
	FarmID          string    `json:"farm_id"`
	SurveyNumber    string    `json:"survey_number"`
	VillageLGDCode  string    `json:"village_lgd_code"`
	IncidentDate    string    `json:"incident_date"`
	DamagePercent   *float64  `json:"damage_percent"`
	Severity        string    `json:"severity"`
	ConfidenceScore float64   `json:"confidence_score"`
	TriageTier      string    `json:"triage_tier"`
	CropPresence    string    `json:"crop_presence"`
	FraudCount      int       `json:"fraud_count"`
	CriticalFraud   bool      `json:"critical_fraud"`
	CreatedAt       time.Time `json:"created_at"`
}

// Store persists assessments in PostgreSQL.
type Store struct{ pool *db.Pool }

// New builds a store over a connection pool.
func New(pool *db.Pool) *Store { return &Store{pool: pool} }

// Filter narrows a listing. Scope is not optional: every read path takes one,
// so that adding a new one without thinking about jurisdiction does not
// compile.
type Filter struct {
	Scope    rbac.Scope
	FarmerID string
	FarmID   string
	Triage   string
	Page     page.Params
}

// searchPredicate is what a free-text search matches on.
//
// An assessment is looked up by the land it is about, not by a reference:
// a farm id from AgriStack, a survey number off a 7/12 extract, or the crop.
// Substring in every case, which is what migration 0008's trigram indexes
// exist to serve.
const searchPredicate = `(
	$8::text = '' OR
	a.farm_id ILIKE $8 OR
	a.survey_number ILIKE $8 OR
	a.farmer_id ILIKE $8 OR
	COALESCE(a.crop_name, '') ILIKE $8
)`

// Put stores an assessment and returns its id.
func (s *Store) Put(ctx context.Context, a *Assessment) (string, error) {
	verified, err := json.Marshal(a.Assessment)
	if err != nil {
		return "", fmt.Errorf("encoding assessment: %w", err)
	}
	sat, err := json.Marshal(a.Satellite)
	if err != nil {
		return "", fmt.Errorf("encoding satellite: %w", err)
	}
	ter, err := json.Marshal(a.Terrain)
	if err != nil {
		return "", fmt.Errorf("encoding terrain: %w", err)
	}
	series, err := json.Marshal(a.NDVISeries)
	if err != nil {
		return "", fmt.Errorf("encoding ndvi series: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO assessments (
			farmer_id, farm_id, survey_number,
			village_lgd_code, sub_district_lgd_code, district_lgd_code, state_lgd_code,
			sowing_date, incident_date, crop_name, owner_extent_hectares,
			assessment, satellite, terrain, triage_tier, ndvi_series, created_by,
			crop_loss_report_id, crop_presence, confidence, baseline,
			survey_event_id
		) VALUES (
			$1, $2, $3,
			$4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''),
			$8, $9, NULLIF($10, ''), $11,
			$12, $13, $14, $15, $16, $17,
			$18, '', 0, $19,
			$20
		)
		RETURNING id, created_at`,
		a.FarmerID, a.FarmID, a.SurveyNumber,
		a.VillageLGDCode, a.SubDistrictLGDCode, a.DistrictLGDCode, a.StateLGDCode,
		a.SowingDate, a.IncidentDate, a.CropName, a.OwnerExtentHectares,
		verified, sat, ter, a.Triage, series, nullString(a.CreatedBy),
		nullString(a.CropLossReportID), nullJSON(a.Baseline),
		nullString(a.SurveyEventID),
	).Scan(&id, &a.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("inserting assessment: %w", err)
	}

	// crop_presence and confidence are placeholders above: the database
	// trigger derives them from the stored judgement, so the lifted columns
	// cannot disagree with the document they were lifted from.

	if a.Images != nil {
		mid, _ := json.Marshal(a.Images.NDVIMidDate)
		last, _ := json.Marshal(a.Images.NDVILastVisit)
		if _, err := tx.Exec(ctx, `
			INSERT INTO assessment_images (
				assessment_id, ndvi_mid_date, ndvi_last_visit,
				terrain_slope, terrain_aspect
			) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))`,
			id, mid, last, a.Images.TerrainSlope, a.Images.TerrainAspect,
		); err != nil {
			return "", fmt.Errorf("inserting images: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	a.ID = id
	return id, nil
}

// Get returns one assessment, images included, if it lies inside the scope.
func (s *Store) Get(ctx context.Context, id string, scope rbac.Scope) (*Assessment, error) {
	if scope.Empty() {
		return nil, ErrNotFound
	}

	row := s.pool.QueryRow(ctx, `
		SELECT a.id, a.farmer_id, a.farm_id, a.survey_number,
		       a.village_lgd_code, COALESCE(a.sub_district_lgd_code, ''),
		       COALESCE(a.district_lgd_code, ''), COALESCE(a.state_lgd_code, ''),
		       a.sowing_date, a.incident_date, COALESCE(a.crop_name, ''),
		       a.owner_extent_hectares,
		       a.assessment, a.satellite, a.terrain, a.triage_tier,
		       COALESCE(a.ndvi_series, '[]'::jsonb),
		       COALESCE(a.created_by::text, ''), a.created_at,
		       a.baseline, COALESCE(a.crop_loss_report_id::text, ''),
		       i.ndvi_mid_date, i.ndvi_last_visit,
		       COALESCE(i.terrain_slope, ''), COALESCE(i.terrain_aspect, '')
		FROM assessments a
		LEFT JOIN assessment_images i ON i.assessment_id = a.id
		WHERE a.id = $1 AND `+scopePredicate("a", "$2", "$3"),
		id, nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID))

	a, err := scanAssessment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// LatestForClaim returns the newest assessment run on a crop-loss report.
//
// Unscoped, and deliberately: it is only ever called after the caller has
// been allowed to read the report itself, and an assessment belongs to the
// claim it was run on. A farmer holds no jurisdiction, and the farmer a
// handset is acting for by grant is not the account's own -- scoping this a
// second time would refuse the very person the claim belongs to.
func (s *Store) LatestForClaim(ctx context.Context, reportID string) (*Assessment, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT a.id, a.farmer_id, a.farm_id, a.survey_number,
		       a.village_lgd_code, COALESCE(a.sub_district_lgd_code, ''),
		       COALESCE(a.district_lgd_code, ''), COALESCE(a.state_lgd_code, ''),
		       a.sowing_date, a.incident_date, COALESCE(a.crop_name, ''),
		       a.owner_extent_hectares,
		       a.assessment, a.satellite, a.terrain, a.triage_tier,
		       COALESCE(a.ndvi_series, '[]'::jsonb),
		       COALESCE(a.created_by::text, ''), a.created_at,
		       a.baseline, COALESCE(a.crop_loss_report_id::text, ''),
		       i.ndvi_mid_date, i.ndvi_last_visit,
		       COALESCE(i.terrain_slope, ''), COALESCE(i.terrain_aspect, '')
		FROM assessments a
		LEFT JOIN assessment_images i ON i.assessment_id = a.id
		WHERE a.crop_loss_report_id = $1
		ORDER BY a.created_at DESC
		LIMIT 1`, reportID)

	a, err := scanAssessment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// List returns summaries, newest first, restricted to the scope.
func (s *Store) List(ctx context.Context, f Filter) (page.Result[Summary], error) {
	if f.Scope.Empty() {
		return page.Wrap([]Summary{}, 0, f.Page), nil
	}

	jur := nullableUUIDs(f.Scope.JurisdictionIDs)
	farmer := nullString(f.Scope.FarmerID)
	search := ""
	if f.Page.Query != "" {
		search = page.Like(f.Page.Query)
	}

	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM assessments a
		WHERE `+scopePredicate("a", "$1", "$2")+`
		  AND ($3::text = '' OR a.farmer_id = $3)
		  AND ($4::text = '' OR a.farm_id = $4)
		  AND ($5::text = '' OR a.triage_tier = $5)
		  AND `+strings.ReplaceAll(searchPredicate, "$8", "$6"),
		jur, farmer, f.FarmerID, f.FarmID, f.Triage, search).Scan(&total); err != nil {
		return page.Result[Summary]{}, fmt.Errorf("counting assessments: %w", err)
	}
	if total == 0 {
		return page.Wrap([]Summary{}, 0, f.Page), nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.farmer_id, a.farm_id, a.survey_number, a.village_lgd_code,
		       a.incident_date, a.damage_percent,
		       COALESCE(a.assessment ->> 'severity', ''),
		       a.confidence, a.triage_tier, a.crop_presence,
		       CASE WHEN jsonb_typeof(a.assessment -> 'fraud_indicators') = 'array'
		            THEN jsonb_array_length(a.assessment -> 'fraud_indicators')
		            ELSE 0 END,
		       a.critical_fraud, a.created_at
		FROM assessments a
		WHERE `+scopePredicate("a", "$1", "$2")+`
		  AND ($3::text = '' OR a.farmer_id = $3)
		  AND ($4::text = '' OR a.farm_id = $4)
		  AND ($5::text = '' OR a.triage_tier = $5)
		  AND `+searchPredicate+`
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $6 OFFSET $7`,
		jur, farmer, f.FarmerID, f.FarmID, f.Triage,
		f.Page.Limit, f.Page.Offset, search)
	if err != nil {
		return page.Result[Summary]{}, fmt.Errorf("listing assessments: %w", err)
	}
	defer rows.Close()

	out := []Summary{}
	for rows.Next() {
		var s Summary
		var incident time.Time
		if err := rows.Scan(
			&s.ID, &s.FarmerID, &s.FarmID, &s.SurveyNumber, &s.VillageLGDCode,
			&incident, &s.DamagePercent, &s.Severity, &s.ConfidenceScore,
			&s.TriageTier, &s.CropPresence, &s.FraudCount, &s.CriticalFraud,
			&s.CreatedAt,
		); err != nil {
			return page.Result[Summary]{}, err
		}
		s.IncidentDate = incident.Format("2006-01-02")
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return page.Result[Summary]{}, err
	}
	return page.Wrap(out, total, f.Page), nil
}

// scopePredicate is the jurisdiction filter, shared by every read.
//
// Written once and interpolated with placeholder names rather than values:
// two copies of this clause is two chances for one of them to be wrong, and
// the wrong one leaks.
func scopePredicate(alias, jurParam, farmerParam string) string {
	return `(
		(` + farmerParam + `::text IS NOT NULL AND ` + alias + `.farmer_id = ` + farmerParam + `)
		OR (` + jurParam + `::uuid[] IS NOT NULL AND EXISTS (
			SELECT 1 FROM jurisdictions j
			WHERE j.level = 'village' AND j.code = ` + alias + `.village_lgd_code
			  AND (j.id = ANY(` + jurParam + `) OR j.ancestor_ids && ` + jurParam + `)
		))
	)`
}

func scanAssessment(row pgx.Row) (*Assessment, error) {
	var a Assessment
	var sowing, incident time.Time
	var verified, sat, ter, series []byte
	var baseline []byte
	var mid, last []byte
	var slope, aspect string

	if err := row.Scan(
		&a.ID, &a.FarmerID, &a.FarmID, &a.SurveyNumber,
		&a.VillageLGDCode, &a.SubDistrictLGDCode, &a.DistrictLGDCode, &a.StateLGDCode,
		&sowing, &incident, &a.CropName, &a.OwnerExtentHectares,
		&verified, &sat, &ter, &a.Triage, &series,
		&a.CreatedBy, &a.CreatedAt, &baseline, &a.CropLossReportID,
		&mid, &last, &slope, &aspect,
	); err != nil {
		return nil, err
	}

	a.SowingDate = sowing.Format("2006-01-02")
	a.IncidentDate = incident.Format("2006-01-02")

	if err := json.Unmarshal(verified, &a.Assessment); err != nil {
		return nil, fmt.Errorf("decoding assessment: %w", err)
	}
	if err := json.Unmarshal(sat, &a.Satellite); err != nil {
		return nil, fmt.Errorf("decoding satellite: %w", err)
	}
	if err := json.Unmarshal(ter, &a.Terrain); err != nil {
		return nil, fmt.Errorf("decoding terrain: %w", err)
	}
	if len(series) > 0 {
		if err := json.Unmarshal(series, &a.NDVISeries); err != nil {
			return nil, fmt.Errorf("decoding ndvi series: %w", err)
		}
	}
	// Passed through as stored. This is a record of what the assessment stood
	// on, and decoding it into a type here would mean an assessment made
	// before a source existed could no longer be read once it did.
	if len(baseline) > 0 {
		a.Baseline = baseline
	}

	if len(mid) > 0 || len(last) > 0 || slope != "" || aspect != "" {
		imgs := &Images{TerrainSlope: slope, TerrainAspect: aspect}
		if len(mid) > 0 {
			_ = json.Unmarshal(mid, &imgs.NDVIMidDate)
		}
		if len(last) > 0 {
			_ = json.Unmarshal(last, &imgs.NDVILastVisit)
		}
		a.Images = imgs
	}

	return &a, nil
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

// nullJSON keeps an absent packet out of the column as NULL rather than as
// the four bytes "null", which reads as a stored decision that there was no
// baseline rather than as nothing having been recorded.
func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// TerrainFor is the most recent terrain analysis of a parcel.
//
// Used to decide which end of a field the sampling stations should call "low".
// Reported as (value, false) rather than an error when there is nothing to
// find, because there usually is nothing: an assessment may not have run yet,
// and the terrain module is optional. Neither is a fault, and neither should
// stop a farmer walking their field -- station placement takes the absence and
// falls back to the parcel's geometry.
//
// Ordered by incident date rather than insertion: re-running an old assessment
// should not make its terrain the current answer for a parcel.
func (s *Store) TerrainFor(ctx context.Context, farmID string) (assessment.Terrain, bool) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT terrain FROM assessments
		WHERE farm_id = $1
		ORDER BY incident_date DESC, created_at DESC
		LIMIT 1`, farmID).Scan(&raw)
	if err != nil || len(raw) == 0 {
		return assessment.Terrain{}, false
	}

	var out assessment.Terrain
	if err := json.Unmarshal(raw, &out); err != nil {
		return assessment.Terrain{}, false
	}
	return out, true
}
