package dossier

import (
	"fmt"

	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
)

// The line printed under each photograph in the walk figure.
//
// # Why the per-point reading belongs under the picture
//
// The vision pipeline scores each of the four clips separately and keeps the
// results in Score.Stations, precisely because four points can disagree: a
// field 70% damaged at one end and untouched at the other is a different claim
// from one uniformly damaged, and the total hides which it was. Those
// per-point readings were computed and printed nowhere.
//
// Under the photograph is where they answer something. A reader looking at a
// frame of flattened crop and reading "severe lodging" beside it is checking
// the machine against their own eyes, which is the only check on this evidence
// anybody can actually perform. The same reader looking at a frame that is
// mostly sky needs to be told the score behind it rests on very little, and
// needs to be told it there rather than in a note four pages down.

// StationFinding is what the vision pipeline measured at one point.
//
// Returns an empty Text where there is no reading for the point, so the plate
// prints the photograph with its position and claims nothing further. A
// station whose clip was never scored is an ordinary state -- an unreadable
// upload, a walk assessed before the pipeline ran -- and inventing a finding
// for it would be the one unrecoverable error in a document like this.
func StationFinding(s *groundcv.StationScore) Text {
	if s == nil || s.Keyframes == 0 {
		return Text{}
	}

	// The share of the frame that was crop comes first when it is small.
	// Every ratio behind the score is taken over that area, so a score from a
	// lens pointed at the bund reads exactly like a score from a lens pointed
	// at the crop, and only this number tells them apart.
	if s.CropPixelShare < 0.25 {
		return Tf(
			"चित्रचौकटीत केवळ %.0f%% पीक; या ठिकाणचे मोजमाप (%.0f) थोड्या "+
				"क्षेत्रावरून",
			"only %.0f%% of the frame was crop; this point's reading (%.0f) "+
				"rests on that little",
			s.CropPixelShare*100, s.SCV,
		)
	}

	band := lodgingWord(s.LodgingClass)
	if band.Empty() {
		return Tf(
			"या ठिकाणचे मोजमाप %.0f · चित्रचौकटीत %.0f%% पीक",
			"reading at this point %.0f · %.0f%% of frame was crop",
			s.SCV, s.CropPixelShare*100,
		)
	}
	// Written out rather than through Tf: Tf shares one argument list across
	// both halves, which is right for numbers and wrong for a word that has a
	// translation.
	return Text{
		MR: fmt.Sprintf("या ठिकाणचे मोजमाप %.0f · %s · चित्रचौकटीत %.0f%% पीक",
			s.SCV, band.MR, s.CropPixelShare*100),
		EN: fmt.Sprintf("reading at this point %.0f · %s · %.0f%% of frame was crop",
			s.SCV, band.EN, s.CropPixelShare*100),
	}
}

// lodgingWord is the lodging class without its angle band.
//
// lodgingBand prints the degrees because it stands in a table's reference
// column, where the band is what the reading is being compared against. Under
// a photograph there is no comparison being made and no room for one: the
// word is what a reader checks against the picture.
func lodgingWord(class string) Text {
	switch class {
	case groundcv.LodgingErect:
		return T("पीक उभे", "crop erect")
	case groundcv.LodgingMild:
		return T("किंचित आडवे", "mild lodging")
	case groundcv.LodgingSevere:
		return T("गंभीर आडवे", "severe lodging")
	case groundcv.LodgingProstrate:
		return T("पूर्ण आडवे", "crop flattened")
	}
	return Text{}
}

// StationScores indexes a score's per-point readings by station number.
//
// Stations carry their own number rather than their position in the list: a
// walk where the second clip failed to upload produces three readings, and
// printing the third point's finding under the second point's photograph is
// the kind of error a report is never forgiven.
func StationScores(g *groundcv.Score) map[int]*groundcv.StationScore {
	if g == nil || len(g.Stations) == 0 {
		return nil
	}
	out := make(map[int]*groundcv.StationScore, len(g.Stations))
	for i := range g.Stations {
		if n := g.Stations[i].StationNo; n > 0 {
			out[n] = &g.Stations[i]
		}
	}
	return out
}
