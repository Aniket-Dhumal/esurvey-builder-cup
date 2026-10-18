// Package satellite is a client for the Farm Analytics Engine.
//
// Two modules share one contract: NDVI derives a cloud-free Sentinel-2 time
// series plus two colourised overlays; Terrain derives elevation, slope and
// aspect from the Copernicus DEM GLO-30.
//
// Both are asynchronous — submit returns 202 with a task id, and the caller
// polls until the status leaves PROCESSING. Repeat submits of the same field
// are cache-served and complete in milliseconds.
package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

const (
	StatusProcessing = "PROCESSING"
	StatusCompleted  = "COMPLETED"
	StatusFailed     = "FAILED"
)

// Client talks to one Farm Analytics Engine deployment.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// PollInterval and PollTimeout bound the wait for an async task.
	PollInterval time.Duration
	PollTimeout  time.Duration

	// Gate bounds how many analyses every instance together has running on
	// this upstream. Nil admits all.
	Gate *gate.Gate

	// breaker stops a dead upstream from making every officer wait out the
	// full timeout to learn the same thing.
	breaker breaker
}

// Health reports whether this client currently believes the engine is up.
func (c *Client) Health() (open bool, consecutiveFailures int) {
	return c.breaker.state()
}

// New builds a client with sensible defaults.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		HTTP:         &http.Client{Timeout: 60 * time.Second},
		PollInterval: 2 * time.Second,
		PollTimeout:  3 * time.Minute,
	}
}

// Geometry is a GeoJSON Polygon or MultiPolygon. Positions are
// [longitude, latitude] and the exterior ring must be closed — the engine
// rejects [latitude, longitude] with LATLON_SWAPPED rather than silently
// producing a field on the wrong continent.
type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

type submitResponse struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// apiError is the engine's error envelope.
type apiError struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

// Error carries the engine's stable error code so callers can branch on it
// rather than on message text.
type Error struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("satellite: %s (%s, http %d)", e.Message, e.Code, e.HTTPStatus)
}

// NDVIPoint is the spatial mean NDVI for one cloud-free pass.
type NDVIPoint struct {
	Date string  `json:"date"`
	NDVI float64 `json:"ndvi"`
}

// Image is a colourised overlay, clipped to the submitted boundary, returned
// inline as a data:image/png;base64 URI.
type Image struct {
	Date   string `json:"date"`
	Base64 string `json:"base64"`
}

// NDVIResult is the completed NDVI analysis.
//
// A window with no cloud-free pass is a success, not an error: TimeSeries is
// empty and both images are nil. That is common in kharif — on the reference
// parcel a 110-day window yielded four usable passes with a two-month monsoon
// gap.
type NDVIResult struct {
	TimeSeries     []NDVIPoint `json:"time_series"`
	MidDateImage   *Image      `json:"mid_date_image"`
	LastVisitImage *Image      `json:"last_visit_image"`
}

// TerrainResult is the completed terrain analysis.
type TerrainResult struct {
	Elevation struct {
		MeanMeters float64 `json:"mean_meters"`
		MinMeters  float64 `json:"min_meters"`
		MaxMeters  float64 `json:"max_meters"`
	} `json:"elevation"`
	Slope struct {
		MeanDegrees    float64 `json:"mean_degrees"`
		MeanPercentage float64 `json:"mean_percentage"`
		Classification string  `json:"classification"`
		ErosionRisk    string  `json:"erosion_risk"`
		Base64Map      string  `json:"base64_map"`
	} `json:"slope"`
	Aspect struct {
		MeanDegrees       *float64 `json:"mean_degrees"` // nil when too flat to have a downhill direction
		CardinalDirection string   `json:"cardinal_direction"`
		Base64Map         string   `json:"base64_map"`
	} `json:"aspect"`
}

type ndviStatusResponse struct {
	TaskID    string      `json:"task_id"`
	Status    string      `json:"status"`
	Result    *NDVIResult `json:"result"`
	Err       string      `json:"error"`
	ErrorCode string      `json:"error_code"`
}

type terrainStatusResponse struct {
	TaskID    string         `json:"task_id"`
	Status    string         `json:"status"`
	Result    *TerrainResult `json:"result"`
	Err       string         `json:"error"`
	ErrorCode string         `json:"error_code"`
}

// NDVIRequest submits a crop cycle. Both dates must be in the past and
// IncidentDate must fall strictly after SowingDate.
type NDVIRequest struct {
	Geometry     Geometry `json:"geometry"`
	SowingDate   string   `json:"sowing_date"`   // YYYY-MM-DD
	IncidentDate string   `json:"incident_date"` // YYYY-MM-DD
	FarmerID     string   `json:"farmer_id,omitempty"`
	FieldID      string   `json:"field_id,omitempty"`
}

// TerrainRequest submits a field for elevation analysis.
type TerrainRequest struct {
	Geometry Geometry `json:"geometry"`
	FarmerID string   `json:"farmer_id,omitempty"`
	FieldID  string   `json:"field_id,omitempty"`
}

