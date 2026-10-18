package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agribid/esurvey-platform/backend/internal/evidence"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// pointOptical runs the parcel's own optical analysis over each spot the
// farmer filmed.
//
// # Why this is not one request
//
// The analytics engine answers with the spatial mean over the geometry it was
// given. There is no per-pixel output to slice, so the only way to learn what
// the index was at one corner of a field is to ask about that corner. Four
// points means four analyses.
//
// # What that costs, and why it is serial
//
// Five optical analyses per assessment instead of one. They are submitted one
// at a time rather than together: the engine's concurrency is a deployment-wide
// limit, and a single claim firing four submissions at it would push every
// other officer's assessment behind its own. Repeat submits of the same
// geometry are cache-served, so a re-run of the same assessment is cheap.
//
// The context carries the evidence assembler's budget. A patch the budget ran
// out on is simply absent, which the comparison reports as an unsampled point
// rather than as agreement.
type pointOptical struct {
	captures *survey.Store
	engine   *satellite.Client
}

// SpotSeries implements evidence.PointOptical.
func (p pointOptical) SpotSeries(
	ctx context.Context, req evidence.PointRequest,
) (map[int]evidence.PointSample, error) {
	if p.captures == nil || p.engine == nil {
		return nil, errors.New("per-point optical sampling is not configured")
	}

	areas, err := p.captures.SamplingAreasFor(
		ctx, req.ReportID, req.FarmID, req.RadiusM)
	if err != nil {
		return nil, err
	}
	if len(areas) == 0 {
		return nil, nil
	}

	out := make(map[int]evidence.PointSample, len(areas))
	for _, a := range areas {
		// Whatever has been gathered stands. Returning an error here would
		// throw away the points that did come back, and three readings are
		// worth more than none.
		if ctx.Err() != nil {
			break
		}

		// ST_AsGeoJSON emits a geometry object, which is exactly the shape
		// satellite.Geometry carries. A patch this service built and could not
		// read back is a fault worth reporting rather than skipping.
		var geom satellite.Geometry
		if err := json.Unmarshal(a.GeoJSON, &geom); err != nil {
			return nil, fmt.Errorf(
				"reading the patch computed for point %d: %w", a.StationNo, err)
		}

		res, err := p.engine.NDVI(ctx, satellite.NDVIRequest{
			Geometry:     geom,
			SowingDate:   req.SowingDate,
			IncidentDate: req.IncidentDate,
			FarmerID:     req.FarmerID,
			// A field id of its own per point.
			//
			// The engine caches on the identifiers it is given. Submitting a
			// 20 m patch under the parcel's own field id invites a cache that
			// keys on the id rather than the geometry to answer with the
			// parcel's series -- which would make every point look exactly
			// like the field and silently delete the whole check.
			FieldID: fmt.Sprintf("%s#point-%d", req.FarmID, a.StationNo),
		})
		if err != nil {
			// One clouded or refused patch is not a failure of the analysis.
			// The point is left out and the comparison says so.
			continue
		}

		out[a.StationNo] = evidence.PointSample{
			AreaM2:        a.AreaM2,
			OutsideParcel: a.OutsideParcel,
			Series:        res.TimeSeries,
		}
	}
	return out, nil
}

// PointOpticalFor builds the per-point optical sampler.
//
// Exported so cmd/server can wire it without this file having to know how the
// server is assembled. Returns nil where either half is missing, which the
// assembler reports as an absent source.
func PointOpticalFor(
	captures *survey.Store, engine *satellite.Client,
) evidence.PointOptical {
	if captures == nil || engine == nil {
		return nil
	}
	return pointOptical{captures: captures, engine: engine}
}
