package dossier

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
)

// The map an officer looks at before reading anything else.
//
// A drawing is believed more readily than a table, which is the reason to
// have one and the reason to be careful: a dot in the wrong place, or a scale
// bar in the wrong units, is a false statement about where a farmer stood.

// A square parcel about 100 m on a side, near the trial's own latitude.
func square() [][]MapPoint {
	const (
		lat0 = 18.1690
		lon0 = 75.8120
		dLat = 0.0009 // ~100 m
		dLon = 0.00095
	)
	return [][]MapPoint{{
		{Lat: lat0, Lon: lon0},
		{Lat: lat0, Lon: lon0 + dLon},
		{Lat: lat0 + dLat, Lon: lon0 + dLon},
		{Lat: lat0 + dLat, Lon: lon0},
		{Lat: lat0, Lon: lon0},
	}}
}

func at(lat, lon float64) *MapPoint { return &MapPoint{Lat: lat, Lon: lon} }

func accuracy(m float64) *float64 { return &m }

func TestAnEmptyMapDrawsNothing(t *testing.T) {
	var m *FieldMap
	if !m.Empty() || m.SVG() != "" {
		t.Fatal("a nil map drew something")
	}
	empty := &FieldMap{}
	if !empty.Empty() || empty.SVG() != "" {
		t.Fatal("a map with no boundary drew something")
	}
}

func TestThePointsAndTheirCirclesAreDrawn(t *testing.T) {
	m := &FieldMap{
		Rings: square(),
		Stations: []MapStation{{
			StationNo: 1,
			Suggested: MapPoint{Lat: 18.1694, Lon: 75.8124},
			Actual:    at(18.1694, 75.8124),
			AccuracyM: accuracy(8),
		}},
	}
	svg := string(m.SVG())

	for _, want := range []string{
		`class="parcel"`,    // the field
		`class="suggested"`, // where the plan wanted a sample
		`class="stood"`,     // where they were
		`class="accuracy"`,  // what the handset knew
		`class="coverage"`,  // what a clip plausibly shows
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the map is missing %s", want)
		}
	}
}

func TestAPointWithNoPositionIsNotInvented(t *testing.T) {
	// A point nobody sampled is part of the record: the suggestion is drawn,
	// and nothing pretends to know where anyone stood.
	m := &FieldMap{
		Rings: square(),
		Stations: []MapStation{{
			StationNo: 3,
			Suggested: MapPoint{Lat: 18.1694, Lon: 75.8124},
		}},
	}
	svg := string(m.SVG())

	if !strings.Contains(svg, `class="suggested"`) {
		t.Error("the suggested point was dropped")
	}
	for _, unwanted := range []string{`class="stood"`, `class="accuracy"`, `class="coverage"`} {
		if strings.Contains(svg, unwanted) {
			t.Errorf("a station with no recorded position drew %s", unwanted)
		}
	}
}

func TestAPointOutsideTheBoundaryStaysOnTheDrawing(t *testing.T) {
	// The case an officer most needs to see. Fitting the view to the parcel
	// alone would push it off the edge and quietly hide it.
	outside := at(18.1730, 75.8180) // well north-east of the square
	m := &FieldMap{
		Rings: square(),
		Stations: []MapStation{{
			StationNo: 2,
			Suggested: MapPoint{Lat: 18.1694, Lon: 75.8124},
			Actual:    outside,
			AccuracyM: accuracy(10),
		}},
	}
	svg := string(m.SVG())

	x, y, ok := firstCircle(svg, `class="stood"`)
	if !ok {
		t.Fatal("the recorded position was not drawn")
	}
	if x < 0 || x > 520 || y < 0 || y > 340 {
		t.Fatalf("the point was drawn off the canvas at %.1f,%.1f", x, y)
	}
}

func TestTheScaleBarIsInMetres(t *testing.T) {
	// It read the drawing's width in pixels as though it were metres, which
	// prints a bar labelled "100 m" at whatever length happened to fit.
	m := &FieldMap{Rings: square()}
	svg := string(m.SVG())

	label, ok := between(svg, `class="scaletext"`, ">", "</text>")
	if !ok {
		t.Fatal("no scale bar")
	}
	var metres float64
	if _, err := fmtSscan(label, &metres); err != nil {
		t.Fatalf("scale label %q is not a distance", label)
	}

	x1, x2, ok := lineEnds(svg, `class="scalebar"`)
	if !ok {
		t.Fatal("no scale bar line")
	}
	drawn := x2 - x1

	// Against the parcel as actually drawn, not against the canvas: this
	// square is ~100 m on a side and the fit is limited by the shorter
	// dimension, so assuming it fills the width would be measuring the bar
	// against a parcel that was never that wide.
	parcelDrawn, ok := pathWidth(svg)
	if !ok {
		t.Fatal("the parcel was not drawn")
	}
	const parcelMetres = 100.5 // dLon at this latitude
	want := metres / parcelMetres * parcelDrawn
	if math.Abs(drawn-want) > want*0.12 {
		t.Fatalf("a %.0f m bar was drawn %.0f units; ~%.0f expected "+
			"against a parcel drawn %.0f units wide",
			metres, drawn, want, parcelDrawn)
	}
}