// do performs one request, retrying what is worth retrying.
//
// A POST is retried only where the request demonstrably never arrived --
// see sentToServer. Retrying an accepted submit would start a second billable
// analysis of the same parcel and leave two task ids for one assessment.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if !c.breaker.allow(time.Now()) {
		return nil, ErrUpstreamDown
	}

	var buf []byte
	if body != nil {
		var err error
		buf, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := backoff(ctx, attempt-1); err != nil {
				return nil, err
			}
		}

		raw, err := c.attempt(ctx, method, path, buf, body != nil)
		if err == nil {
			c.breaker.succeeded()
			return raw, nil
		}
		lastErr = err

		if !retryable(err) {
			// A rejected request is not an upstream failure: the engine is
			// working and said no. Counting it toward the breaker would open
			// the circuit on a run of malformed polygons.
			var apiErr *Error
			if errors.As(err, &apiErr) && apiErr.HTTPStatus < 500 {
				return nil, err
			}
			c.breaker.failed(time.Now())
			return nil, err
		}

		if method != http.MethodGet && sentToServer(err) {
			c.breaker.failed(time.Now())
			return nil, err
		}
	}

	c.breaker.failed(time.Now())
	return nil, lastErr
}

func (c *Client) attempt(
	ctx context.Context, method, path string, buf []byte, hasBody bool,
) ([]byte, error) {
	var rdr io.Reader
	if hasBody {
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if resp.StatusCode >= 400 {
		var ae apiError
		if json.Unmarshal(raw, &ae) == nil && ae.Error.Code != "" {
			return nil, &Error{HTTPStatus: resp.StatusCode, Code: ae.Error.Code, Message: ae.Error.Message}
		}
		return nil, &Error{HTTPStatus: resp.StatusCode, Code: "UNKNOWN", Message: string(raw)}
	}
	return raw, nil
}

func (c *Client) submit(ctx context.Context, path string, body any) (string, error) {
	raw, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", fmt.Errorf("decoding submit response: %w", err)
	}
	if sr.TaskID == "" {
		return "", errors.New("submit returned no task_id")
	}
	return sr.TaskID, nil
}

// NDVI submits and waits for a completed NDVI analysis.
func (c *Client) NDVI(ctx context.Context, req NDVIRequest) (*NDVIResult, error) {
	// One slot for the whole analysis, submit to result: the upstream's
	// limit is on analyses running, not on requests made.
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	taskID, err := c.submit(ctx, "/api/v1/ndvi/submit", req)
	if err != nil {
		return nil, err
	}

	var out *NDVIResult
	err = c.poll(ctx, "/api/v1/ndvi/status/"+taskID, func(raw []byte) (bool, error) {
		var sr ndviStatusResponse
		if err := json.Unmarshal(raw, &sr); err != nil {
			return false, fmt.Errorf("decoding ndvi status: %w", err)
		}
		switch sr.Status {
		case StatusProcessing:
			return false, nil
		case StatusFailed:
			return true, &Error{Code: sr.ErrorCode, Message: sr.Err}
		case StatusCompleted:
			if sr.Result == nil {
				return true, errors.New("ndvi completed without a result")
			}
			out = sr.Result
			return true, nil
		default:
			return true, fmt.Errorf("unexpected ndvi status %q", sr.Status)
		}
	})
	return out, err
}

// Terrain submits and waits for a completed terrain analysis.
func (c *Client) Terrain(ctx context.Context, req TerrainRequest) (*TerrainResult, error) {
	// One slot for the whole analysis, submit to result: the upstream's
	// limit is on analyses running, not on requests made.
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	taskID, err := c.submit(ctx, "/api/v1/terrain/submit", req)
	if err != nil {
		return nil, err
	}

	var out *TerrainResult
	err = c.poll(ctx, "/api/v1/terrain/status/"+taskID, func(raw []byte) (bool, error) {
		var sr terrainStatusResponse
		if err := json.Unmarshal(raw, &sr); err != nil {
			return false, fmt.Errorf("decoding terrain status: %w", err)
		}
		switch sr.Status {
		case StatusProcessing:
			return false, nil
		case StatusFailed:
			return true, &Error{Code: sr.ErrorCode, Message: sr.Err}
		case StatusCompleted:
			if sr.Result == nil {
				return true, errors.New("terrain completed without a result")
			}
			out = sr.Result
			return true, nil
		default:
			return true, fmt.Errorf("unexpected terrain status %q", sr.Status)
		}
	})
	return out, err
}

// poll calls path until done reports true or the timeout elapses. Status
// polling is not rate limited by the engine.
func (c *Client) poll(ctx context.Context, path string, done func([]byte) (bool, error)) error {
	deadline := time.Now().Add(c.PollTimeout)
	for {
		raw, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		finished, err := done(raw)
		if err != nil {
			return err
		}
		if finished {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", c.PollTimeout, path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.PollInterval):
		}
	}
}
