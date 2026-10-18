// Package vision measures a farmer's survey video.
//
// # What it does
//
// Finds the station video on a report, sends the bytes to the ground vision
// service, and returns the measurements groundcv scores. Nothing here decides
// anything: the service measures, groundcv applies the specification's
// arithmetic, and the fusion decides how much either is worth.
//
// # Why the baseline is refused by default
//
// The only vision implementation that exists today is a classical baseline --
// colour thresholds and a gradient orientation, not the trained segmentation
// the specification asks for. It labels itself, and this package refuses it
// unless a deployment has explicitly permitted it, in exactly the way radar
// refuses a generated backscatter series.
//
// The reason is the same and it is worth stating twice: the specification
// hands the ground signal half a compensation decision in clear weather and
// nearly two thirds under cloud. An unvalidated estimate carrying that weight
// is worse than no estimate, because an absence is visible on the dossier and
// a bad number is not.
package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/survey"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

// ErrUnavailable means this deployment has no vision model.
//
// Distinct from a failure, because it is not one: the assessment proceeds and
// reports the ground score as an absent evidence source. Every deployment
// today is in this state.
var ErrUnavailable = errors.New("this deployment has no ground vision model")

// ErrBaselineRefused means the service answered with an unvalidated
// measurement and this deployment does not accept those.
var ErrBaselineRefused = errors.New(
	"the vision service returned an unvalidated baseline measurement, " +
		"which is not evidence")

// Captures is the part of the survey store this package needs.
type Captures interface {
	ForReport(ctx context.Context, reportID string) ([]survey.Capture, error)
	Media(ctx context.Context, id string) (io.ReadCloser, string, error)
}

// Client measures survey video through the ground vision service.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	captures Captures

	// frames caches what has been extracted from a clip. Nil means no cache
	// and every request re-extracts, which works and is merely wasteful.
	frames Frames

	// Gate bounds how many clips every instance together has being measured.
	// Nil admits all.
	Gate *gate.Gate

	// AllowBaseline permits an unvalidated classical measurement to be used.
	// Development only, and off by default so that a deployment which has
	// never heard of this flag refuses one.
	AllowBaseline bool
}

// New builds a client. An empty baseURL yields a client that always reports
// the module unavailable, which is what a deployment without one wants.
func New(baseURL, token string, captures Captures, allowBaseline bool) *Client {
	return &Client{
		baseURL:  baseURL,
		token:    token,
		captures: captures,
		// Decoding a minute of video is slower than any other evidence source
		// here, and the assembler's own budget still bounds the whole packet.
		http:          &http.Client{Timeout: 90 * time.Second},
		AllowBaseline: allowBaseline,
	}
}

// maxClip is the ceiling survey.MaxVideoBytes puts on one capture.
const maxClip = 60 << 20

// response is what the service returns.
type response struct {
	StemAngleDegrees float64 `json:"stem_angle_degrees"`
	Classes          struct {
		HealthyFoliage uint64 `json:"healthy_foliage"`
		Submerged      uint64 `json:"submerged"`
		Necrotic       uint64 `json:"necrotic"`
		Background     uint64 `json:"background"`
	} `json:"classes"`
	Quality           float64  `json:"quality"`
	Keyframes         int      `json:"keyframes"`
	CanopyHeightRatio *float64 `json:"canopy_height_ratio"`
	Baseline          bool     `json:"baseline"`
	QualityNotes      []string `json:"quality_notes"`
	CropDeclared      string   `json:"crop_declared"`
	CropSeen          string   `json:"crop_seen"`
	CropMatches       *bool    `json:"crop_matches"`
	CropMismatchConf  float64  `json:"crop_mismatch_confidence"`
	CropMismatchWhy   string   `json:"crop_mismatch_detail"`
}

