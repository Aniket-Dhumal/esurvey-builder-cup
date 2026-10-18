package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Sentinel-1 radar, on the same contract as the two modules that already
// exist.
//
// # Why here and not a separate integration
//
// The Farm Analytics Engine is already Earth Engine work behind an HTTP
// contract -- that is what the NDVI and terrain modules are. Sentinel-1 lives
// in the same catalogue as Sentinel-2 and the Copernicus DEM, so a radar
// module is one more endpoint on a service that is already authenticated,
// already cached, already rate-limited and already circuit-broken. Standing up
// a parallel Earth Engine client inside this process would duplicate all four
// and add a credential this platform does not currently need.
//
// # Why it is optional
//
// The engine may not implement it yet. A deployment where it does not must
// keep working exactly as before, so an unimplemented endpoint is reported as
// unavailable rather than as a failure, and the assessment proceeds without
// radar and says so. This is the same shape as every other evidence source
// here: absent is a state, not an error.

// SARRequest submits a parcel for backscatter over a window.
type SARRequest struct {
	Geometry Geometry `json:"geometry"`
	// StartDate and EndDate bound the passes to return. A window either side
	// of the incident is what the analysis needs; the engine returns every
	// pass it has.
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	FarmerID  string `json:"farmer_id,omitempty"`
	FieldID   string `json:"field_id,omitempty"`
}

// SARPoint is the parcel-mean backscatter for one pass, in decibels.
type SARPoint struct {
	Date string  `json:"date"`
	VVdB float64 `json:"vv_db"`
	VHdB float64 `json:"vh_db"`
	// Orbit is ASCENDING or DESCENDING. Required for a meaningful comparison:
	// the same unchanged field returns several decibels differently between
	// the two, so a series without it can only be compared loosely.
	Orbit string `json:"orbit,omitempty"`
}

// SARResult is the completed radar analysis.
//
// An empty series is a success with nothing to say -- the same convention the
// NDVI module uses for a wholly clouded window -- because a parcel may simply
// have had no pass in a short window.
type SARResult struct {
	TimeSeries []SARPoint `json:"time_series"`
	// Missions names what was actually queried, so a result can be traced to
	// a collection rather than to "satellite".
	Missions []string `json:"missions,omitempty"`
}

type sarStatusResponse struct {
	Status    string     `json:"status"`
	Result    *SARResult `json:"result"`
	Err       string     `json:"error"`
	ErrorCode string     `json:"error_code"`
}

// ErrRadarUnavailable means this engine has no radar module.
//
// Distinct from a failure, because it is not one: it is a deployment where
// the endpoint does not exist, the assessment should proceed without radar,
// and nobody should be paged.
var ErrRadarUnavailable = errors.New("the analytics engine has no radar module")

// SAR submits and waits for a completed backscatter series.
func (c *Client) SAR(ctx context.Context, req SARRequest) (*SARResult, error) {
	// One slot for the whole analysis, submit to result: the upstream's
	// limit is on analyses running, not on requests made.
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	taskID, err := c.submit(ctx, "/api/v1/sar/submit", req)
	if err != nil {
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusNotFound {
			return nil, ErrRadarUnavailable
		}
		return nil, err
	}

	var out *SARResult
	err = c.poll(ctx, "/api/v1/sar/status/"+taskID, func(raw []byte) (bool, error) {
		var sr sarStatusResponse
		if err := json.Unmarshal(raw, &sr); err != nil {
			return false, fmt.Errorf("decoding sar status: %w", err)
		}
		switch sr.Status {
		case StatusProcessing:
			return false, nil
		case StatusFailed:
			return true, &Error{Code: sr.ErrorCode, Message: sr.Err}
		case StatusCompleted:
			if sr.Result == nil {
				return true, errors.New("sar completed without a result")
			}
			out = sr.Result
			return true, nil
		default:
			return true, fmt.Errorf("unexpected sar status %q", sr.Status)
		}
	})
	return out, err
}
