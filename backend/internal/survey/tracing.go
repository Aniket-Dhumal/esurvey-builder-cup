package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Walking a plot boundary.
//
// # Why this is the piece everything else waited on
//
// AgriStack returns no geometry for any farmer this platform can reach.
// Without a boundary the assessment refuses outright, the geofence on every
// photograph has nothing to check against, and the prior-season and
// neighbourhood baselines have no parcel to read. Every analysis built on
// this platform stands on a field boundary that no live farmer had.
//
// A farmer with a handset can walk their field in five minutes. That is the
// whole idea; the rest of this file is about doing it honestly.
//
// # What honestly means here
//
// A phone's position is uncertain by metres, and a boundary drawn from it is
// uncertain by the same. So the worst accuracy at any corner is kept and
// reported rather than averaged away, the walk itself is stored beside the
// polygon, and the area it implies is compared with the extent the registry
// holds without either being corrected to match the other. A jointly held
// survey number and a walk that cut a corner look identical from orbit, and
// choosing between them is not this code's business.

// TracePoint is one corner of a walk.
type TracePoint struct {
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	AccuracyM float64   `json:"accuracy_m"`
	At        time.Time `json:"at"`
}

// Trace is a completed walk of one parcel.
type Trace struct {
	FarmID         string
	FarmerID       string
	SurveyNumber   string
	VillageLGDCode string
	TracedBy       string

	Points []TracePoint

	// RegistryExtentHa is what AgriStack holds for this survey number, copied
	// so a later disagreement can be seen without re-reading a registry that
	// may have changed.
	RegistryExtentHa float64
}

// Errors a farmer's handset can cause, each with something they can do.
var (
	ErrTooFewPoints  = errors.New("a boundary needs at least three corners")
	ErrTooCoarse     = errors.New("the position was too uncertain to draw a boundary")
	ErrNotAPolygon   = errors.New("those points do not enclose an area")
	ErrImplausible   = errors.New("the area enclosed is not a plausible field")
	ErrNotYourParcel = errors.New("that parcel is not yours to trace")
)

// Limits, named so they can be argued with.
const (
	// minPoints is a triangle. Fewer encloses nothing.
	minPoints = 3

	// maxPoints bounds what a handset can send. A five-minute walk logging
	// every second is three hundred points; ten times that is a device stuck
	// in a loop, not a field.
	maxPoints = 3000

	// worstUsableAccuracyM is the point accuracy beyond which a corner is not
	// a corner. Fifty metres is a phone that has not seen the sky: the
	// boundary it draws could be a neighbour's field.
	worstUsableAccuracyM = 50

	// minFieldHa and maxFieldHa bound a plausible Maharashtra holding. The
	// lower bound catches a farmer who tapped three times without walking;
	// the upper catches a walk that wrapped a village.
	minFieldHa = 0.01
	maxFieldHa = 200
)

// Boundary gains the tracing provenance.
type Trailing struct {
	Source           string       `json:"source"`
	TracedAt         string       `json:"traced_at,omitempty"`
	TracedByName     string       `json:"traced_by_name,omitempty"`
	WorstAccuracyM   float64      `json:"worst_accuracy_m,omitempty"`
	Points           []TracePoint `json:"traced_points,omitempty"`
	ConfirmedAt      string       `json:"confirmed_at,omitempty"`
	ConfirmedByName  string       `json:"confirmed_by_name,omitempty"`
	RegistryExtentHa float64      `json:"registry_extent_ha,omitempty"`

	// AreaDisagreement is the polygon's area against the registry's extent,
	// as a share of the registry figure. Reported, never resolved.
	AreaDisagreement *float64 `json:"area_disagreement_percent,omitempty"`

	// Notes are what an officer should read before relying on this boundary.
	Notes []string `json:"notes,omitempty"`
}

