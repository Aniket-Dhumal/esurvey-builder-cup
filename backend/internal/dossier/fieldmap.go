package dossier

import (
	"fmt"
	"html/template"
	"math"
	"strings"
)

// A map of the walk: the parcel, the four points, and where the farmer stood.
//
// # Why a picture
//
// The sampling section already says, in a table, that four points were fixed
// before the walk and how far from each the farmer actually stood. A table
// answers "was this representative?" only for a reader willing to hold four
// coordinates in their head. An officer deciding a claim looks at a shape and
// four dots and knows in a second whether the evidence came from across the
// field or from one corner of it.
//
// # What is drawn, and what each mark claims
//
//   - The parcel outline, from the boundary the assessment used.
//   - The suggested point, as a small ring: where the system placed it. The
//     farmer is not required to stand there, so the ring is where the plan
//     wanted a sample and not where anybody was sent.
//   - Where they actually stood, as a filled dot joined to the suggested
//     point by a line when the two differ.
//   - An accuracy circle around each dot: the handset's own estimate of how
//     well it knew that position. This is a fact the phone recorded.
//   - A coverage ring, fainter and dashed: roughly how much field a person
//     filming from that spot can show. This is an assumption, not a
//     measurement -- nothing in the record says what the lens saw -- so it is
//     drawn differently from everything else and the legend says so.
//
// Anything the map cannot honestly draw, it omits: a station with no recorded
// position gets no dot rather than a guessed one.

// clipID names the clip path the rings are cut to.
//
// Fixed rather than generated: one figure per document, and a stable id keeps
// the markup diffable and the tests readable.
const clipID = "parcel-clip"

// CoverageAssumptionM is the radius the coverage ring is drawn at.
//
// Twenty metres. A fifteen-second clip panned across a field shows perhaps
// that far before detail is useless for judging a crop, which is a judgement
// and not a measurement -- hence the dashed line and the legend. It is
// deliberately the same figure as the NDVI sampling buffer, so that the ring
// an officer sees is the ground the index was read over.
const CoverageAssumptionM = 20.0

// MapPoint is one place on the map.
type MapPoint struct {
	Lat, Lon float64
}

// MapStation is one suggested point and what happened at it.
type MapStation struct {
	StationNo int

	// Suggested is where the system placed the point. The farmer is not
	// required to stand there.
	Suggested MapPoint

	// Actual is where the capture was recorded, when one arrived with a
	// position. Nil otherwise, and then nothing is drawn for it.
	Actual *MapPoint

	// AccuracyM is the handset's own estimate at that moment.
	AccuracyM *float64
}

// FieldMap is everything the drawing needs.
type FieldMap struct {
	// Rings are the parcel's outline, in order, each closed.
	Rings [][]MapPoint

	Stations []MapStation
}

// Empty reports whether there is nothing worth drawing.
func (m *FieldMap) Empty() bool {
	return m == nil || len(m.Rings) == 0
}

