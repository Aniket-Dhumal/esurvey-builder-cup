package api

import (
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

func capWithStation(n *int) survey.Capture {
	return survey.Capture{
		ID:          "cap-1",
		StationNo:   n,
		ContentType: "video/mp4",
		CapturedAt:  time.Now(),
	}
}

// Whether the panel is given anything it can read of the field.
//
// This gates the damage figure: verify.go discards a percentage produced
// without ground imagery, because Sentinel-2 at 10 m establishes that a crop
// grew and cannot measure how badly one was hurt.
//
// The walk records only video now. The still that used to be taken beside
// each clip is gone, so the only ground imagery an assessment can have is a
// frame lifted out of a clip -- and a walk of four points that yields no
// readable image is a walk that cannot produce a figure, however good the
// footage was.

func TestAKeyframeCountsAsGroundImageryAndAClipDoesNot(t *testing.T) {
	// The distinction the whole rule rests on. A clip sent as a photograph
	// once unlocked a damage figure from footage the panel was never built to
	// read; a frame out of that clip is an image it can read.
	frame := []ai.Image{{Kind: ai.ImageSurveyKeyfr, DataURI: "data:image/jpeg;base64,x"}}
	if !ai.HasGroundImagery(frame) {
		t.Error("a survey keyframe does not count as ground imagery, so a " +
			"video-only walk can never produce a damage figure")
	}

	photo := []ai.Image{{Kind: ai.ImageFieldPhoto, DataURI: "data:image/jpeg;base64,x"}}
	if !ai.HasGroundImagery(photo) {
		t.Error("a field photograph does not count as ground imagery")
	}
}

func TestSatelliteProductsNeverUnlockAFigure(t *testing.T) {
	// A colourised NDVI overlay is a satellite product, not a photograph of
	// the field. Counting it would let the satellite unlock the figure the
	// satellite is not allowed to produce.
	for _, kind := range []string{
		ai.ImageNDVIMid, ai.ImageNDVILast,
		ai.ImageTerrainSlope, ai.ImageTerrainAspect,
	} {
		imgs := []ai.Image{{Kind: kind, DataURI: "data:image/png;base64,x"}}
		if ai.HasGroundImagery(imgs) {
			t.Errorf("%s was counted as ground imagery", kind)
		}
	}
}

func TestAKeyframeCaptionSaysWhichPointItCameFrom(t *testing.T) {
	// The panel weighs a still from a directed point differently from a
	// photograph the farmer chose to take, and can only do that if the
	// caption says which it is looking at.
	n := 3
	got := keyframeCaption(capWithStation(&n))
	for _, want := range []string{"frame", "point 3"} {
		if !contains(got, want) {
			t.Errorf("caption %q does not say %q", got, want)
		}
	}

	// A clip carrying no point still says it is a frame rather than claiming
	// a point it does not have.
	loose := keyframeCaption(capWithStation(nil))
	if contains(loose, "point ") {
		t.Errorf("a clip with no station number claimed one: %q", loose)
	}
	if !contains(loose, "frame") {
		t.Errorf("caption %q does not say it is a frame", loose)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
