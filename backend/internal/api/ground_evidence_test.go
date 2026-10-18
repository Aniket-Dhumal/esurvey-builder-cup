package api

import (
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// What goes in front of the model, and what does not.
//
// A station clip used to be appended as an ai.ImageFieldPhoto: a twelve-
// megabyte data URI captioned "field photograph", which HasGroundImagery then
// counted as the ground evidence that unlocks a damage figure
// (docs/EVIDENCE_REVALIDATION.md, V3). The clips are measured by the vision
// pipeline instead; the panel sees the stills.
func TestAClipIsNotAPhotograph(t *testing.T) {
	for _, mime := range []string{"video/mp4", "video/webm", "video/quicktime"} {
		if !survey.IsVideo(mime) {
			t.Errorf("%s is not recognised as a clip, so it would reach the panel "+
				"as a photograph", mime)
		}
	}
	for _, mime := range []string{"image/jpeg", "image/png", "image/webp"} {
		if survey.IsVideo(mime) {
			t.Errorf("%s was taken for a clip; the panel would never see it", mime)
		}
	}
}
