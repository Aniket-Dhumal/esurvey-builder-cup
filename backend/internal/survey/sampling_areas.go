package survey

import (
	"context"
	"encoding/json"
	"fmt"
)

// The ground each clip actually shows, as a shape an analysis can be run over.
//
// # Why this exists
//
// The optical analysis is run over the whole parcel and returns one spatial
// mean per pass. That is the right figure for "how did this field do", and it
// is the wrong figure for the question a fraud check asks: does the ground the
// farmer filmed look, from orbit, like what the footage shows?
//
// A field mean cannot answer that. A farmer who films the one ruined corner of
// a healthy field produces a claim the field mean contradicts only weakly --
// the mean is dragged down a little by the ruined corner and the officer has no
// way to tell a small bad patch from a uniformly poor season. Worse, the
// reverse case is invisible: footage of a devastated crop taken at a spot the
// satellite shows as vigorous is the strongest evidence of fraud this platform
// could hold, and averaging it into a field mean destroys it.
//
// So each point of the walk gets its own small polygon, and the same optical
// analysis is run over that.
//
// # Why the polygons are clipped to the parcel
//
// A buffer around a point near the edge of a field overlaps the neighbour's
// crop, the bund, and the road. NDVI averaged over that is not a reading about
// this farm, and a claim decided on it would rest partly on land the farmer
// does not own. Clipped, the shape is the part of *this* parcel the clip could
// plausibly have shown -- which is the only ground this assessment may speak
// for. It is the same rule the map of the walk draws.
//
// A capture recorded outside the boundary clips to nothing. That is a finding,
// not a failure: there is no ground inside the field at that point to sample,
// and the reading is absent with that said in words.

// SamplingRadiusM is the radius sampled around each recorded point.
//
// Twenty metres, deliberately the same figure the report's map draws as the
// assumed coverage of a clip, so the ring an officer sees is the ground the
// index was read over. Two numbers that mean the same thing and differ by five
// metres is how a document stops being checkable.
//
// It is also near the floor of what Sentinel-2 can answer: a 20 m circle is
// about 1,250 m², or a dozen 10 m pixels, several of them mixed with whatever
// lies beyond the buffer. That is enough for a mean and not enough for a small
// difference to mean anything, which is why the comparison that reads these
// requires a wide margin before it says anything at all.
const SamplingRadiusM = 20.0

// SamplingArea is one point's own patch of the parcel.
type SamplingArea struct {
	StationNo int

	// GeoJSON is the clipped polygon, ready to submit to the analytics engine.
	// Never empty: an area that clipped to nothing is not returned at all.
	GeoJSON json.RawMessage

	// AreaM2 is how much ground survived the clip. A point near a corner keeps
	// a fraction of its circle, and a reading over 300 m² carries less than
	// one over the full 1,250 -- so the figure travels with the shape rather
	// than being recomputed by whoever reads it.
	AreaM2 float64

	// OutsideParcel is true where the recorded position itself fell outside
	// the boundary. The area is then the part of the buffer that reaches back
	// inside, which is worth sampling and worth flagging.
	OutsideParcel bool
}

// SamplingAreasFor returns one patch per recorded point of a report's walk.
//
// Ordered by point number. Points with no recorded position, and points whose
// buffer lies entirely outside the parcel, are absent from the result: there is
// nothing to sample, and a zero-area polygon submitted to an analysis engine
// comes back as an error rather than as the honest silence it should be.
//
// Uses the still or the clip's own recorded position -- where the farmer
// actually stood -- and never the suggested coordinate. Sampling the suggestion
// would answer a question nobody asked: the footage came from where they were.
func (s *Store) SamplingAreasFor(
	ctx context.Context, reportID, farmID string, radiusM float64,
) ([]SamplingArea, error) {
	if radiusM <= 0 {
		radiusM = SamplingRadiusM
	}

	// The buffer is taken on the geography type so the radius is in metres at
	// this latitude rather than in degrees, which at 19°N would be a circle
	// about 40% wider east to west than north to south.
	//
	// DISTINCT ON keeps one patch per point: a station has a still and a clip
	// at the same position, and sampling the same ground twice would double a
	// point's weight in whatever reads this.
	rows, err := s.pool.Query(ctx, `
		WITH parcel AS (
		    SELECT geom FROM parcel_boundaries WHERE farm_id = $2
		),
		spots AS (
		    SELECT DISTINCT ON (c.station_no)
		           c.station_no, c.geom
		      FROM survey_captures c
		     WHERE c.report_id = $1::uuid
		       AND c.station_no IS NOT NULL
		       AND c.geom IS NOT NULL
		     ORDER BY c.station_no, c.captured_at
		)
		SELECT s.station_no,
		       ST_AsGeoJSON(patch.g),
		       ST_Area(patch.g::geography),
		       NOT ST_Intersects(s.geom, parcel.geom)
		  FROM spots s
		 CROSS JOIN parcel
		 CROSS JOIN LATERAL (
		     SELECT ST_Intersection(
		                ST_Buffer(s.geom::geography, $3)::geometry,
		                parcel.geom) AS g
		 ) AS patch
		 WHERE NOT ST_IsEmpty(patch.g)
		 ORDER BY s.station_no`, reportID, farmID, radiusM)
	if err != nil {
		return nil, fmt.Errorf("working out the ground each clip covers: %w", err)
	}
	defer rows.Close()

	var out []SamplingArea
	for rows.Next() {
		var a SamplingArea
		var geo string
		if err := rows.Scan(&a.StationNo, &geo, &a.AreaM2, &a.OutsideParcel); err != nil {
			return nil, err
		}
		a.GeoJSON = json.RawMessage(geo)
		out = append(out, a)
	}
	return out, rows.Err()
}
