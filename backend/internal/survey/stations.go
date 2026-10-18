package survey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sampling stations: the four points a farmer is sent to.
//
// # What this is for
//
// Evidence chosen by the person being assessed is not a sample of the field,
// it is a selection from it. A farmer who films only the waterlogged corner
// turns a 30% loss into an 80% one, usually without intending to, because the
// wet patch is the part that feels worth showing. So the app stops accepting
// whatever is offered and says where to stand.
//
// # Why the server decides
//
// So the assessment report can state the farmer was *directed* to these
// points rather than choosing them. That sentence is what answers a
// selective-evidence challenge on appeal, and it is only true if the points
// existed before the walk -- and only provable if the server wrote them down.

// Station is one assigned point.
type Station struct {
	StationNo int     `json:"station_no"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`

	// low, mid_left, mid_right, high. Where Placement is "geometric" the ends
	// are simply the ends of the parcel's long axis: there was not enough
	// relief for one to be meaningfully lower than the other.
	Role string `json:"role"`

	// terrain, when the aspect gave a real downhill and station 1 is the end
	// water runs to; geometric otherwise.
	Placement string `json:"placement"`

	// RequiredSpacingM is how far this claim's walk must move between clips.
	//
	// The same on every station of a set: it is a property of the walk, not of a
	// point. Sent so the handset enforces the office's figure rather than
	// deriving one of its own, which is how a farmer came to record three clips
	// the office then refused.
	RequiredSpacingM float64 `json:"required_spacing_m,omitempty"`
}

// A parcel with no boundary yields ErrNoBoundary, declared in capture.go
// alongside the other errors of this package.
//
// It is not an internal fault. Live AgriStack returns plot_geometry: null for
// every farmer we can reach, so this is the *ordinary* state of a real parcel
// before anyone has walked it. The app reads it as "trace the boundary first",
// which is the only honest answer -- a geofence that does not exist cannot be
// walked to, and directing somebody to a point inside a polygon nobody has
// drawn is worse than admitting there is no polygon.

// Stations returns the four assigned points for a report, computing and
// freezing them on first call.
//
// # Why they are frozen
//
// Recomputing on every visit would mean the assigned points moved while the
// farmer walked between them, and "directed" would stop being true -- a point
// that changes after the walk began is not one anybody was sent to.
//
// # The exception, and why it is not a loophole
//
// A boundary that has itself changed. A station frozen against the old
// polygon can end up outside the corrected one, where the geofence will refuse
// the very capture the app is directing the farmer to take -- the app telling
// somebody to stand somewhere the app then rejects them for standing. So a
// station set is rebuilt when, and only when, the boundary it was computed
// against has moved. Every other call returns exactly what it returned before.
func (s *Store) Stations(
	ctx context.Context, reportID, farmID string, downhillDeg *float64,
) ([]Station, error) {
	out, _, err := s.stations(ctx, reportID, farmID, downhillDeg)
	return out, err
}

// StationsWithRebuild is Stations, and says whether the set was replaced.
//
// The caller audits a replacement. The report tells a reader that the farmer
// was directed to a fixed set of points, so the one operation that can move
// those points after the fact needs to leave a trace -- otherwise "fixed
// before the walk" rests on nothing an auditor could check.
//
// Replacement is legitimate and rare: it happens only when the parcel boundary
// itself has changed. That is exactly why it is worth recording rather than
// suppressing.
func (s *Store) StationsWithRebuild(
	ctx context.Context, reportID, farmID string, downhillDeg *float64,
) ([]Station, bool, error) {
	return s.stations(ctx, reportID, farmID, downhillDeg)
}

func (s *Store) stations(
	ctx context.Context, reportID, farmID string, downhillDeg *float64,
) ([]Station, bool, error) {
	boundaryAt, err := s.boundaryVersion(ctx, farmID)
	if err != nil {
		return nil, false, err
	}

	existing, frozenAt, err := s.frozenStations(ctx, reportID)
	if err != nil {
		return nil, false, err
	}
	if len(existing) == 4 && frozenAt.Equal(boundaryAt) {
		return existing, false, nil
	}

	// A first assignment is not a rebuild; there was nothing to replace.
	rebuilt := len(existing) > 0

	out, err := s.freezeStations(ctx, reportID, farmID, downhillDeg, boundaryAt)
	return out, rebuilt, err
}