// RecordTrace turns a walk into a boundary.
//
// Replaces any existing traced boundary for the parcel: a farmer who walks it
// again has corrected themselves, and keeping the first would mean the
// correction never took effect. A boundary that came from the registry is
// never overwritten -- the registry is the authority where it has an answer.
func (s *Store) RecordTrace(ctx context.Context, t Trace) (*Boundary, *Trailing, error) {
	if len(t.Points) < minPoints {
		return nil, nil, fmt.Errorf("%w: %d were recorded", ErrTooFewPoints, len(t.Points))
	}
	if len(t.Points) > maxPoints {
		return nil, nil, fmt.Errorf(
			"%w: %d points is not a walk around a field", ErrNotAPolygon, len(t.Points))
	}

	worst := 0.0
	for i, p := range t.Points {
		if math.IsNaN(p.Lat) || math.IsNaN(p.Lon) ||
			p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 {
			return nil, nil, fmt.Errorf("%w: corner %d is not a position",
				ErrNotAPolygon, i+1)
		}
		worst = math.Max(worst, p.AccuracyM)
	}
	if worst > worstUsableAccuracyM {
		return nil, nil, fmt.Errorf(
			"%w: the worst corner was uncertain by %.0f m, and a boundary drawn "+
				"from that could sit on a neighbour's field. Move into the open, "+
				"wait for the position to settle, and walk it again.",
			ErrTooCoarse, worst)
	}

	ring := closedRing(t.Points)
	coords, err := json.Marshal([][][2]float64{ring})
	if err != nil {
		return nil, nil, err
	}
	geojson := fmt.Sprintf(`{"type":"Polygon","coordinates":%s}`, coords)

	pointsJSON, err := json.Marshal(t.Points)
	if err != nil {
		return nil, nil, err
	}

	var b Boundary
	var areaHa float64
	err = s.pool.QueryRow(ctx, `
		WITH drawn AS (
			-- ST_MakeValid repairs a ring that crosses itself, which is what a
			-- walk produces when somebody steps back around an obstacle. The
			-- alternative is refusing a boundary over a detour round a tree.
			-- CollectionExtract keeps the polygons: repairing a ring that
			-- pinches can leave a GeometryCollection, which the column does
			-- not take. See survey/boundaries.go.
			SELECT ST_Multi(ST_CollectionExtract(ST_MakeValid(
				ST_SetSRID(ST_GeomFromGeoJSON($5), 4326)), 3)) AS geom
		)
		INSERT INTO parcel_boundaries (
			farm_id, farmer_id, survey_number, village_lgd_code,
			geom, polygon_area_ha, source,
			traced_points, worst_accuracy_m, traced_by, traced_at,
			registry_extent_ha, captured_by
		)
		SELECT $1, $2, $3, $4,
		       d.geom,
		       ST_Area(d.geom::geography) / 10000,
		       'field_traced',
		       $6, $7, NULLIF($8, '')::uuid, now(),
		       NULLIF($9, 0), NULLIF($8, '')::uuid
		FROM drawn d
		ON CONFLICT (farm_id) DO UPDATE
		SET geom               = EXCLUDED.geom,
		    polygon_area_ha    = EXCLUDED.polygon_area_ha,
		    source             = EXCLUDED.source,
		    traced_points      = EXCLUDED.traced_points,
		    worst_accuracy_m   = EXCLUDED.worst_accuracy_m,
		    traced_by          = EXCLUDED.traced_by,
		    traced_at          = EXCLUDED.traced_at,
		    registry_extent_ha = EXCLUDED.registry_extent_ha,
		    -- A re-walk is a new boundary, so an old confirmation no longer
		    -- applies to it.
		    confirmed_by       = NULL,
		    confirmed_at       = NULL
		-- The registry is the authority wherever it has an answer.
		WHERE parcel_boundaries.source <> 'agristack'
		RETURNING farm_id, farmer_id, survey_number, village_lgd_code,
		          COALESCE(polygon_area_ha, 0), source`,
		t.FarmID, t.FarmerID, t.SurveyNumber, t.VillageLGDCode,
		geojson, pointsJSON, worst, t.TracedBy, t.RegistryExtentHa).
		Scan(&b.FarmID, &b.FarmerID, &b.SurveyNumber, &b.VillageCode,
			&areaHa, &b.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf(
			"%w: this parcel already has a boundary from the land registry, "+
				"which is the authority for it", ErrNotYourParcel)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("recording the traced boundary: %w", err)
	}
	b.PolygonAreaHa = areaHa

	if areaHa < minFieldHa || areaHa > maxFieldHa {
		// Written and then refused, so the walk is not silently lost: the row
		// is removed and the farmer told what happened.
		_, _ = s.pool.Exec(ctx,
			`DELETE FROM parcel_boundaries WHERE farm_id = $1 AND source = 'field_traced'`,
			t.FarmID)
		return nil, nil, fmt.Errorf(
			"%w: the corners enclose %.3f hectares. Walk the whole edge of the "+
				"plot, stopping at each corner, and finish where you started.",
			ErrImplausible, areaHa)
	}

	return &b, s.trailing(t, worst, areaHa), nil
}

// trailing assembles what an officer should read before relying on a boundary.
func (s *Store) trailing(t Trace, worst, areaHa float64) *Trailing {
	tr := &Trailing{
		Source:           "field_traced",
		WorstAccuracyM:   round2(worst),
		Points:           t.Points,
		RegistryExtentHa: t.RegistryExtentHa,
		TracedAt:         time.Now().UTC().Format(time.RFC3339),
	}

	if t.RegistryExtentHa > 0 {
		diff := (areaHa - t.RegistryExtentHa) / t.RegistryExtentHa * 100
		d := round1(diff)
		tr.AreaDisagreement = &d

		if math.Abs(diff) >= areaDisagreementPercent {
			tr.Notes = append(tr.Notes, fmt.Sprintf(
				"the walk encloses %.2f ha against the %.2f ha the registry holds "+
					"for this survey number, a difference of %.0f%%. A jointly held "+
					"survey number, a plot divided between family, and a walk that "+
					"cut a corner all look like this; the two figures are kept as "+
					"they are rather than one being corrected to the other.",
				areaHa, t.RegistryExtentHa, diff))
		}
	}

	// The uncertainty a boundary carries is the uncertainty of its worst
	// corner, and it is worth stating in metres of field rather than as a
	// number nobody converts.
	if worst > 0 {
		tr.Notes = append(tr.Notes, fmt.Sprintf(
			"the position was uncertain by up to %.0f m at one corner, so the "+
				"edge of this plot could be out by about that much", worst))
	}
	if worst > goodAccuracyM {
		tr.Notes = append(tr.Notes, fmt.Sprintf(
			"a boundary walked with better than %.0f m at every corner would be "+
				"materially tighter; this one is usable and should be re-walked "+
				"if a claim turns on where the edge falls", goodAccuracyM))
	}

	tr.Notes = append(tr.Notes,
		"this boundary was drawn by the person claiming on it and has not been "+
			"confirmed by an officer; every figure derived from it carries that")

	return tr
}