// Clip is one station's footage, measured.
type Clip struct {
	// StationNo is which directed point it was taken at, where the walk was
	// directed. Zero for a clip that carries none.
	StationNo    int
	Measurements groundcv.Measurements

	// Crop is what the footage says about the crop's identity, as opposed to
	// its condition.
	Crop CropCheck
}

// CropCheck is whether the field holds the crop that was claimed.
//
// The walk is the only evidence that can answer it: the registry says what
// was sown months ago, the satellite says a crop grew, and neither can tell
// soybean from sugarcane on a two-hectare plot. A claim filed for a crop that
// is not in the field is the fraud this platform is most able to catch, and
// the one nothing else here looks for.
type CropCheck struct {
	// Declared is what the farmer said, as sent to the service.
	Declared string `json:"crop_declared,omitempty"`
	// Seen is what the model believes is there. Empty when it could not say.
	Seen string `json:"crop_seen,omitempty"`
	// Matches is nil when the question could not be settled -- no crop
	// declared, or footage that cannot identify one. Nil is not "no
	// mismatch", and the two must never be collapsed: one is silence and the
	// other is a finding about a person.
	Matches *bool `json:"crop_matches,omitempty"`
	// Confidence is how sure the model is of a mismatch, 0 to 1. Zero unless
	// Matches is false.
	Confidence float64 `json:"crop_mismatch_confidence,omitempty"`
	// Detail is what a person can check against the same video.
	Detail string `json:"crop_mismatch_detail,omitempty"`
}

// Mismatched reports whether the footage positively contradicts the
// declaration.
func (c CropCheck) Mismatched() bool { return c.Matches != nil && !*c.Matches }

// MaxClips bounds how many clips one assessment measures.
//
// The walk is four points; the cap is above that so a farmer who re-recorded a
// station is not cut off mid-walk, and bounded so a report with thirty clips
// on it cannot turn one assessment into thirty vision calls.
const MaxClips = 6

