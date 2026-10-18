package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/dossier"
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// The figure the report prints above the sampling table: the parcel with the
// four points on it, and one photograph from each.
//
// Every plate is an assertion about where a photograph was taken, which is
// why these tests are about the pairing rather than about the drawing. The
// pairing was broken for the whole of this feature's life -- the handset sent
// the station number with the clip and not with the still, so the office held
// four photographs belonging to no point -- and nothing failed. The map drew
// perfectly and printed nothing beside it.

func parcel() [][]dossier.MapPoint {
	const lat, lon = 18.1690, 75.8120
	return [][]dossier.MapPoint{{
		{Lat: lat, Lon: lon},
		{Lat: lat, Lon: lon + 0.00095},
		{Lat: lat + 0.0009, Lon: lon + 0.00095},
		{Lat: lat + 0.0009, Lon: lon},
		{Lat: lat, Lon: lon},
	}}
}

func f(v float64) *float64 { return &v }

// walked is coverage as it arrives once a walk has been recorded properly:
// four points, each with a position, an accuracy and its own photograph.
func walked() []survey.Coverage {
	out := make([]survey.Coverage, 0, 4)
	for i := 1; i <= 4; i++ {
		out = append(out, survey.Coverage{
			Station: survey.Station{
				StationNo: i,
				Lat:       18.1692 + float64(i)*0.0001,
				Lon:       75.8122 + float64(i)*0.0001,
			},
			Captured:   true,
			CaptureID:  fmt.Sprintf("clip-%d", i),
			CapturedAt: time.Date(2026, 9, 21, 7, 13, 0, 0, time.UTC),
			HasVideo:   true,
			Lat:        f(18.1692 + float64(i)*0.0001),
			Lon:        f(75.8122 + float64(i)*0.0001),
			AccuracyM:  f(6),
			SHA256:     fmt.Sprintf("sha-%d", i),
		})
	}
	return out
}

// stubStill stands in for the frame taken out of each clip. Keyed on the
// clip's own id, so a test can tell whether the right point's picture was
// printed beside the right point.
func stubStill(c survey.Coverage) (string, error) {
	return "data:image/jpeg;base64,/9j/" + c.CaptureID, nil
}

func TestAPhotographIsPrintedFromEveryPointWalked(t *testing.T) {
	s := &Server{}
	fig := s.walkFigureFrom(parcel(), walked(), nil, stubStill)
	if fig == nil {
		t.Fatal("no figure was drawn for a walk with four photographs")
	}
	if len(fig.Plates) != 4 {
		t.Fatalf("%d plates printed for a four-point walk", len(fig.Plates))
	}
	for i, p := range fig.Plates {
		want := fmt.Sprintf("Point %d", i+1)
		if p.Title.EN != want {
			t.Errorf("plate %d is titled %q, not %q", i, p.Title.EN, want)
		}
		if !strings.Contains(string(p.Src), fmt.Sprintf("clip-%d", i+1)) {
			t.Errorf("plate %d prints %q, which is not the frame from that "+
				"point's own clip", i, p.Src)
		}
		// The accuracy belongs beside the image: a frame recorded on a
		// thirty-metre fix may be of the neighbouring field, and the reader
		// looking at it is the person who should be told.
		if !strings.Contains(p.Meta.EN, "±6 m") {
			t.Errorf("plate %d does not say how well the handset knew where it "+
				"was: %q", i, p.Meta.EN)
		}
	}
}

func TestAPointWithNoPhotographPrintsNoPlate(t *testing.T) {
	// The station is still on the map -- a point nobody reached is part of the
	// record -- but nothing is printed for it, and the plates that do print
	// stay tied to their own points rather than sliding up a place.
	coverage := walked()
	coverage[1].CaptureID = ""
	coverage[1].StillID = ""

	s := &Server{}
	fig := s.walkFigureFrom(parcel(), coverage, nil, stubStill)
	if len(fig.Plates) != 3 {
		t.Fatalf("%d plates printed when one point had no photograph",
			len(fig.Plates))
	}
	for _, p := range fig.Plates {
		if p.Title.EN == "Point 2" {
			t.Error("a plate was printed for the point with no photograph")
		}
	}
	if !strings.Contains(string(fig.SVG), `class="suggested"`) {
		t.Error("the point with no photograph was dropped from the map too")
	}
}