// --- small readers, so the assertions above stay about the drawing ---------

func fmtSscan(label string, out *float64) (int, error) {
	return fmt.Sscanf(strings.TrimSpace(label), "%f m", out)
}

func between(s, anchor, open, close string) (string, bool) {
	i := strings.Index(s, anchor)
	if i < 0 {
		return "", false
	}
	j := strings.Index(s[i:], open)
	if j < 0 {
		return "", false
	}
	start := i + j + len(open)
	k := strings.Index(s[start:], close)
	if k < 0 {
		return "", false
	}
	return s[start : start+k], true
}

var numAttr = regexp.MustCompile(`(c?x1?|c?y1?|x2|y2)="(-?[0-9.]+)"`)

// firstCircle returns the centre of the first circle carrying a class.
func firstCircle(svg, class string) (x, y float64, ok bool) {
	i := strings.Index(svg, class)
	if i < 0 {
		return 0, 0, false
	}
	start := strings.LastIndex(svg[:i], "<circle")
	end := strings.Index(svg[i:], "/>")
	if start < 0 || end < 0 {
		return 0, 0, false
	}
	for _, m := range numAttr.FindAllStringSubmatch(svg[start:i+end], -1) {
		v, err := strconvParse(m[2])
		if err != nil {
			continue
		}
		switch m[1] {
		case "cx":
			x = v
		case "cy":
			y = v
		}
	}
	return x, y, true
}

// lineEnds returns the x extent of the first line carrying a class.
func lineEnds(svg, class string) (x1, x2 float64, ok bool) {
	i := strings.Index(svg, class)
	if i < 0 {
		return 0, 0, false
	}
	start := strings.LastIndex(svg[:i], "<line")
	end := strings.Index(svg[i:], "/>")
	if start < 0 || end < 0 {
		return 0, 0, false
	}
	for _, m := range numAttr.FindAllStringSubmatch(svg[start:i+end], -1) {
		v, err := strconvParse(m[2])
		if err != nil {
			continue
		}
		switch m[1] {
		case "x1":
			x1 = v
		case "x2":
			x2 = v
		}
	}
	return x1, x2, true
}

func strconvParse(s string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	return v, err
}

// pathWidth is how wide the parcel outline was actually drawn.
func pathWidth(svg string) (float64, bool) {
	d, ok := between(svg, `class="parcel"`, `d="`, `"`)
	if !ok {
		return 0, false
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, token := range strings.FieldsFunc(d, func(r rune) bool {
		return r == 'M' || r == 'L' || r == 'Z'
	}) {
		var x, y float64
		if _, err := fmt.Sscanf(strings.TrimSpace(token), "%f %f", &x, &y); err != nil {
			continue
		}
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
	}
	if math.IsInf(minX, 1) {
		return 0, false
	}
	return maxX - minX, true
}

func TestTheRingsAreCutAtTheBoundary(t *testing.T) {
	// A ring spilling over the boundary draws a claim about the neighbour's
	// land. Only the parcel is this assessment's to speak for.
	m := &FieldMap{
		Rings: square(),
		Stations: []MapStation{{
			StationNo: 1,
			Suggested: MapPoint{Lat: 18.1694, Lon: 75.8124},
			// Right on the corner, so both rings certainly overhang.
			Actual:    at(18.1690, 75.8120),
			AccuracyM: accuracy(30),
		}},
	}
	svg := string(m.SVG())

	if !strings.Contains(svg, `<clipPath id="parcel-clip">`) {
		t.Fatal("no clip path was defined")
	}
	for _, class := range []string{"coverage", "accuracy"} {
		ring, ok := between(svg, `class="`+class+`"`, `clip-path="`, `"`)
		if !ok {
			t.Errorf("the %s ring is not clipped to anything", class)
			continue
		}
		if ring != "url(#parcel-clip)" {
			t.Errorf("the %s ring is clipped to %q", class, ring)
		}
	}
}

func TestWhereTheyStoodIsNeverClippedAway(t *testing.T) {
	// The one mark worth catching. A farmer who recorded from outside their
	// own boundary must stay visible; clipping the dot to the parcel would
	// delete exactly the evidence of that.
	outside := at(18.1730, 75.8180)
	m := &FieldMap{
		Rings: square(),
		Stations: []MapStation{{
			StationNo: 2,
			Suggested: MapPoint{Lat: 18.1694, Lon: 75.8124},
			Actual:    outside,
			AccuracyM: accuracy(10),
		}},
	}
	svg := string(m.SVG())

	for _, class := range []string{"stood", "suggested", "moved"} {
		i := strings.Index(svg, `class="`+class+`"`)
		if i < 0 {
			t.Errorf("%s was not drawn at all", class)
			continue
		}
		// The element this class sits on, up to its close.
		end := strings.Index(svg[i:], "/>")
		if end > 0 && strings.Contains(svg[i:i+end], "clip-path") {
			t.Errorf("%s was clipped to the parcel and would vanish for a "+
				"point recorded outside it", class)
		}
	}
}