// SVG renders the map, sized to the page.
//
// Inline SVG rather than a raster: the document is printed, and a printed
// raster of a field boundary at 96 dpi is a smudge. It also keeps the
// renderer dependency-free, which matters for a binary that runs in a
// distroless image.
func (m *FieldMap) SVG() template.HTML {
	if m.Empty() {
		return ""
	}

	const (
		width  = 520.0
		height = 340.0
		pad    = 28.0
	)

	// A local flat projection. Over a parcel a few hundred metres across the
	// earth's curvature is far below the width of the line being drawn, and
	// anything more would be false precision dressed as rigour.
	var sumLat float64
	var n int
	for _, ring := range m.Rings {
		for _, p := range ring {
			sumLat += p.Lat
			n++
		}
	}
	if n == 0 {
		return ""
	}
	midLat := sumLat / float64(n)
	mx := 111_320 * math.Cos(midLat*math.Pi/180)
	my := 110_540.0

	type xy struct{ x, y float64 }
	project := func(p MapPoint) xy {
		return xy{x: p.Lon * mx, y: -p.Lat * my}
	}

	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	note := func(q xy) {
		minX, maxX = math.Min(minX, q.x), math.Max(maxX, q.x)
		minY, maxY = math.Min(minY, q.y), math.Max(maxY, q.y)
	}
	for _, ring := range m.Rings {
		for _, p := range ring {
			note(project(p))
		}
	}
	// The rings alone can leave a point that was recorded outside the
	// boundary off the edge of the drawing -- which is exactly the case an
	// officer most needs to see.
	for _, st := range m.Stations {
		note(project(st.Suggested))
		if st.Actual != nil {
			note(project(*st.Actual))
		}
	}

	spanX, spanY := maxX-minX, maxY-minY
	if spanX <= 0 {
		spanX = 1
	}
	if spanY <= 0 {
		spanY = 1
	}
	scale := math.Min((width-2*pad)/spanX, (height-2*pad)/spanY)

	at := func(p MapPoint) xy {
		q := project(p)
		return xy{
			x: pad + (q.x-minX)*scale + (width-2*pad-spanX*scale)/2,
			y: pad + (q.y-minY)*scale + (height-2*pad-spanY*scale)/2,
		}
	}
	// Metres to drawing units, for the circles.
	metres := func(m float64) float64 { return m * scale }

	var b strings.Builder
	fmt.Fprintf(&b,
		`<svg class="fieldmap" viewBox="0 0 %.0f %.0f" role="img" `+
			`xmlns="http://www.w3.org/2000/svg">`, width, height)

	var outline strings.Builder
	for _, ring := range m.Rings {
		if len(ring) < 3 {
			continue
		}
		for i, p := range ring {
			q := at(p)
			if i == 0 {
				fmt.Fprintf(&outline, "M%.1f %.1f", q.x, q.y)
				continue
			}
			fmt.Fprintf(&outline, "L%.1f %.1f", q.x, q.y)
		}
		outline.WriteString("Z")
	}
	if outline.Len() == 0 {
		return ""
	}

	// The rings are clipped to the parcel.
	//
	// A ring spilling over the boundary draws a claim about somebody else's
	// land: whatever it says about coverage or accuracy, the only ground this
	// assessment may speak for is inside that line. Clipped, the shape answers
	// the question an officer actually has -- how much of *this* field could
	// that spot show -- and a farmer who stood at the edge sees a crescent
	// rather than a circle half over the neighbour's crop.
	//
	// Only the rings. The dot and the line to it are never clipped: a farmer
	// who recorded from outside their own boundary must stay visible, and
	// clipping the mark that shows it would hide the one thing here worth
	// catching.
	fmt.Fprintf(&b, `<clipPath id="%s"><path d="%s"/></clipPath>`,
		clipID, outline.String())
	fmt.Fprintf(&b, `<path class="parcel" d="%s"/>`, outline.String())

	for _, st := range m.Stations {
		a := at(st.Suggested)

		if st.Actual != nil {
			c := at(*st.Actual)

			// The assumption, drawn first so the facts sit on top of it.
			fmt.Fprintf(&b,
				`<circle class="coverage" clip-path="url(#%s)" `+
					`cx="%.1f" cy="%.1f" r="%.1f"/>`,
				clipID, c.x, c.y, metres(CoverageAssumptionM))

			if st.AccuracyM != nil && *st.AccuracyM > 0 {
				fmt.Fprintf(&b,
					`<circle class="accuracy" clip-path="url(#%s)" `+
						`cx="%.1f" cy="%.1f" r="%.1f"/>`,
					clipID, c.x, c.y, metres(*st.AccuracyM))
			}

			// The line from the suggestion to where they stood. Drawn
			// only when it is visible at this scale; a two-metre line under
			// the dot is noise.
			if math.Hypot(c.x-a.x, c.y-a.y) > 4 {
				fmt.Fprintf(&b, `<line class="moved" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
					a.x, a.y, c.x, c.y)
			}
			fmt.Fprintf(&b, `<circle class="stood" cx="%.1f" cy="%.1f" r="4"/>`, c.x, c.y)
			fmt.Fprintf(&b, `<text class="pointno" x="%.1f" y="%.1f">%d</text>`,
				c.x, c.y-9, st.StationNo)
		}

		// The suggested point is always drawn, recorded or not: a point
		// nobody sampled is part of the record.
		fmt.Fprintf(&b, `<circle class="suggested" cx="%.1f" cy="%.1f" r="3.5"/>`, a.x, a.y)
		if st.Actual == nil {
			fmt.Fprintf(&b, `<text class="pointno missed" x="%.1f" y="%.1f">%d</text>`,
				a.x, a.y-8, st.StationNo)
		}
	}

	// A scale bar, because every distance above is otherwise unreadable.
	bar := niceBar(scale, width-2*pad)
	if bar.metres > 0 {
		x0, y0 := pad, height-12.0
		fmt.Fprintf(&b, `<line class="scalebar" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
			x0, y0, x0+metres(bar.metres), y0)
		fmt.Fprintf(&b, `<text class="scaletext" x="%.1f" y="%.1f">%s</text>`,
			x0, y0-4, bar.label)
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // every value is a number this function computed
}

type scaleBar struct {
	metres float64
	label  string
}

// niceBar picks the largest round distance that fits comfortably.
//
// scale is drawing units per metre; available is how much width the bar may
// occupy. Both are needed: a bar chosen from the drawing's width alone is a
// bar in the wrong units, which on a field map is worse than no bar at all.
func niceBar(scale, available float64) scaleBar {
	if scale <= 0 || available <= 0 {
		return scaleBar{}
	}
	for _, m := range []float64{200, 100, 50, 25, 10, 5} {
		if m*scale <= available*0.4 {
			return scaleBar{metres: m, label: fmt.Sprintf("%.0f m", m)}
		}
	}
	return scaleBar{}
}