const (
	// areaDisagreementPercent is how far the walk and the registry may differ
	// before it is worth remarking on. Fifteen per cent: a walk is not a
	// survey, and a smaller difference says nothing.
	areaDisagreementPercent = 15

	// goodAccuracyM is the accuracy at which a boundary is as good as a phone
	// will give.
	goodAccuracyM = 10.0
)

// closedRing turns a walk into a GeoJSON ring.
//
// Positions are longitude-first, which is the order GeoJSON and the analytics
// engine both use and the opposite of how a person says them. Getting it
// backwards puts the field on the wrong continent, and the engine says so
// with LATLON_SWAPPED rather than silently analysing the sea.
func closedRing(points []TracePoint) [][2]float64 {
	ring := make([][2]float64, 0, len(points)+1)
	for _, p := range points {
		ring = append(ring, [2]float64{p.Lon, p.Lat})
	}
	// A walk ends where it started only if the farmer was careful. Closing it
	// here is what the format requires and what they meant.
	if first, last := ring[0], ring[len(ring)-1]; first != last {
		ring = append(ring, first)
	}
	return ring
}

// Confirm records an officer agreeing that a traced boundary is the plot.
func (s *Store) Confirm(ctx context.Context, farmID, officerID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE parcel_boundaries
		SET confirmed_by = $2::uuid, confirmed_at = now()
		WHERE farm_id = $1 AND source = 'field_traced'`, farmID, officerID)
	if err != nil {
		return fmt.Errorf("confirming the boundary: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no traced boundary for %s", ErrNoBoundary, farmID)
	}
	return nil
}

// TracedBoundary reads a boundary back with its provenance.
//
// Returns the geometry as GeoJSON so the assessment can hand it straight to
// the analytics engine, which is the whole reason for tracing one.
func (s *Store) TracedBoundary(
	ctx context.Context, farmID string,
) (geojson []byte, tr *Trailing, err error) {
	var (
		points           []byte
		worst, extent    *float64
		tracedAt, confAt *time.Time
		tracedBy, confBy string
		source           string
		areaHa           float64
	)

	err = s.pool.QueryRow(ctx, `
		SELECT ST_AsGeoJSON(b.geom), b.source,
		       b.traced_points, b.worst_accuracy_m,
		       b.traced_at, COALESCE(t.full_name, ''),
		       b.confirmed_at, COALESCE(c.full_name, ''),
		       b.registry_extent_ha, COALESCE(b.polygon_area_ha, 0)
		FROM parcel_boundaries b
		LEFT JOIN users t ON t.id = b.traced_by
		LEFT JOIN users c ON c.id = b.confirmed_by
		WHERE b.farm_id = $1`, farmID).
		Scan(&geojson, &source, &points, &worst,
			&tracedAt, &tracedBy, &confAt, &confBy, &extent, &areaHa)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNoBoundary
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the boundary: %w", err)
	}

	tr = &Trailing{Source: source, TracedByName: tracedBy, ConfirmedByName: confBy}
	if worst != nil {
		tr.WorstAccuracyM = *worst
	}
	if extent != nil {
		tr.RegistryExtentHa = *extent
		if *extent > 0 {
			d := round1((areaHa - *extent) / *extent * 100)
			tr.AreaDisagreement = &d
		}
	}
	if tracedAt != nil {
		tr.TracedAt = tracedAt.UTC().Format(time.RFC3339)
	}
	if confAt != nil {
		tr.ConfirmedAt = confAt.UTC().Format(time.RFC3339)
	}
	if len(points) > 0 {
		_ = json.Unmarshal(points, &tr.Points)
	}

	if source == "field_traced" && tr.ConfirmedAt == "" {
		tr.Notes = append(tr.Notes,
			"drawn by the person claiming on it and not yet confirmed by an officer")
	}
	if tr.AreaDisagreement != nil && math.Abs(*tr.AreaDisagreement) >= areaDisagreementPercent {
		tr.Notes = append(tr.Notes, fmt.Sprintf(
			"the boundary encloses %.0f%% %s area than the registry holds for this "+
				"survey number", math.Abs(*tr.AreaDisagreement),
			map[bool]string{true: "more", false: "less"}[*tr.AreaDisagreement > 0]))
	}

	return geojson, tr, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
