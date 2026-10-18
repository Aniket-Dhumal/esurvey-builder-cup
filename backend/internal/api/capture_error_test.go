package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// A photograph the fence refuses is a verdict, not a failure.
//
// These fell through to a 500, which the handset reads as "the office is
// down, try again": a photograph taken away from the field was resent on
// every pass for ever, and the farmer never learned they had to walk back
// into their own field.
func TestAFencedPhotographIsRefusedNotFailed(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("%w: 329731 m from the boundary", survey.ErrOutsideParcel), "OUTSIDE_PARCEL"},
		{survey.ErrMockedPosition, "MOCKED_POSITION"},
		{survey.ErrDuplicate, "UNUSABLE_MEDIA"},
	} {
		rec := httptest.NewRecorder()
		s.writeCaptureError(rec, c.err)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422", c.err, rec.Code)
			continue
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != c.code {
			t.Errorf("%v: code %q, want %q", c.err, body.Error.Code, c.code)
		}
	}
}
