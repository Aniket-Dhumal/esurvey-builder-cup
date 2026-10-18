package dossier

import "time"

// ist is the timezone the report is read in.
//
// A hailstorm at 16:40 local is not a useful fact rendered as 11:10 UTC on a
// document somebody signs. internal/report keeps its own copy for the same
// reason; duplicating a fixed offset is cheaper than exporting it and tying
// the two packages together.
var ist = time.FixedZone("IST", 5*60*60+30*60)

// day renders a date the way both halves of the report print it.
func day(t time.Time) string { return t.In(ist).Format("02-01-2006") }