func TestAPhotographThatWillNotLoadIsLeftOut(t *testing.T) {
	// Evidence the store cannot produce is a broken image in a document
	// somebody prints. The rest of the figure still stands.
	s := &Server{}
	fig := s.walkFigureFrom(parcel(), walked(), nil,
		func(c survey.Coverage) (string, error) {
			if c.Station.StationNo == 3 {
				return "", errors.New("no such object")
			}
			return stubStill(c)
		})
	if len(fig.Plates) != 3 {
		t.Fatalf("%d plates printed when one photograph was unreadable",
			len(fig.Plates))
	}
	if fig.SVG == "" {
		t.Error("the map was lost along with the photograph")
	}
}

func TestTheMapMarksWhereTheyStoodAgainstWhereTheyWereSent(t *testing.T) {
	// The whole point of the drawing. A farmer who filmed four clips from the
	// gate and one who crossed the field both fill the sampling table; only
	// the map tells them apart.
	coverage := walked()
	for i := range coverage {
		// Every clip recorded from one corner.
		coverage[i].Lat, coverage[i].Lon = f(18.16905), f(75.81205)
	}

	s := &Server{}
	svg := string(s.walkFigureFrom(parcel(), coverage, nil, stubStill).SVG)
	for _, want := range []string{
		`class="suggested"`, // the four points the plan placed
		`class="stood"`,     // the one place they stood
		`class="moved"`,     // and the distance between them, drawn
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the map does not show %s", want)
		}
	}
}

func TestNoBoundaryMeansNoFigure(t *testing.T) {
	// A scatter of dots with no field around them says less than the table it
	// would sit above.
	s := &Server{}
	if fig := s.walkFigureFrom(nil, walked(), nil, stubStill); fig != nil {
		t.Error("a figure was drawn for a parcel with no boundary")
	}
}

func TestEachPointsOwnReadingIsPrintedUnderItsOwnPhotograph(t *testing.T) {
	// The vision pipeline scores the four clips separately because they can
	// disagree, and it indexes them by station number. A walk whose second
	// clip never scored leaves a gap in that list, and the readings that do
	// exist must not slide up a place to fill it.
	scores := map[int]*groundcv.StationScore{
		1: {StationNo: 1, SCV: 12, LodgingClass: groundcv.LodgingErect,
			Keyframes: 8, CropPixelShare: 0.71},
		3: {StationNo: 3, SCV: 78, LodgingClass: groundcv.LodgingProstrate,
			Keyframes: 9, CropPixelShare: 0.66},
		4: {StationNo: 4, SCV: 40, LodgingClass: groundcv.LodgingSevere,
			Keyframes: 7, CropPixelShare: 0.08},
	}

	s := &Server{}
	fig := s.walkFigureFrom(parcel(), walked(), scores, stubStill)
	if len(fig.Plates) != 4 {
		t.Fatalf("%d plates", len(fig.Plates))
	}

	byTitle := map[string]dossier.Plate{}
	for _, p := range fig.Plates {
		byTitle[p.Title.EN] = p
	}

	if got := byTitle["Point 1"].Finding.EN; !strings.Contains(got, "crop erect") ||
		!strings.Contains(got, "12") {
		t.Errorf("point 1 reads %q; its own reading was 12, crop erect", got)
	}
	if got := byTitle["Point 3"].Finding.EN; !strings.Contains(got, "crop flattened") ||
		!strings.Contains(got, "78") {
		t.Errorf("point 3 reads %q; its own reading was 78, flattened", got)
	}

	// The point with no reading claims nothing rather than borrowing one.
	if got := byTitle["Point 2"].Finding; !got.Empty() {
		t.Errorf("point 2 was never scored yet prints %q", got.EN)
	}

	// A frame that was mostly sky leads with that, beside the image, because
	// the reader looking at it is the person who can see it is mostly sky.
	if got := byTitle["Point 4"].Finding.EN; !strings.Contains(got, "8% of the frame was crop") {
		t.Errorf("point 4 reads %q without warning that the frame held almost "+
			"no crop", got)
	}
}