// boundaryVersion is the parcel boundary's last change, which is what
// distinguishes a stale station set from a merely old one.
func (s *Store) boundaryVersion(ctx context.Context, farmID string) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT updated_at FROM parcel_boundaries WHERE farm_id = $1`,
		farmID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNoBoundary
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the parcel boundary: %w", err)
	}
	return at, nil
}

// Coverage is one assigned point and what came back from it.
type Coverage struct {
	Station Station

	// Captured is whether any evidence arrived from this point.
	Captured   bool
	CaptureID  string
	CapturedAt time.Time
	// OffsetM is how far from the assigned point the farmer actually stood.
	OffsetM *float64
	// HasVideo says the clip arrived, not only the still. A point with a
	// photograph and no clip is half a station, and the report should say so.
	HasVideo bool

	// Where the farmer actually stood, and how well the handset knew it.
	//
	// The offset above says how far that was from the assigned point; these
	// say where it was, which is what a map of the walk needs. The accuracy
	// is the handset's own estimate at the moment of recording -- 6 m under
	// open sky, 30 m under trees or beside a wall -- and drawing the point
	// without it would claim a precision the phone never had.
	Lat, Lon  *float64
	AccuracyM *float64

	// StillID is the photograph taken at this point, which the report prints.
	//
	// The walk takes a still and then a clip at each station, the still
	// first and deliberately -- it is a few hundred kilobytes against the
	// clip's twelve megabytes. It is therefore already stored, already
	// hashed, already carrying the position it was taken at, and it is a
	// better image than a frame pulled back out of a compressed video.
	StillID string

	// SHA256 is the clip's own content hash. The report's frame extraction
	// caches on it, so a frame can never be served for a different clip than
	// the one it came out of.
	SHA256 string
}

// CoverageFor reads where the farmer was directed and what came back.
//
// The record that answers a selective-evidence challenge: an officer at an
// appeal can see that the points were fixed before the walk, which ones were
// reached, and how far from each the farmer actually stood. It existed in the
// database and in the dossier's renderer, and nothing joined the two
// (docs/EVIDENCE_REVALIDATION.md, V2).
func (s *Store) CoverageFor(ctx context.Context, reportID string) ([]Coverage, error) {
	stations, _, err := s.frozenStations(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if len(stations) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT station_no, id::text, captured_at, station_offset_m,
		       content_type LIKE 'video/%', lat, lon, accuracy_m, sha256
		  FROM survey_captures
		 WHERE report_id = $1 AND station_no IS NOT NULL
		 ORDER BY station_no, captured_at`, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading what came back from each station: %w", err)
	}
	defer rows.Close()

	type arrival struct {
		id        string
		at        time.Time
		offset    *float64
		hasVideo  bool
		lat, lon  *float64
		accuracyM *float64
		stillID   string
		sha       string
	}
	got := map[int]arrival{}
	for rows.Next() {
		var n int
		var a arrival
		if err := rows.Scan(&n, &a.id, &a.at, &a.offset, &a.hasVideo,
			&a.lat, &a.lon, &a.accuracyM, &a.sha); err != nil {
			return nil, err
		}
		still := ""
		if !a.hasVideo {
			still = a.id
		}
		held, seen := got[n]
		// The clip is what a station is judged on; keep whichever arrival
		// carries it, and the earliest otherwise. The photograph's own id is
		// carried alongside either way, because that is what the report
		// prints.
		switch {
		case !seen:
			a.stillID = still
			got[n] = a
		case a.hasVideo && !held.hasVideo:
			a.stillID = held.stillID
			if a.stillID == "" {
				a.stillID = still
			}
			got[n] = a
		default:
			if held.stillID == "" {
				held.stillID = still
			}
			held.hasVideo = held.hasVideo || a.hasVideo
			got[n] = held
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Coverage, 0, len(stations))
	for _, st := range stations {
		c := Coverage{Station: st}
		if a, ok := got[st.StationNo]; ok {
			c.Captured = true
			c.CaptureID = a.id
			c.CapturedAt = a.at
			c.OffsetM = a.offset
			c.HasVideo = a.hasVideo
			c.Lat, c.Lon, c.AccuracyM = a.lat, a.lon, a.accuracyM
			c.StillID = a.stillID
			c.SHA256 = a.sha
		}
		out = append(out, c)
	}
	return out, nil
}

