package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Parcel boundaries.
//
// AgriStack returns plot_geometry: null for every farmer reachable on both the
// Maharashtra and Uttar Pradesh deployments, so in practice a boundary here
// came from the mock registry or was traced in the field. The source is
// carried on the row and travels with every figure derived from it, because a
// geofence against a synthetic boundary is a geofence against a guess.

// Boundary is one parcel's recorded geometry.
type Boundary struct {
	FarmID        string  `json:"farm_id"`
	FarmerID      string  `json:"farmer_id"`
	SurveyNumber  string  `json:"survey_number"`
	VillageCode   string  `json:"village_lgd_code"`
	PolygonAreaHa float64 `json:"polygon_area_ha"`
	Source        string  `json:"source"`
	// GeoJSON is the geometry, for a map to draw.
	GeoJSON json.RawMessage `json:"geojson,omitempty"`
}

// BoundaryFor returns a parcel's boundary, if one is recorded.
func (s *Store) BoundaryFor(ctx context.Context, farmID string) (*Boundary, error) {
	var b Boundary
	var geo string
	err := s.pool.QueryRow(ctx, `
		SELECT farm_id, farmer_id, survey_number, village_lgd_code,
		       COALESCE(polygon_area_ha, 0), source, ST_AsGeoJSON(geom)
		FROM parcel_boundaries WHERE farm_id = $1`, farmID).Scan(
		&b.FarmID, &b.FarmerID, &b.SurveyNumber, &b.VillageCode,
		&b.PolygonAreaHa, &b.Source, &geo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoBoundary
	}
	if err != nil {
		return nil, err
	}
	b.GeoJSON = json.RawMessage(geo)
	return &b, nil
}

// SyncBoundaries loads every parcel geometry the registry has into PostGIS.
//
// Idempotent, and re-run at startup: the registry is the source of truth for
// geometry and this table is a spatial index over it. A parcel whose geometry
// changes upstream is updated; one that loses its geometry keeps the boundary
// already recorded, because a capture already checked against it must stay
// explicable.
func SyncBoundaries(
	ctx context.Context, pool *db.Pool, src agristack.Enumerable, log *slog.Logger,
) (int, error) {
	loaded, failed := 0, 0
	for _, farmerID := range src.FarmerIDs() {
		rec, err := src.FarmerRecord(ctx, farmerID)
		if err != nil {
			return loaded, fmt.Errorf("reading farmer %s: %w", farmerID, err)
		}
		n, err := SyncRecordBoundaries(ctx, pool, rec)
		loaded += n
		if err != nil {
			// One unusable outline is not a reason to refuse to start.
			//
			// This ran at startup and returned the first error it met, so a
			// single parcel whose geometry PostGIS would not take stopped the
			// whole platform -- every farmer in the state locked out by one
			// farmer's untidy boundary. The parcel is named in the log and
			// simply has no fence until somebody walks it, which is the state
			// every unmapped parcel is in anyway.
			failed++
			log.Warn("a parcel's boundary could not be loaded",
				"farmer_id", farmerID, "error", err)
		}
	}
	if loaded > 0 || failed > 0 {
		log.Info("parcel boundaries synced",
			"parcels", loaded, "unusable", failed)
	}
	return loaded, nil
}

// SyncRecordBoundaries loads one record's parcel geometry into PostGIS.
//
// Called for each record the registry stores or changes, so a live registry
// that cannot be enumerated keeps boundaries current one farmer at a time.
func SyncRecordBoundaries(ctx context.Context, pool *db.Pool, rec *agristack.FarmerRecord) (int, error) {
	loaded := 0
	var firstErr error
	for _, p := range rec.Parcels {
		if p.PlotGeometry == nil {
			continue
		}

		geom, err := json.Marshal(p.PlotGeometry)
		if err != nil {
			return loaded, fmt.Errorf("encoding geometry for %s: %w", p.FarmID, err)
		}

		// ST_Multi normalises a Polygon into a MultiPolygon so the column
		// type holds either shape. ST_MakeValid repairs a ring that is
		// self-intersecting or unclosed rather than refusing the load --
		// a traced boundary from a phone is often a little untidy, and a
		// repaired polygon is far more useful than none.
		//
		// ST_CollectionExtract(..., 3) keeps the polygons and drops what
		// else the repair produced. Repairing a ring that crosses itself can
		// yield a GeometryCollection -- the polygons plus the stray line or
		// point where it pinched -- and that does not fit a MultiPolygon
		// column. Found on the first import of a real village: one parcel of
		// 873 did it, the insert failed, and because the sync ran at startup
		// the whole platform went into a restart loop over one farmer's
		// untidy outline.
		_, err = pool.Exec(ctx, `
			INSERT INTO parcel_boundaries (
				farm_id, farmer_id, survey_number, village_lgd_code,
				geom, polygon_area_ha, source
			) VALUES (
				$1, $2, $3, $4,
				ST_Multi(ST_CollectionExtract(
					ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($5), 4326)), 3)),
				ST_Area(ST_CollectionExtract(
					ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($5), 4326)), 3)::geography) / 10000,
				$6
			)
			ON CONFLICT (farm_id) DO UPDATE
			SET geom = EXCLUDED.geom,
			    polygon_area_ha = EXCLUDED.polygon_area_ha,
			    village_lgd_code = EXCLUDED.village_lgd_code,
			    survey_number = EXCLUDED.survey_number`,
			p.FarmID, p.FarmerID, p.SurveyNumber, p.VillageLGDCode,
			string(geom), boundarySource())
		if err != nil {
			// The next parcel is somebody else's fence and is not implicated
			// by this one.
			if firstErr == nil {
				firstErr = fmt.Errorf("loading boundary for %s: %w", p.FarmID, err)
			}
			continue
		}
		loaded++
	}
	return loaded, firstErr
}

// boundarySource labels where a boundary came from.
//
// Everything in mock_files carries a traced boundary rather than a registry
// one; when a live AgriStack source starts returning geometry this becomes a
// property of the source rather than a constant.
func boundarySource() string { return "mock" }

// VillageOfParcel says where a parcel is, and nothing else about it.
//
// For the access check on the routes that return a parcel's position: an
// officer reaches the villages they are posted to, and asking that question
// should not require reading the geometry it is deciding whether to release.
//
// Empty and no error when nothing is recorded for the parcel. A parcel with no
// boundary has no position to protect, and refusing on a missing row would
// make this an oracle for which farm ids have been surveyed.
func (s *Store) VillageOfParcel(ctx context.Context, farmID string) (string, error) {
	var village string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(village_lgd_code, '') FROM parcel_boundaries
		  WHERE farm_id = $1`, farmID).Scan(&village)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading a parcel's village: %w", err)
	}
	return village, nil
}
