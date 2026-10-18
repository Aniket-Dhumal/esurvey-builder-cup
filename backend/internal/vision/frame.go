package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// One frame out of a clip, for the assessment report to print.
//
// # Why the frame is taken here and not sent by the phone
//
// The walk used to record a still photograph and then a clip at each point,
// and upload both: twelve megabytes of video plus a few hundred kilobytes of
// photograph, eight files, over a village connection. The clip already contains
// that frame. Taking it on this side costs the farmer nothing, halves the
// number of files their phone has to push, and removes a whole class of bug --
// the still and the clip were two records of one moment that could disagree
// about where they were taken, and for the entire life of the feature the still
// arrived carrying no station number at all.
//
// # Why it is cached and keyed on the clip's own hash
//
// An assessment report printed today and printed at an appeal next year must be
// the same page. The extraction is deterministic for a given clip, so caching
// is not what makes it reproducible -- but re-decoding a twelve megabyte video
// every time somebody opens a report is a cost with no benefit, and the cache
// key being the clip's content hash means a frame can never be served for a
// different clip than the one it came out of.

// framePrefix is where extracted frames live in the evidence store.
//
// A separate prefix from the captures themselves, because these are derived:
// nothing here was observed by anybody, and a directory of frames must not be
// mistakable for a directory of evidence.
const framePrefix = "frames/"

// maxFrame bounds a returned frame. A JPEG of one video frame is tens of
// kilobytes; a megabyte is a wide margin and a bound all the same.
const maxFrame = 4 << 20

// ErrNoFrame means the clip yielded no decodable frame.
//
// A finding about the file rather than a fault on either side: the report
// prints that point without a picture instead of failing to print.
var ErrNoFrame = errors.New("vision: no frame could be decoded from this clip")

// Frames is the evidence store, for caching what has been extracted.
//
// Declared here rather than taking blob.Store so a deployment without a cache
// still works: Frame falls back to extracting every time.
type Frames interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// WithFrameCache gives the client somewhere to keep extracted frames.
func (c *Client) WithFrameCache(f Frames) *Client {
	if c != nil {
		c.frames = f
	}
	return c
}

// Frame returns one still from a stored clip, as a data URI.
//
// The data URI rather than a URL: the report is read offline, printed, and
// attached to an appeal file, and a link to a server is not evidence in a year.
//
// Returns ErrNoFrame where the clip cannot be decoded, and ErrUnavailable where
// no vision service is configured. Both are ordinary: the figure on the page
// does not depend on this picture.
func (c *Client) Frame(ctx context.Context, captureID, sha256 string) (string, error) {
	if c == nil || c.baseURL == "" {
		return "", ErrUnavailable
	}
	if c.captures == nil {
		return "", errors.New("no capture store is configured")
	}

	key := framePrefix + sha256 + ".jpg"
	if c.frames != nil && sha256 != "" {
		if rc, err := c.frames.Get(ctx, key); err == nil {
			defer rc.Close()
			if cached, err := io.ReadAll(io.LimitReader(rc, maxFrame+1)); err == nil &&
				len(cached) > 0 && len(cached) <= maxFrame {
				return dataURI(cached), nil
			}
			// A cached object that will not read is not worth a second
			// thought: the clip is still there and extracting again is cheap
			// beside being wrong.
		}
	}

	clip, contentType, err := c.readClip(ctx, captureID)
	if err != nil {
		return "", err
	}

	jpeg, err := c.extractFrame(ctx, clip, contentType)
	if err != nil {
		return "", err
	}

	// Best effort, and silent. A frame that could not be cached is still a
	// frame, and the caller is rendering a document rather than debugging a
	// store.
	if c.frames != nil && sha256 != "" {
		_ = c.frames.Put(ctx, key, "image/jpeg", jpeg)
	}
	return dataURI(jpeg), nil
}

func dataURI(jpeg []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
}

// extractFrame asks the vision service for one frame.
//
// Takes a slot from the same gate the measurements use: decoding a video is
// the expensive part of both, and a report being printed must not be able to
// push an assessment out of the way.
func (c *Client) extractFrame(
	ctx context.Context, clip []byte, contentType string,
) ([]byte, error) {
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
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/vision/frame", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnprocessableEntity:
		return nil, ErrNoFrame
	case http.StatusNotFound:
		// A service too old to have this endpoint. Not a fault: the report
		// prints without pictures, as it did before they existed.
		return nil, ErrUnavailable
	default:
		return nil, fmt.Errorf("%w: the vision service answered %d",
			ErrUnavailable, resp.StatusCode)
	}

	jpeg, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
	if err != nil {
		return nil, err
	}
	if len(jpeg) == 0 {
		return nil, ErrNoFrame
	}
	if len(jpeg) > maxFrame {
		return nil, fmt.Errorf("the vision service returned more than %d MB "+
			"for one frame", maxFrame>>20)
	}
	return jpeg, nil
}