// Measure returns what the survey videos on a report show, one reading per
// directed point.
//
// # Why every clip and not one
//
// It measured the most recent clip alone, on the reasoning that the signal is
// about the parcel rather than any corner of it. That reasoning is exactly
// backwards: the platform sends the farmer to four points *because* corners
// differ, and measuring one of the four throws away the difference the walk
// was made to capture -- while the farmer still pays to upload all four over a
// village connection (docs/EVIDENCE_REVALIDATION.md, V1).
//
// Returns no clips and a nil error where the report has no video: an ordinary
// state, not a fault, and the caller reports it absent.
func (c *Client) Measure(
	ctx context.Context, reportID, cropName string,
) ([]Clip, error) {
	if c == nil || c.baseURL == "" {
		return nil, ErrUnavailable
	}
	if c.captures == nil {
		return nil, errors.New("no capture store is configured")
	}

	clips, err := c.findVideos(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if len(clips) == 0 {
		return nil, nil
	}

	var out []Clip
	var firstErr error
	for _, clip := range clips {
		body, contentType, err := c.readClip(ctx, clip.ID)
		if err != nil {
			// One unreadable clip must not cost the other three. The walk is
			// reported on what came back, and how many points that was.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		station := 0
		if clip.StationNo != nil {
			station = *clip.StationNo
		}

		res, err := c.post(ctx, body, contentType, cropName, station)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// The service labels an unvalidated measurement. Refusing here rather
		// than downstream keeps it out of the packet, the agent's reasoning
		// and the dossier in one place.
		if res.Baseline && !c.AllowBaseline {
			return nil, ErrBaselineRefused
		}

		out = append(out, Clip{
			StationNo: station,
			Crop: CropCheck{
				Declared:   res.CropDeclared,
				Seen:       res.CropSeen,
				Matches:    res.CropMatches,
				Confidence: res.CropMismatchConf,
				Detail:     res.CropMismatchWhy,
			},
			Measurements: groundcv.Measurements{
				StemAngleDegrees: res.StemAngleDegrees,
				Classes: groundcv.ClassPixels{
					HealthyFoliage: res.Classes.HealthyFoliage,
					Submerged:      res.Classes.Submerged,
					Necrotic:       res.Classes.Necrotic,
					Background:     res.Classes.Background,
				},
				Quality:           res.Quality,
				Keyframes:         res.Keyframes,
				CanopyHeightRatio: res.CanopyHeightRatio,
			},
		})
	}

	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// findVideos returns the clips to measure, in walk order.
//
// One per station, newest where a station was recorded more than once -- a
// farmer who redid a point meant the second take. Clips with no station (an
// older report, or the photograph path) come last and are bounded with the
// rest by MaxClips.
func (c *Client) findVideos(ctx context.Context, reportID string) ([]survey.Capture, error) {
	all, err := c.captures.ForReport(ctx, reportID)
	if err != nil {
		return nil, err
	}

	byStation := map[int]survey.Capture{}
	var unstationed []survey.Capture
	for i := range all {
		cap := all[i]
		if !survey.IsVideo(cap.ContentType) {
			continue
		}
		if cap.StationNo == nil {
			unstationed = append(unstationed, cap)
			continue
		}
		held, seen := byStation[*cap.StationNo]
		if !seen || cap.CapturedAt.After(held.CapturedAt) {
			byStation[*cap.StationNo] = cap
		}
	}

	stations := make([]int, 0, len(byStation))
	for n := range byStation {
		stations = append(stations, n)
	}
	sort.Ints(stations)

	out := make([]survey.Capture, 0, len(stations)+len(unstationed))
	for _, n := range stations {
		out = append(out, byStation[n])
	}
	// Newest first among the unstationed, so a bound that bites keeps the
	// most recent glance rather than the oldest.
	sort.Slice(unstationed, func(i, j int) bool {
		return unstationed[i].CapturedAt.After(unstationed[j].CapturedAt)
	})
	out = append(out, unstationed...)
	if len(out) > MaxClips {
		out = out[:MaxClips]
	}
	return out, nil
}

func (c *Client) readClip(ctx context.Context, id string) ([]byte, string, error) {
	rc, contentType, err := c.captures.Media(ctx, id)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	// Bounded: the row says how big it should be, but the bytes are what
	// arrive, and a reader that never ends would hold the assembler open for
	// its whole budget.
	body, err := io.ReadAll(io.LimitReader(rc, maxClip+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxClip {
		return nil, "", fmt.Errorf("capture %s is larger than the %d MB ceiling",
			id, maxClip>>20)
	}
	return body, contentType, nil
}

func (c *Client) post(
	ctx context.Context, clip []byte, contentType, cropName string, stationNo int,
) (*response, error) {
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="video"; filename="clip"`}
	h["Content-Type"] = []string{contentType}
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(clip); err != nil {
		return nil, err
	}
	// What the farmer declared, and which point of the walk this is.
	//
	// Both change what the footage means. Necrosis in a ripening cereal looks
	// like a healthy harvest, and a reading from one corner is not a reading
	// of the field -- a measurement made without either is a measurement made
	// with the wrong question. Written as form fields rather than added to
	// the clip, so a service that does not know them ignores them.
	if cropName != "" {
		if err := mw.WriteField("crop_name", cropName); err != nil {
			return nil, err
		}
	}
	if stationNo > 0 {
		if err := mw.WriteField("station_no", strconv.Itoa(stationNo)); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/vision/measure", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.token != "" {
		req.Header.Set("X-API-Key", c.token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// The load-bearing 404: no model on this deployment. Read as an absent
	// evidence source, the same as a radar module that is switched off.
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrUnavailable
	}

	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if res.StatusCode == http.StatusUnprocessableEntity {
		return nil, errors.New(
			"the survey video could not be measured: too few usable frames, " +
				"or the camera was not pointed at the crop")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the vision service answered %d", res.StatusCode)
	}

	var out response
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decoding the vision response: %w", err)
	}
	return &out, nil
}