// RecordDirection freezes the points this report's walk was directed to, if
// they are not recorded already.
//
// Called when the first station capture lands. The app fetches the parcel's
// points before the walk -- placement is deterministic given a boundary, so
// these are the same four -- but until they are written against the report
// there is nothing to show that the farmer was directed rather than choosing.
//
// Never fails a capture: evidence that arrived is worth more than the note
// about where it was meant to come from.
func (s *Store) RecordDirection(ctx context.Context, reportID, farmID string) error {
	if reportID == "" || farmID == "" {
		return nil
	}
	existing, _, err := s.frozenStations(ctx, reportID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	_, err = s.Stations(ctx, reportID, farmID, nil)
	return err
}

func (s *Store) frozenStations(
	ctx context.Context, reportID string,
) ([]Station, time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT station_no, lat, lon, role, placement, boundary_updated_at,
		       COALESCE(required_spacing_m, 0)
		FROM report_sampling_stations
		WHERE report_id = $1
		ORDER BY station_no`, reportID)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("reading the assigned stations: %w", err)
	}
	defer rows.Close()

	var (
		out      []Station
		frozenAt time.Time
	)
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.StationNo, &st.Lat, &st.Lon,
			&st.Role, &st.Placement, &frozenAt,
			&st.RequiredSpacingM); err != nil {
			return nil, time.Time{}, fmt.Errorf("reading a station: %w", err)
		}
		out = append(out, st)
	}
	return out, frozenAt, rows.Err()
}

// freezeStations computes a set and writes it down, replacing any stale one.
//
// In a transaction because a half-replaced set is worse than either: four
// stations from two different boundaries is a walk that covers neither
// parcel properly, and nothing downstream could tell that had happened.
func (s *Store) freezeStations(
	ctx context.Context, reportID, farmID string,
	downhillDeg *float64, boundaryAt time.Time,
) ([]Station, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("assigning stations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM report_sampling_stations WHERE report_id = $1`,
		reportID); err != nil {
		return nil, fmt.Errorf("clearing stale stations: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT station_no, lat, lon, role, placement
		FROM parcel_sampling_stations($1, $2)
		ORDER BY station_no`, farmID, downhillDeg)
	if err != nil {
		return nil, fmt.Errorf("placing stations: %w", err)
	}

	var out []Station
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.StationNo, &st.Lat, &st.Lon,
			&st.Role, &st.Placement); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading a placed station: %w", err)
		}
		out = append(out, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("placing stations: %w", err)
	}

	// The function returns nothing for a parcel with no boundary. That is
	// already caught by boundaryVersion above, so reaching here with an empty
	// set means the geometry defeated the placement -- a degenerate polygon,
	// most likely -- and a report with fewer than four stations must not be
	// quietly created.
	if len(out) != 4 {
		return nil, fmt.Errorf(
			"placed %d stations for parcel %s, expected 4", len(out), farmID)
	}

	// How far this claim's walk must spread, frozen with the points it spreads
	// between.
	//
	// From the claim's own sown extent, not the parcel's area: a survey number is
	// routinely shared, and one farmer with 1 ha of soybean, the same farmer's
	// 1 ha of udad and a neighbour's 5 ha were all asked for the same fifteen
	// metres because all three sit on one polygon.
	//
	// Frozen rather than computed on demand for the same reason the points are:
	// the handset is given this figure before the walk and enforces it while
	// offline, so if the office recomputed its own the two could disagree -- and
	// a disagreement means a farmer recording something that is then refused.
	var spacing float64
	if err := tx.QueryRow(ctx, `
		SELECT walk_required_spacing($1, (
			SELECT max(i.area_sown_ha) FROM crop_loss_report_items i
			 WHERE i.report_id = $2::uuid AND i.farm_id = $1))`,
		farmID, reportID).Scan(&spacing); err != nil {
		return nil, fmt.Errorf("sizing the walk for parcel %s: %w", farmID, err)
	}

	for _, st := range out {
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_sampling_stations
				(report_id, station_no, lat, lon, role, placement,
				 boundary_updated_at, required_spacing_m)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			reportID, st.StationNo, st.Lat, st.Lon,
			st.Role, st.Placement, boundaryAt, spacing); err != nil {
			return nil, fmt.Errorf("recording station %d: %w", st.StationNo, err)
		}
	}
	for i := range out {
		out[i].RequiredSpacingM = spacing
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("assigning stations: %w", err)
	}
	return out, nil
}

// PlaceStations computes a parcel's four points without recording them.
//
// # Why this is separate from Stations
//
// Stations freezes a set against a report, which needs a report. The handset
// is offline-first: a draft lives on the phone until there is signal, so at
// the moment a farmer walks their field there is no report on the server to
// freeze anything against.
//
// Placement is deterministic given a boundary and an aspect, so the points do
// not depend on the report. The app fetches them when it fetches the parcel --
// a step that already needs a network -- and carries them into the walk. The
// server still placed them, still before the walk, which is what the report's
// claim of direction rests on.
//
// The captures come back naming a station number, and Stations records the
// frozen set when the report lands; the two agree because they ask the same
// function the same question.
// [sownHa] is the extent of the crop about to be walked, which is what decides
// how far apart the clips must be. The parcel cannot answer it: a survey number
// is routinely shared, so the same polygon carries one farmer's 1 ha of soybean,
// the same farmer's 1 ha of udad and a neighbour's 5 ha, and each of those walks
// is a different size. Zero where the app did not say, which falls back to the
// parcel rule rather than inventing an extent.
func (s *Store) PlaceStations(
	ctx context.Context, farmID string, downhillDeg *float64, sownHa float64,
) ([]Station, error) {
	if _, err := s.boundaryVersion(ctx, farmID); err != nil {
		return nil, err
	}

	var spacing float64
	if err := s.pool.QueryRow(ctx,
		`SELECT walk_required_spacing($1, $2)`,
		farmID, sownHa).Scan(&spacing); err != nil {
		return nil, fmt.Errorf("sizing the walk for parcel %s: %w", farmID, err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT station_no, lat, lon, role, placement
		FROM parcel_sampling_stations($1, $2)
		ORDER BY station_no`, farmID, downhillDeg)
	if err != nil {
		return nil, fmt.Errorf("placing stations: %w", err)
	}
	defer rows.Close()

	var out []Station
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.StationNo, &st.Lat, &st.Lon,
			&st.Role, &st.Placement); err != nil {
			return nil, fmt.Errorf("reading a placed station: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("placing stations: %w", err)
	}
	if len(out) != 4 {
		return nil, fmt.Errorf(
			"placed %d stations for parcel %s, expected 4", len(out), farmID)
	}
	for i := range out {
		out[i].RequiredSpacingM = spacing
	}
	return out, nil
}

// TakenPoint is a sampling point some other claim on a survey number already
// recorded.
//
// # Why the handset is told
//
// So it can refuse before the farmer records, rather than after they have walked
// home. A survey number is routinely shared -- a farmer's soybean and udad are
// two claims on it, and a neighbour's holding is a third -- and ground filmed for
// one is not independent evidence for another. The office refuses such a clip on
// arrival, but a refusal on arrival is a farmer's walk already spent: three clips
// of one trial claim were refused on every pass, and nothing the farmer could do
// from the queue would ever have changed it.
//
// Fetched with the four points, before the walk, so it works with no signal. A
// set that is out of date can only be incomplete, never wrong: a point recorded
// by somebody else after this was fetched is still caught by the office.
type TakenPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`

	// SameFarmer is true when it was this farmer's own other claim, which the
	// handset words differently: they are being sent to their other crop's
	// extent, not told a neighbour got there first.
	SameFarmer bool `json:"same_farmer"`
}

// TakenPoints are the sampling points already recorded on a parcel, other than
// by the report given.
//
// [exceptReport] is empty on the parcel route, where the claim does not exist on
// the server yet -- every point on the parcel then belongs to somebody else by
// definition, which is exactly what the walk needs to avoid.
// MaxTakenPoints bounds what is sent to a handset.
//
// A survey number accumulates points without limit: four per claim, several claims
// per owner, several seasons. Unbounded, this grows into a list the phone stores in
// a journey row and carries into every walk, and the newest points are the ones that
// matter -- an older season's clips are not evidence for this year's claim.
//
// Two hundred is fifty claims' worth, which is far more than a shared survey number
// carries in one season and small enough to sit in a request.
const MaxTakenPoints = 200

func (s *Store) TakenPoints(
	ctx context.Context, farmID, exceptReport, forFarmer string,
) ([]TakenPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ST_Y(c.geom), ST_X(c.geom), r.farmer_id = $3
		  FROM survey_captures c
		  JOIN crop_loss_report_items i ON i.report_id = c.report_id
		  JOIN crop_loss_reports r      ON r.id = c.report_id
		 WHERE i.farm_id = $1
		   AND c.geom IS NOT NULL
		   AND c.station_no IS NOT NULL
		   AND ($2 = '' OR c.report_id <> $2::uuid)
		   -- A withdrawn claim is not evidence of anything, so its ground is
		   -- free again. Without this a farmer who withdrew a claim and came
		   -- back to redo it was refused at every point of their own field by
		   -- their own abandoned walk -- and on a small plot, where the spacing
		   -- rule already holds the points close together, refused everywhere.
		   AND r.state <> 'withdrawn'
		 ORDER BY c.captured_at DESC NULLS LAST
		 LIMIT $4`,
		farmID, exceptReport, forFarmer, MaxTakenPoints)
	if err != nil {
		return nil, fmt.Errorf("reading the points already walked: %w", err)
	}
	defer rows.Close()

	out := []TakenPoint{}
	for rows.Next() {
		var p TakenPoint
		if err := rows.Scan(&p.Lat, &p.Lon, &p.SameFarmer); err != nil {
			return nil, fmt.Errorf("reading a walked point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
