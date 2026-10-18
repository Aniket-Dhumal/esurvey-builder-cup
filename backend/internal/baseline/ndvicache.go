package baseline

import (
	"context"
	"encoding/json"

	"github.com/agribid/esurvey-platform/backend/internal/rscache"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// ndvi returns a cached analysis or buys one.
//
// The cache itself lives in rscache, because the weather record has exactly
// the same shape of problem -- a closed window over an archive that will not
// change -- and two copies of that reasoning would drift.
func (s *Store) ndvi(
	ctx context.Context, product, key string, req satellite.NDVIRequest,
) (*satellite.NDVIResult, error) {
	if raw, ok := s.cache.Get(ctx, product, key, req.SowingDate, req.IncidentDate); ok {
		var out satellite.NDVIResult
		if err := json.Unmarshal(raw, &out); err == nil {
			return &out, nil
		}
		// A row that cannot be decoded would fail forever. Drop it and buy the
		// analysis again.
		s.cache.Forget(ctx, product, key, req.SowingDate, req.IncidentDate)
	}

	res, err := s.eng.NDVI(ctx, req)
	if err != nil {
		return nil, err
	}

	if raw, err := json.Marshal(res); err == nil {
		s.cache.Put(ctx, product, key, req.SowingDate, req.IncidentDate,
			raw, rscache.Settled(req.IncidentDate))
	}
	return res, nil
}
