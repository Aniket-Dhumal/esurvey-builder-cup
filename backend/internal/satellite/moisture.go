package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Canopy water and surface soil moisture, on the radar module's contract.
//
// Served by the same satellite-intelligence service as radar, because it is
// the same Earth Engine project and the same concurrency limit: two services
// with four slots each would put eight requests against a quota sized for
// four. See satellite-intelligence/src/esurvey_sar/moisture.py for what is
// measured, and internal/moisture for what it means.
//
// # Why the field names are pinned
//
// They match the Pydantic models exactly, and a renamed field fails quietly:
// an unmarshalled field is a zero, and a zero NDMI is a plausible value for a
// stressed crop. TestMoistureWireNames holds them.

// MoistureRequest submits a parcel and a window.
type MoistureRequest struct {
	Geometry  Geometry `json:"geometry"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
	// BaselineYears is how many prior seasons to measure over the same
	// calendar window. Zero leaves the season with nothing to be compared
	// against, which is allowed and which the analysis says out loud.
	BaselineYears int    `json:"baseline_years"`
	FarmerID      string `json:"farmer_id,omitempty"`
	FieldID       string `json:"field_id,omitempty"`
}

// OpticalPass is one Sentinel-2 date over the parcel and its neighbourhood.
//
// Pointers, because a pass can see the parcel and no cropland in the ring, or
// the reverse, and a missing mean must stay missing rather than become 0.0.
type OpticalPass struct {
	Date string `json:"date"`
	// SeasonOffset is 0 for the season assessed, N for the same window N
	// years earlier.
	SeasonOffset int `json:"season_offset"`

	NDMI          *float64 `json:"ndmi"`
	NDVI          *float64 `json:"ndvi"`
	ClearFraction float64  `json:"clear_fraction"`
	ClearPixels   int      `json:"clear_pixels"`

	RingNDMI        *float64 `json:"ring_ndmi"`
	RingNDVI        *float64 `json:"ring_ndvi"`
	RingClearPixels int      `json:"ring_clear_pixels"`
}

// SoilPass is one Sentinel-1 VV pass over the parcel.
type SoilPass struct {
	Date          string   `json:"date"`
	VVdB          float64  `json:"vv_db"`
	Orbit         string   `json:"orbit,omitempty"`
	RelativeOrbit *int     `json:"relative_orbit"`
	Coverage      *float64 `json:"coverage"`
}

// MoistureResult is the completed measurement.
type MoistureResult struct {
	Optical []OpticalPass `json:"optical"`
	Soil    []SoilPass    `json:"soil"`

	ParcelPixels       int `json:"parcel_pixels"`
	RingCroplandPixels int `json:"ring_cropland_pixels"`

	BaselineYears  int     `json:"baseline_years"`
	ClearThreshold float64 `json:"clear_threshold"`

	Missions []string `json:"missions,omitempty"`
	AreaHa   *float64 `json:"area_ha"`
}

type moistureStatusResponse struct {
	Status    string          `json:"status"`
	Result    *MoistureResult `json:"result"`
	Err       string          `json:"error"`
	ErrorCode string          `json:"error_code"`
}

// ErrMoistureUnavailable means the service has no moisture module.
//
// Distinct from a failure for the same reason as ErrRadarUnavailable: it is a
// deployment where the endpoint does not exist, the assessment proceeds
// without it, and nobody should be paged.
var ErrMoistureUnavailable = errors.New("the satellite service has no moisture module")

// Moisture submits and waits for a completed measurement.
func (c *Client) Moisture(ctx context.Context, req MoistureRequest) (*MoistureResult, error) {
	// One slot for the whole analysis, submit to result: the upstream's
	// limit is on analyses running, not on requests made.
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	taskID, err := c.submit(ctx, "/api/v1/moisture/submit", req)
	if err != nil {
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusNotFound {
			return nil, ErrMoistureUnavailable
		}
		return nil, err
	}

	var out *MoistureResult
	err = c.poll(ctx, "/api/v1/moisture/status/"+taskID, func(raw []byte) (bool, error) {
		var sr moistureStatusResponse
		if err := json.Unmarshal(raw, &sr); err != nil {
			return false, fmt.Errorf("decoding moisture status: %w", err)
		}
		switch sr.Status {
		case StatusProcessing:
			return false, nil
		case StatusFailed:
			return true, &Error{Code: sr.ErrorCode, Message: sr.Err}
		case StatusCompleted:
			if sr.Result == nil {
				return true, errors.New("moisture completed without a result")
			}
			out = sr.Result
			return true, nil
		default:
			return true, fmt.Errorf("unexpected moisture status %q", sr.Status)
		}
	})
	return out, err
}
