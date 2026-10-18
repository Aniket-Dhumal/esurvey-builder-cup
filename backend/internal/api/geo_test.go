package api

import (
	"encoding/json"
	"math"
	"testing"
)

// About a centimetre on the ground. The shoelace sum accumulates rounding at
// the fourteenth digit of a degree coordinate, and a tolerance below that
// tests the float unit, not the centroid.
const tol = 1e-7

func TestCentroidOfPolygon(t *testing.T) {
	// A one-hundredth-degree square near Solapur.
	raw := json.RawMessage(`[[[75.90,17.65],[75.91,17.65],[75.91,17.66],[75.90,17.66],[75.90,17.65]]]`)
	lat, lon, ok := centroidOf(raw)
	if !ok {
		t.Fatal("a valid square produced no centroid")
	}
	if math.Abs(lat-17.655) > tol || math.Abs(lon-75.905) > tol {
		t.Fatalf("centroid = %.6f, %.6f; want 17.655000, 75.905000", lat, lon)
	}
}

func TestCentroidOfMultiPolygonTakesLargestRing(t *testing.T) {
	// Two disjoint plots: a tiny one first, a large one second. The weather
	// lookup should follow the land, not the array order.
	raw := json.RawMessage(`[
		[[[75.00,17.00],[75.001,17.00],[75.001,17.001],[75.00,17.001],[75.00,17.00]]],
		[[[76.00,18.00],[76.10,18.00],[76.10,18.10],[76.00,18.10],[76.00,18.00]]]
	]`)
	lat, lon, ok := centroidOf(raw)
	if !ok {
		t.Fatal("a valid multipolygon produced no centroid")
	}
	if math.Abs(lat-18.05) > tol || math.Abs(lon-76.05) > tol {
		t.Fatalf("centroid = %.6f, %.6f; want the larger plot at 18.05, 76.05", lat, lon)
	}
}

func TestCentroidOfRejectsUnusable(t *testing.T) {
	// Nothing here is a point on any parcel. Reporting one would send the
	// weather specialist to the Gulf of Guinea with full confidence.
	for name, raw := range map[string]json.RawMessage{
		"empty":     nil,
		"not json":  json.RawMessage(`{`),
		"a point":   json.RawMessage(`[75.90,17.65]`),
		"two verts": json.RawMessage(`[[[75.90,17.65],[75.91,17.65]]]`),
	} {
		if _, _, ok := centroidOf(raw); ok {
			t.Errorf("%s: reported a centroid it cannot have", name)
		}
	}
}
