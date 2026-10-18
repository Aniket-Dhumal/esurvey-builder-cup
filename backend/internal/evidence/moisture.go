package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/agribid/esurvey-platform/backend/internal/moisture"
	"github.com/agribid/esurvey-platform/backend/internal/rscache"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Moisture is the part of the satellite service this package needs.
type Moisture interface {
	Moisture(ctx context.Context, req satellite.MoistureRequest) (*satellite.MoistureResult, error)
}

// MoistureCache is the slice of rscache the cached source uses, so a test can
// hand in a map.
type MoistureCache interface {
	Get(ctx context.Context, product, key, start, end string) ([]byte, bool)
	Put(ctx context.Context, product, key, start, end string, raw []byte, permanent bool)
	Forget(ctx context.Context, product, key, start, end string)
}

// moistureProduct names the cache rows. Versioned, because the measurement's
// shape is a contract with the satellite service: a change to the masks or the
// neighbourhood makes every stored row a different measurement, and bumping
// this retires them without a migration.
const moistureProduct = "moisture:v1"

// MoistureBaselineYears is how many prior seasons each request measures.
//
// Three. Two is a comparison with a single other year, which a wet or a dry
// year can dominate; more than three adds Earth Engine cost for a normal that
// barely moves, and Sentinel-2's surface reflectance record over India is
// dense only from 2019.
const MoistureBaselineYears = 3

// CachedMoisture puts the shared remote-sensing cache in front of the service.
//
// # Why this cache and not only the service's own
//
// The satellite service keeps results in memory, which is lost on every
// restart and is not shared between two instances. This is Postgres, shared
// by every API instance and kept for good once a window has settled: a
// Sentinel-2 pass from last August is not going to be re-flown, and buying
// the same three seasons for the same parcel on every re-assessment and every
// appeal would pay Earth Engine to learn nothing new.
//
// # Why a synthetic measurement is never stored
//
// A development stack that cached one would go on serving it after the
// service was pointed at Earth Engine, and the label would be the only thing
// standing between a generated drought and a compensation record. Not storing
// it means switching the backend is enough.
type CachedMoisture struct {
	Source Moisture
	Cache  MoistureCache
}

// Moisture returns a cached measurement or buys one.
func (c CachedMoisture) Moisture(
	ctx context.Context, req satellite.MoistureRequest,
) (*satellite.MoistureResult, error) {
	key, err := moistureKey(req)
	if err != nil {
		return nil, err
	}

	if c.Cache != nil {
		if raw, ok := c.Cache.Get(ctx, moistureProduct, key, req.StartDate, req.EndDate); ok {
			var out satellite.MoistureResult
			if err := json.Unmarshal(raw, &out); err == nil && !moisture.IsSynthetic(out.Missions) {
				return &out, nil
			}
			// A row that cannot be decoded, or one that should never have been
			// written, would be served forever. Drop it and measure again.
			c.Cache.Forget(ctx, moistureProduct, key, req.StartDate, req.EndDate)
		}
	}

	res, err := c.Source.Moisture(ctx, req)
	if err != nil {
		return nil, err
	}

	if c.Cache != nil && !moisture.IsSynthetic(res.Missions) {
		if raw, err := json.Marshal(res); err == nil {
			c.Cache.Put(ctx, moistureProduct, key, req.StartDate, req.EndDate,
				raw, rscache.Settled(req.EndDate))
		}
	}
	return res, nil
}

// moistureKey is the parcel and the baseline depth. The window is its own
// column in the cache table, so it is not repeated here.
func moistureKey(req satellite.MoistureRequest) (string, error) {
	// The coordinates are re-marshalled rather than hashed as received, so
	// the same ring sent with different whitespace is one row, not two.
	var coords any
	if err := json.Unmarshal(req.Geometry.Coordinates, &coords); err != nil {
		return "", fmt.Errorf("the parcel boundary is not valid JSON: %w", err)
	}
	canon, err := json.Marshal(struct {
		T string `json:"t"`
		C any    `json:"c"`
		Y int    `json:"y"`
	}{req.Geometry.Type, coords, req.BaselineYears})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:16]), nil
}
