package api

import "encoding/json"

// centroidOf returns a point on the parcel, from GeoJSON Polygon or
// MultiPolygon coordinates.
//
// This is the area-weighted centroid of the largest ring, which for a strongly
// concave plot can fall just outside it. That is acceptable here and only
// here: the sole consumer is the weather lookup, whose reanalysis grid is
// about eleven kilometres across. Metres do not change the answer; what
// changes the answer is having no point at all, which sends the specialist
// looking at the wrong district or not running.
//
// Do not reuse this for containment. Geofencing a capture against a plot is a
// question about the actual ring, and PostGIS answers it exactly.
func centroidOf(raw json.RawMessage) (lat, lon float64, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}

	// MultiPolygon first: [][][][ ]float64. A Polygon parses as the inner
	// shape, so trying the deeper type first distinguishes them without
	// needing the geometry's declared type, which the two upstream structs
	// spell differently.
	var multi [][][][]float64
	if err := json.Unmarshal(raw, &multi); err == nil {
		best := 0.0
		for _, poly := range multi {
			if len(poly) == 0 {
				continue
			}
			if y, x, a := ringCentroid(poly[0]); a > best {
				lat, lon, best = y, x, a
			}
		}
		return lat, lon, best > 0
	}

	var poly [][][]float64
	if err := json.Unmarshal(raw, &poly); err != nil || len(poly) == 0 {
		return 0, 0, false
	}
	y, x, a := ringCentroid(poly[0])
	return y, x, a > 0
}

// ringCentroid is the shoelace centroid of a closed ring of [lon, lat] pairs,
// with the unsigned doubled area so callers can pick the largest ring.
func ringCentroid(ring [][]float64) (lat, lon, area float64) {
	if len(ring) < 3 {
		return 0, 0, 0
	}
	var cx, cy, a2 float64
	for i := range ring {
		p, q := ring[i], ring[(i+1)%len(ring)]
		if len(p) < 2 || len(q) < 2 {
			return 0, 0, 0
		}
		cross := p[0]*q[1] - q[0]*p[1]
		a2 += cross
		cx += (p[0] + q[0]) * cross
		cy += (p[1] + q[1]) * cross
	}
	if a2 == 0 {
		// A degenerate or collinear ring. Averaging the vertices is still a
		// point on the plot, which is all the weather lookup needs.
		for _, p := range ring {
			cx += p[0]
			cy += p[1]
		}
		n := float64(len(ring))
		return cy / n, cx / n, 0
	}
	return cy / (3 * a2), cx / (3 * a2), abs(a2 / 2)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
