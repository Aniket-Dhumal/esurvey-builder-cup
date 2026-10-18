// Package survey owns the farmer's own evidence: what they photographed, where
// they were standing, and what the gates made of it.
//
// # Why this exists
//
// The assessment agent refuses to state a damage percentage without ground
// imagery, because Sentinel-2 at 10 m over a smallholding cannot measure how
// badly a crop was hurt. Until a capture exists, every assessment refuses.
// This package is what lets one arrive.
//
// # What the gates are worth
//
// They make casual fraud hard and they record what was checked, so an officer
// weighs a capture rather than taking it on faith. They do not make forgery
// impossible: a rooted handset can lie about position. The posture is that
// this evidence supports a human decision and does not replace one, and the
// gate results are surfaced to the officer rather than folded into a score
// they cannot interrogate.
package survey

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Errors.
var (
	ErrNotFound    = errors.New("capture not found")
	ErrNoBoundary  = errors.New("the parcel has no recorded boundary")
	ErrBadMedia    = errors.New("unusable media")
	ErrTooLarge    = errors.New("the image is too large")
	ErrDuplicate   = errors.New("this exact photograph has already been submitted")
	ErrOutOfWindow = errors.New("the capture is dated outside the event window")

	// The two refusals. Every other gate records what it found and lets an
	// officer weigh it; these stop the capture.
	//
	// Outside the parcel is a decision taken deliberately: a farmer may only
	// evidence a loss from the land they are claiming for. It costs the
	// farmer whose registry boundary is wrong, which is why a parcel with no
	// boundary is not fenced at all and why tracing exists.
	ErrOutsideParcel = errors.New("the capture was taken outside the parcel")

	// A fence that stopped honest farmers and waved through anyone who
	// installed a location-spoofing app would be worse than no fence, because
	// it would look like a control while being none.
	ErrMockedPosition = errors.New("the position came from a mock provider")

	// ErrSamePosition means the walk did not move.
	//
	// The directed walk exists because corners differ. Four clips from one
	// spot are one reading filed four times, and they look like a walk on
	// every screen that counts them. The farmer is not asked to stand on an
	// assigned point -- see MinStationSpacingM -- only to be somewhere else
	// in the same field.
	ErrSamePosition = errors.New(
		"this point of the walk was recorded where another one already was")

	// ErrPointTakenInSurvey means another claim on this survey number already
	// filmed this ground.
	//
	// A survey number is routinely larger than, and shared by more than, the
	// farmer filing. One farmer with 1 ha of soybean and 1 ha of udad files two
	// claims on it; a neighbour with 5 ha of soybean files a third. Each is
	// entitled to four clips of their own extent, and none of them is entitled
	// to the same patch of ground as another -- filmed twice, it is one reading
	// counted as two independent samples, in two different assessments.
	//
	// Separate from ErrSamePosition because the remedy is different and the
	// farmer needs to hear which: not "walk further within this claim" but "this
	// is not your share of the field, go to yours".
	ErrPointTakenInSurvey = errors.New(
		"another claim on this survey number has already recorded this point")

	// ErrAwayFromField means a parcel with no recorded outline had its
	// evidence gathered in two different places.
	//
	// Without a boundary there is nothing to fence against, so the claim's
	// own first capture becomes the field: everything after it must be within
	// UnmappedFieldRadiusM of it. It does not prove the place is the farmer's
	// parcel -- nothing can until the outline is walked -- but it stops one
	// claim's evidence being gathered in two villages.
	ErrAwayFromField = errors.New(
		"this capture is far from where this claim's other evidence was taken")
)

// MaxBytes bounds one photograph.
//
// Eight megabytes is a generous phone photo and a hard ceiling on what one
// farmer on a village connection is asked to upload. Larger images are
// rejected with a message rather than silently truncated.
const MaxBytes = 8 << 20

// MaxVideoBytes bounds one clip.
//
// A short walk across a damaged plot is evidence a still cannot carry: it
// shows extent and continuity, and it is far harder to stage than a
// photograph. Sixty megabytes is roughly a minute of phone video at a
// sensible bitrate -- enough for that walk, and still something a village
// connection can finish.
const MaxVideoBytes = 60 << 20

// acceptedMedia is every content type a capture may carry, and the ceiling
// for each. A type that is not here is refused by name rather than stored and
// discovered later by whatever tries to display it.
var acceptedMedia = map[string]int{
	"image/jpeg": MaxBytes,
	"image/png":  MaxBytes,
	"image/webp": MaxBytes,
	"video/mp4":  MaxVideoBytes,
	"video/webm": MaxVideoBytes,
	// iOS records QuickTime by default; refusing it would refuse the iPhone.
	"video/quicktime": MaxVideoBytes,
}

// IsVideo reports whether a content type is a clip rather than a still.
//
// Load-bearing beyond presentation: the vision specialist is given stills, and
// a clip has to be reduced to keyframes before it can be reasoned over.
func IsVideo(contentType string) bool {
	return strings.HasPrefix(contentType, "video/")
}

// GeofenceBufferM is documented in the migration; repeated here because the
// number appears in the message a farmer reads.
const GeofenceBufferM = 25

// MinStationSpacingM is how far apart two points of the walk must be.
//
// # What the walk is for, and what it is not for
//
// It is for evidence from more than one part of the field: a claim resting on
// a field 70% damaged at one end and untouched at the other is a different
// claim from one resting on a field damaged throughout, and only several
// positions can tell them apart.
//
// It is not for making a farmer stand on a surveyed point. The app refused to
// record until the handset was within a few metres of an assigned coordinate,
// which in a wet field, under tree cover, or on a phone with a poor fix is a
// farmer standing in the mud watching a disabled button. The assigned points
// remain as suggestions of where to go; what is enforced is that the next
// clip is not taken where the last one was.
//
// Fifteen metres is short enough to walk in twenty seconds on the smallest
// plot this platform sees -- a tenth of a hectare is about 32 m across -- and
// long enough that a handset's own position error cannot produce it while
// standing still.
const MinStationSpacingM = 15

// SmallestStationSpacingM is the spread asked of the smallest plots.
//
// Measured against a real village: of 437 mapped parcels in Malegaon, three
// are under thirty metres across and the smallest is nine. Asking for fifteen
// metres there is asking a farmer to stand outside their own field, which the
// geofence then refuses -- a rule that contradicts itself on the plots least
// able to argue with it.
//
// Five metres is past a handset's own error and inside every plot here.
const SmallestStationSpacingM = 5

// SpacingFor is how far the walk must move on a parcel of this size.
//
// Exported because the assessment report states the figure. The document is
// the only place a farmer or an appellate officer learns what the walk was
// actually required to do, and a report that says "spread across the parcel"
// without saying how far apart is asking to be taken on trust.
//
// A third of the plot's span, bounded by the two constants above: four points
// a third of a span apart sit across a field rather than in one corner of it,
// and on a large parcel the cap keeps the walk from becoming a hike. An
// unmapped parcel -- no polygon, no area -- gets the ordinary figure, which is
// what the app suggests too.
func SpacingFor(areaM2 float64) float64 {
	if areaM2 <= 0 {
		return MinStationSpacingM
	}
	span := math.Sqrt(areaM2) / 3
	return min(max(span, SmallestStationSpacingM), MinStationSpacingM)
}

// UnmappedFieldRadiusM bounds a claim whose parcel has no outline.
//
// AgriStack returns no geometry for any farmer we can reach, so a parcel
// nobody has walked has no fence at all, and until now that meant a claim's
// photographs could be taken anywhere on earth. They still cannot be checked
// against the parcel -- there is none to check against -- but they can be
// required to come from one place: the first capture anchors the field and
// the rest must be near it.
//
// A hundred and fifty metres is wider than almost every smallholding here, so
// it does not fence a farmer inside their own field; it fences a claim to one
// field.
const UnmappedFieldRadiusM = 150

// Kind is what a capture is a photograph of.
type Kind string

const (
	KindWideShot      Kind = "wide_shot"
	KindDamageCloseup Kind = "damage_closeup"
	KindCropCloseup   Kind = "crop_closeup"
	KindBoundaryPoint Kind = "boundary_point"

	// A clip recorded at one of the four assigned sampling stations. Distinct
	// from the other kinds because it is the only one the farmer did not
	// choose the location of -- which is exactly what makes it worth
	// something in an appeal.
	KindStationVideo Kind = "station_video"
)

// ValidKind reports whether k is a known capture kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindWideShot, KindDamageCloseup, KindCropCloseup, KindBoundaryPoint,
		KindStationVideo:
		return true
	}
	return false
}

// Gate names. Strings rather than an enum because they travel to the officer's
// screen and into the audit detail, where a name reads and a number does not.
const (
	GateInsideParcel  = "inside_parcel"
	GatePositionFix   = "position_fix"
	GateWithinWindow  = "within_incident_window"
	GateAfterIncident = "after_the_incident"
	GateFreshCapture  = "captured_near_upload"
	GateDistinctImage = "distinct_image"
	GateGenuineFix    = "genuine_position"
	GateMovedOn       = "distinct_position"
	GateSameField     = "one_field"
	GateOwnExtent     = "own_extent"
)

// GateExplanations are what an officer reads beside a failed gate.
var GateExplanations = map[string]string{
	GateInsideParcel:  "the photograph was taken inside the plot it claims to show",
	GatePositionFix:   "the handset had a usable position fix",
	GateWithinWindow:  "the photograph was taken inside the event's incident window",
	GateAfterIncident: "the photograph was taken after the loss occurred",
	GateFreshCapture:  "the photograph was taken close to when it was uploaded",
	GateDistinctImage: "the photograph has not been submitted before",
	GateGenuineFix:    "the position came from the handset's own satellite fix, not a mock provider",
	GateMovedOn:       "this point of the walk was recorded somewhere the walk had not already been",
	GateSameField:     "the capture was taken where this claim's other evidence was taken",
	GateOwnExtent:     "this point is not ground another claim on the same survey number already filmed",
}

// Capture is one photograph and everything known about it.
type Capture struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`
	LineNo   *int   `json:"line_no,omitempty"`

	Kind    Kind   `json:"kind"`
	Caption string `json:"caption,omitempty"`

	ContentType string `json:"content_type"`
	ByteSize    int    `json:"byte_size"`
	SHA256      string `json:"sha256"`

	CapturedAt  time.Time `json:"captured_at"`
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	AccuracyM   *float64  `json:"accuracy_m,omitempty"`
	HeadingDeg  *float64  `json:"heading_deg,omitempty"`
	DeviceLabel string    `json:"device_label,omitempty"`
	UploadedAt  time.Time `json:"uploaded_at"`

	InsideParcel        *bool    `json:"inside_parcel"`
	DistanceFromParcelM *float64 `json:"distance_from_parcel_m"`
	GatesPassed         []string `json:"gates_passed"`
	GatesFailed         []string `json:"gates_failed"`

	// StationNo is which of the directed points this was taken at, and
	// StationOffsetM how far from it the farmer actually stood. Both were
	// recorded from the first walk and read by nothing, so an officer could
	// not tell whether a claim rested on four points or one
	// (docs/EVIDENCE_REVALIDATION.md, V8).
	StationNo      *int     `json:"station_no,omitempty"`
	StationOffsetM *float64 `json:"station_offset_m,omitempty"`
	DurationMs     *int     `json:"duration_ms,omitempty"`

	// GuidanceAskedFor is what the live assistant asked the farmer to show in this
	// clip, in the words they heard. Read so the assessment document can say a clip
	// was a directed observation rather than the farmer's own selection of what to
	// show -- which is the distinction that answers a selective-evidence challenge,
	// and one a reader cannot make unless the document states it.
	//
	// Empty for most clips, and that is ordinary: the assistant needs a network and
	// most walks do not have one.
	GuidanceAskedFor string `json:"guidance_asked_for,omitempty"`
	GuidanceModel    string `json:"guidance_model,omitempty"`

	CapturedBy string    `json:"captured_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`

	// Replayed is set when these bytes were already on the report and this
	// submission found them. Describes the response, never serialised.
	Replayed bool `json:"-"`
}

// Trusted reports whether every gate passed.
//
// Not a score. A capture that failed a gate is not discarded -- it is shown to
// the officer with the reason, because a farmer standing on the bund of their
// own field is outside the polygon and is not a fraudster.
func (c *Capture) Trusted() bool { return len(c.GatesFailed) == 0 }

// disqualifyingGates are the failures that mean a photograph is not evidence
// of this loss on this parcel.
//
// The distinction matters and is easy to get wrong. "Worth flagging to an
// officer" and "unsafe to put in front of the model" are different bars, and
// treating every gate as disqualifying withholds a perfectly good photograph
// because it was uploaded a week late -- which is exactly the farmer this
// system exists for, filing from a village with no signal.
//
// Only two failures actually mislead:
//
//   - inside_parcel: the photograph shows a different field, and the model has
//     no way to know that.
//   - after_the_incident: the photograph predates the damage, so whatever it
//     shows is not the damage.
//
// The rest are provenance caveats. They travel to the model in the caption, so
// it is told the photograph is a week old rather than being shown it as though
// it were fresh, and they travel to the officer, who decides what to make of
// them.
var disqualifyingGates = map[string]bool{
	GateInsideParcel:  true,
	GateAfterIncident: true,
	GateGenuineFix:    true,
	// Two clips from one spot are one reading counted twice, and a claim
	// gathered in two places is not a claim about one field. Both are refused
	// on arrival, so neither normally reaches a stored capture; they are
	// listed so that anything already stored is not shown to the model as a
	// walk it was not.
	GateMovedOn:   true,
	GateSameField: true,
	// A survey number is routinely shared -- one farmer's soybean and udad are
	// two claims on it, and a neighbour's holding is a third. Ground filmed for
	// one of them is not independent evidence for another, so a capture that
	// failed this must not be shown to the model as though it sampled this
	// claim's own extent.
	GateOwnExtent: true,
}

// UsableAsEvidence reports whether a capture may be put in front of the model.
func (c *Capture) UsableAsEvidence() bool {
	for _, g := range c.GatesFailed {
		if disqualifyingGates[g] {
			return false
		}
	}
	return true
}

// Caveats are the non-disqualifying gate failures, as sentences.
//
// These become part of what the model is told about a photograph. A model
// shown a ten-day-old image and told it is ten days old can reason about that;
// one shown it without the caveat cannot.
func (c *Capture) Caveats() []string {
	var out []string
	for _, g := range c.GatesFailed {
		if disqualifyingGates[g] {
			continue
		}
		switch g {
		case GateFreshCapture:
			out = append(out, "uploaded some days after it was taken")
		case GatePositionFix:
			out = append(out, "the handset position fix was poor")
		case GateWithinWindow:
			out = append(out, "taken outside the notified incident window")
		case GateDistinctImage:
			out = append(out, "the same image was submitted more than once")
		default:
			out = append(out, g)
		}
	}
	return out
}

// The gate predicates, as pure functions.
//
// Extracted from runGates so they can be tested without a database. The
// database part of a gate is the geofence and the duplicate check; these four
// are arithmetic on a timestamp, and they are the ones whose boundaries decide
// whether a farmer's photograph is treated as evidence.

// MaxAccuracyM is the worst position fix that can still place someone in a
// plot. A one-hectare field is about 100 m across, so a fix worse than this
// cannot say which side of the boundary the handset was on.
const MaxAccuracyM = 100.0

// FreshnessWindow is how long after capture an upload is still called fresh.
//
// Seven days because that is roughly how long a village can be without signal
// after the weather that caused the loss -- the exact situation offline
// capture exists for. Exceeding it is a caveat, never a refusal.
const FreshnessWindow = 7 * 24 * time.Hour

// IncidentGrace is how far before the recorded incident time a photograph may
// still have been taken.
//
// An hour, because the incident timestamp is when a farmer says the hail
// started, recorded later from memory. Demanding that every photograph
// postdate it exactly would fail honest captures on a rounding error.
const IncidentGrace = time.Hour

// ValidPosition reports whether a capture carries a real position.
//
// Null island is refused along with anything out of range: 0,0 is a point in
// the Atlantic and the usual shape of an uninitialised reading, and a
// photograph claiming it is not one worth geofencing.
func ValidPosition(lat, lon float64) bool {
	if lat == 0 && lon == 0 {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// PositionFixUsable reports whether an accuracy reading can place a handset
// inside a plot.
func PositionFixUsable(accuracyM float64) bool {
	return accuracyM > 0 && accuracyM <= MaxAccuracyM
}

// WithinWindow reports whether a capture falls on or after the event's
// incident window opened.
//
// Compared by calendar day in UTC, because the window is recorded as a date
// and a photograph taken at 06:00 local on the opening day must not fail for
// being 00:30 UTC.
func WithinWindow(capturedAt time.Time, windowStart string) bool {
	return capturedAt.UTC().Format("2006-01-02") >= windowStart
}

// AfterIncident reports whether a photograph was taken after the loss.
//
// The one unambiguous check here: a photograph of damage taken before the
// damage happened is not a photograph of that damage, whatever else is true
// about it. That is why it disqualifies rather than caveats.
func AfterIncident(capturedAt, incidentAt time.Time) bool {
	return !capturedAt.Before(incidentAt.Add(-IncidentGrace))
}

// FreshCapture reports whether a photograph was uploaded close to when it was
// taken.
func FreshCapture(capturedAt, now time.Time) bool {
	return now.Sub(capturedAt) <= FreshnessWindow
}

// New is a submitted capture, before the gates have run.
type New struct {
	ReportID string
	LineNo   *int
	FarmID   string

	Kind    Kind
	Caption string

	ContentType string
	Media       io.Reader

	CapturedAt  time.Time
	Lat, Lon    float64
	AccuracyM   *float64
	HeadingDeg  *float64
	DeviceLabel string

	CapturedBy string

	// Which of the four assigned stations this evidences, and how far from it
	// the farmer actually stood. Nil for a capture that is not part of the
	// directed walk.
	//
	// The offset is kept even though the farmer is never asked to justify
	// moving: an officer reading "station 4, captured 40 m short" knows what
	// they are looking at, which is the whole point of having assigned the
	// point in the first place.
	StationNo      *int
	StationOffsetM *float64

	// EncodingProfile, EncodingFPS and EncodingBitrateBPS are what the handset's
	// encoder agreed to, which is not always what it was asked for: a Vivo V2253
	// asked for 2.5 Mbit/s recorded at 8.5, so a claim came to 65 MB instead of
	// 18. Stored so an operator can tell a large claim caused by a handset from
	// one caused by a longer walk.
	EncodingProfile    *string
	EncodingFPS        *int
	EncodingBitrateBPS *int

	// GuidanceAskedFor is what the live assistant asked the farmer to show in this
	// clip, in the words they heard, and GuidanceModel is what said it.
	//
	// Nil for most clips: every one recorded before the assistant existed, every
	// one from a build without it, and every one on a farm whose connection could
	// not carry a session. Null means nothing was asked -- never that a clip is
	// missing something. What makes a claim complete is the walk, decided offline.
	GuidanceAskedFor *string
	GuidanceModel    *string

	// Length of the clip. Nil for a still.
	DurationMs *int

	// Whether the handset reported the fix as coming from a mock provider.
	// Nil from a client too old to say -- which must not read as false, since
	// "did not tell us" and "told us it was genuine" are different facts.
	IsMocked *bool

	// IncidentAt and the event window, for the timing gates.
	IncidentAt  time.Time
	WindowStart string
	WindowEnd   string
}

// Store persists captures and their media.
//
// The rows live in Postgres and the bytes live wherever blob.Store puts them:
// a directory in development, an encrypted regional bucket in anything real.
// media_path holds the key, which is the same string either way, so moving
// between them does not rewrite a single row.
type Store struct {
	pool  *db.Pool
	blobs blob.Store

	// log is optional: a store built without one simply says less.
	log *slog.Logger

	// assembling bounds how many chunked uploads are stitched together at
	// once. See AssemblyLimit.
	assembling chan struct{}
}

// AssemblyLimit is how many uploads may be assembled at the same moment.
//
// Assembly streams: the parts are read one at a time to hash them, and again
// to store them, so memory per assembly is one chunk. What the limit bounds now
// is I/O -- each assembly is two passes over up to sixty megabytes of the
// evidence store -- so a burst on the morning after an event queues for a few
// seconds instead of saturating the store, and a request that waits too long
// is told to come back, which the handset does.
//
// It once held the whole file in memory, twice: four together measured 956 MiB
// against a 512 MiB container (docs/SYNC_SCALE_REVIEW.md, C2).
const AssemblyLimit = 4

// AssemblyWait is how long a finishing chunk waits for an assembly slot before
// the server answers busy. Well under the handset's upload timeout, so the
// answer arrives as a 503 it can act on rather than as a dropped connection.
const AssemblyWait = 20 * time.Second

// NewStore builds a store over a connection pool and an evidence store.
func NewStore(pool *db.Pool, blobs blob.Store) *Store {
	return &Store{pool: pool, blobs: blobs, assembling: make(chan struct{}, AssemblyLimit)}
}

// WithLogger attaches a logger, for the few things worth saying that are not
// worth failing a capture over.
func (s *Store) WithLogger(l *slog.Logger) *Store {
	s.log = l
	return s
}

// Evidence returns the underlying store, for readiness reporting.
func (s *Store) Evidence() blob.Store { return s.blobs }

// Submit stores a capture, runs the gates and records what they found.
//
// For media that arrives whole in one request -- a photograph, bounded at a few
// megabytes. A clip arrives in chunks and is recorded by finishUpload, which
// streams it.
func (s *Store) Submit(ctx context.Context, in New) (*Capture, error) {
	limit, err := checkNew(in)
	if err != nil {
		return nil, err
	}

	// Read once, hash and buffer. Bounded above, so holding one in memory is
	// cheaper than a temp file and a second pass.
	body, err := io.ReadAll(io.LimitReader(in.Media, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadMedia, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: the image is empty", ErrBadMedia)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%w: %d MiB is the limit for %s",
			ErrTooLarge, limit>>20, in.ContentType)
	}

	sum := sha256.Sum256(body)
	return s.record(ctx, in, hex.EncodeToString(sum[:]), int64(len(body)),
		func(ctx context.Context, key string) error {
			return s.blobs.Put(ctx, key, in.ContentType, body)
		})
}

// checkNew validates what a capture says about itself, before any bytes.
// Returns the size limit for its media type.
func checkNew(in New) (int, error) {
	if !ValidKind(in.Kind) {
		return 0, fmt.Errorf("%w: unknown capture kind %q", ErrBadMedia, in.Kind)
	}
	limit, ok := acceptedMedia[in.ContentType]
	if !ok {
		return 0, fmt.Errorf("%w: %s is not a media type this system accepts",
			ErrBadMedia, in.ContentType)
	}
	if in.CapturedAt.IsZero() {
		return 0, fmt.Errorf("%w: the capture has no timestamp", ErrBadMedia)
	}

	// A position is required at submission. The database no longer enforces
	// it -- migration 0010 made lat and lon nullable so a capture can be
	// forgotten -- so the requirement lives here, where the difference
	// between "never had one" and "no longer has one" is knowable.
	//
	// Zero, zero is rejected as well as out-of-range: it is a real point in
	// the Atlantic and the usual shape of an uninitialised reading, and a
	// capture that claims it is not one anyone should geofence.
	if !ValidPosition(in.Lat, in.Lon) {
		return 0, fmt.Errorf(
			"%w: the capture has no usable position", ErrBadMedia)
	}
	return limit, nil
}

// record runs the gates on media already hashed and sized, stores it with put,
// and records the capture. Shared by a photograph held in memory and a clip
// streamed from its chunks.
func (s *Store) record(
	ctx context.Context, in New, hash string, size int64,
	put func(ctx context.Context, key string) error,
) (*Capture, error) {

	// The same bytes already on this report are this photograph, sent again
	// because the reply to the first send was lost. Answered with the capture
	// already stored -- before the gates, which would otherwise read the
	// farmer's own retry as one photograph submitted twice.
	if existing, err := s.onReport(ctx, in.ReportID, hash); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	gates := runGates(ctx, s.pool, in, hash)

	// Refused here, before the media is stored, so a rejected capture leaves
	// nothing behind to reclaim.
	//
	// The handset checks both of these too and should never send one. This is
	// the copy that counts: a client-side fence is a suggestion, and the one
	// client that matters is the one someone has modified.
	if gates.inside != nil && !*gates.inside {
		return nil, fmt.Errorf("%w: %.0f m from the boundary",
			ErrOutsideParcel, *gates.distance)
	}
	if in.IsMocked != nil && *in.IsMocked {
		return nil, ErrMockedPosition
	}
	if gates.nearestOther != nil && *gates.nearestOther < gates.spacing {
		return nil, fmt.Errorf("%w: %.0f m from the last one, and this crop "+
			"needs %.0f m", ErrSamePosition, *gates.nearestOther, gates.spacing)
	}
	if gates.takenBy != nil {
		return nil, takenError(gates.takenBy)
	}
	if gates.away != nil && *gates.away > UnmappedFieldRadiusM {
		return nil, fmt.Errorf("%w: %.0f m from this claim's other evidence",
			ErrAwayFromField, *gates.away)
	}

	// Stored before the row, so a row can never point at media that does not
	// exist. The reverse leaves an orphan object, which is space to reclaim
	// rather than a broken reference an officer runs into mid-verification.
	//
	// The key is the content hash, so the same photograph submitted twice
	// occupies one object -- and a key can never be chosen by a caller.
	rel := hash[:2] + "/" + hash + extensionFor(in.ContentType)
	if err := put(ctx, rel); err != nil {
		return nil, fmt.Errorf("storing media: %w", err)
	}

	// The check above and the insert below, made atomic for this report and
	// these bytes. Two copies of one retry arriving together -- the app's
	// foreground timer and its background worker -- would otherwise both find
	// nothing and both insert. A transaction-scoped advisory lock on the pair
	// serialises exactly those two and nothing else.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`,
		in.ReportID, hash); err != nil {
		return nil, fmt.Errorf("serialising a capture: %w", err)
	}
	var already string
	switch err := tx.QueryRow(ctx, `
		SELECT id FROM survey_captures WHERE report_id = $1 AND sha256 = $2
		ORDER BY created_at LIMIT 1`, in.ReportID, hash).Scan(&already); {
	case err == nil:
		_ = tx.Rollback(ctx)
		c, gerr := s.Get(ctx, already)
		if gerr != nil {
			return nil, gerr
		}
		c.Replayed = true
		return c, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("checking for a resent capture: %w", err)
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO survey_captures (
			report_id, line_no, kind, caption,
			media_path, content_type, byte_size, sha256,
			captured_at, lat, lon, accuracy_m, heading_deg, device_label,
			inside_parcel, distance_from_parcel_m, gates_passed, gates_failed,
			captured_by,
			station_no, station_offset_m, duration_ms, is_mocked,
			encoding_profile, encoding_fps, encoding_bitrate_bps,
			guidance_asked_for, guidance_model
		) VALUES (
			$1, $2, $3::capture_kind, NULLIF($4, ''),
			$5, $6, $7, $8,
			$9, $10, $11, $12, $13, NULLIF($14, ''),
			$15, $16, $17, $18, $19,
			$20, $21, $22, $23,
			$24, $25, $26,
			NULLIF($27, ''), NULLIF($28, '')
		) RETURNING id`,
		in.ReportID, in.LineNo, string(in.Kind), in.Caption,
		rel, in.ContentType, size, hash,
		in.CapturedAt, in.Lat, in.Lon, in.AccuracyM, in.HeadingDeg, in.DeviceLabel,
		gates.inside, gates.distance, gates.passed, gates.failed,
		nullString(in.CapturedBy),
		in.StationNo, in.StationOffsetM, in.DurationMs, in.IsMocked,
		in.EncodingProfile, in.EncodingFPS, in.EncodingBitrateBPS,
		derefOrEmpty(in.GuidanceAskedFor), derefOrEmpty(in.GuidanceModel),
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("recording capture: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("recording capture: %w", err)
	}

	// Evidence from a directed point: record which points the walk was sent
	// to, once per report. Best effort -- a capture that arrived is worth
	// more than the note about where it was meant to come from.
	if in.StationNo != nil && in.FarmID != "" {
		if err := s.RecordDirection(ctx, in.ReportID, in.FarmID); err != nil {
			s.logDirectionFailure(in.ReportID, err)
		}
	}

	return s.Get(ctx, id)
}

// logDirectionFailure notes that the directed points could not be recorded.
//
// Deliberately quiet: it costs the report its sampling-coverage section, which
// is a weaker report rather than a lost capture.
func (s *Store) logDirectionFailure(reportID string, err error) {
	if s.log == nil {
		return
	}
	s.log.Warn("could not record the directed points for a report",
		"report_id", reportID, "error", err)
}

// onReport finds a capture of these exact bytes already on this report.
func (s *Store) onReport(ctx context.Context, reportID, hash string) (*Capture, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM survey_captures WHERE report_id = $1 AND sha256 = $2
		ORDER BY created_at LIMIT 1`, reportID, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking for a resent capture: %w", err)
	}
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Replayed = true
	return c, nil
}

type gateResult struct {
	inside   *bool
	distance *float64
	// away is how far this capture is from the claim's nearest other
	// capture, for a parcel with no outline. Nil where there is a boundary --
	// the geofence is the stronger answer -- or where nothing else has been
	// captured yet.
	away *float64
	// nearestOther is how far this clip is from the nearest clip of a
	// different point of the walk. Nil where this is not a walk clip, or
	// where it is the first.
	nearestOther *float64
	// spacing is how far this claim requires the walk to move. Frozen with the
	// assigned points from the crop's own sown extent, so the handset is working
	// to the same figure -- see walk_required_spacing in migration 0055.
	spacing float64
	// takenBy names the claim that already recorded near here, when one on the
	// same survey number has.
	//
	// A survey number is routinely shared: one farmer with 1 ha of soybean and
	// 1 ha of udad files two claims on it, and a neighbour with 5 ha files a
	// third. Ground filmed for one of those is not independent evidence for
	// another, and until now nothing looked -- the check was scoped to a single
	// report, so the same patch could be filmed three times and counted three
	// times.
	takenBy *takenPoint
	passed  []string
	failed  []string
}

// takenPoint is a point on this survey number that another claim has already
// recorded.
type takenPoint struct {
	// MetresAway from the capture being judged.
	MetresAway float64
	// SameFarmer is true when it is this farmer's own other claim, which needs
	// different words: they are being sent to their other crop's extent, not
	// told a neighbour got there first.
	SameFarmer bool
	// Crop the other claim was for, where it is known. Empty otherwise: an
	// invented crop name in a refusal is worse than none.
	Crop string
}

// takenError says whose ground this is, in the terms the farmer needs.
//
// The distance is deliberately absent from the farmer-facing half of this. "You
// are 6 m from a point another claim recorded" invites them to take four steps
// and try again, which is not the remedy: the remedy is to walk to their own
// share of the survey number, however far that is. The metres stay in the
// message for the officer reading the audit trail.
func takenError(t *takenPoint) error {
	switch {
	case t.SameFarmer && t.Crop != "":
		return fmt.Errorf("%w: your own %s claim recorded this point, %.0f m "+
			"away; record this crop on its own extent",
			ErrPointTakenInSurvey, t.Crop, t.MetresAway)
	case t.SameFarmer:
		return fmt.Errorf("%w: your own other claim on this survey number "+
			"recorded this point, %.0f m away; record this crop on its own "+
			"extent", ErrPointTakenInSurvey, t.MetresAway)
	default:
		return fmt.Errorf("%w: another farmer in this survey number recorded "+
			"this point, %.0f m away; record on your own extent",
			ErrPointTakenInSurvey, t.MetresAway)
	}
}

// runGates checks a capture and names what it found.
//
// Every gate is recorded either way. A list of what passed is as useful to an
// officer as a list of what did not: it is the difference between "this was
// checked and was fine" and "this was never checked".
func runGates(ctx context.Context, pool *db.Pool, in New, hash string) gateResult {
	g := gateResult{passed: []string{}, failed: []string{}}

	add := func(name string, ok bool) {
		if ok {
			g.passed = append(g.passed, name)
		} else {
			g.failed = append(g.failed, name)
		}
	}

	// Geofence, when the parcel has a boundary at all. AgriStack returns none
	// for any farmer we can reach, so an absent boundary is the common case
	// and is not the farmer's fault -- the gate is skipped rather than failed.
	var inside bool
	var distance float64
	err := pool.QueryRow(ctx,
		`SELECT inside, distance_m FROM capture_geofence($1, $2, $3)`,
		in.FarmID, in.Lon, in.Lat).Scan(&inside, &distance)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No boundary: nothing to check against, and nothing to claim.
	case err != nil:
		g.failed = append(g.failed, GateInsideParcel)
	default:
		g.inside, g.distance = &inside, &distance
		add(GateInsideParcel, inside)
	}

	// Without a boundary there is no fence, so the claim's own evidence
	// becomes one: the first capture anchors the field and everything after
	// it must be near that. Skipped once a boundary exists, which is the
	// stronger check.
	if g.inside == nil && (in.Lat != 0 || in.Lon != 0) {
		var away *float64
		err := pool.QueryRow(ctx, `
			SELECT min(ST_Distance(c.geom::geography,
			                       ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography))
			  FROM survey_captures c
			 WHERE c.report_id = $1::uuid AND c.geom IS NOT NULL`,
			in.ReportID, in.Lon, in.Lat).Scan(&away)
		if err == nil && away != nil {
			g.away = away
			add(GateSameField, *away <= UnmappedFieldRadiusM)
		}
	}

	// The walk has to move. Measured against the other points of this walk,
	// never against the assigned coordinates: a farmer who cannot reach the
	// surveyed spot but records fifteen metres away has walked, and a farmer
	// who records four clips from the gate has not. Clips of the same point
	// are excluded, because re-recording a point the phone failed on is
	// ordinary and happens where the first attempt was made.
	if in.StationNo != nil && (in.Lat != 0 || in.Lon != 0) {
		// How far this claim requires the walk to move.
		//
		// The figure frozen with the assigned points, which is what the handset
		// was given and is therefore what it enforced. Derived from the crop's
		// sown extent rather than from the parcel: a survey number is often
		// shared, and the old rule asked a farmer with 1 ha of soybean and a
		// neighbour with 5 ha for the same fifteen metres because both sit on one
		// polygon.
		//
		// Falling back to the parcel rule where nothing was frozen -- a report
		// from before 0055, or a parcel with no boundary to place points in. The
		// fallback is the figure such a handset is itself using, so the two still
		// agree.
		var frozen *float64
		_ = pool.QueryRow(ctx,
			`SELECT required_spacing_m FROM report_sampling_stations
			  WHERE report_id = $1::uuid AND required_spacing_m IS NOT NULL
			  LIMIT 1`, in.ReportID).Scan(&frozen)
		if frozen != nil {
			g.spacing = *frozen
		} else {
			var areaM2 float64
			_ = pool.QueryRow(ctx,
				`SELECT COALESCE(ST_Area(geom::geography), 0) FROM parcel_boundaries
				  WHERE farm_id = $1`, in.FarmID).Scan(&areaM2)
			g.spacing = SpacingFor(areaM2)
		}

		// Ground another claim on this survey number has already filmed.
		//
		// Joined through the report's items rather than read off the capture,
		// which carries no farm id of its own. Restricted to walk clips: a
		// farmer's close-up of the damage is not a sampling point and does not
		// reserve the ground it was taken on.
		if in.FarmID != "" {
			var (
				away       *float64
				sameFarmer *bool
				otherCrop  *string
			)
			// Whose claim it was is resolved here rather than passed in: the
			// wording turns on it, and threading a farmer id through every
			// caller of this to answer one sentence is a wider change than the
			// question deserves.
			err := pool.QueryRow(ctx, `
				SELECT ST_Distance(c.geom::geography,
				                   ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography)
				     , r.farmer_id = (SELECT farmer_id FROM crop_loss_reports
				                       WHERE id = $4::uuid)
				     , i.crop_name
				  FROM survey_captures c
				  JOIN crop_loss_report_items i ON i.report_id = c.report_id
				  JOIN crop_loss_reports r      ON r.id = c.report_id
				 WHERE i.farm_id = $1
				   AND c.report_id <> $4::uuid
				   AND c.geom IS NOT NULL
				   AND c.station_no IS NOT NULL
				   -- A withdrawn claim evidences nothing, so its ground is free.
				   --
				   -- This must say exactly what TakenPoints says, and for one
				   -- visit it did not: that query was taught about withdrawal and
				   -- this one was not, so the handset was handed a list with a
				   -- withdrawn claim's points left out, let the farmer record
				   -- there, and the office refused the clip. The farmer walks the
				   -- field twice and is told at the end of the first walk that
				   -- somebody else has that ground, which is not even true.
				   --
				   -- The two queries answer the same question -- what has been
				   -- filmed on this farm that is not this claim -- and a
				   -- difference between them is always a farmer sent back.
				   AND r.state <> 'withdrawn'
				 ORDER BY c.geom::geography <->
				          ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography
				 LIMIT 1`,
				in.FarmID, in.Lon, in.Lat, in.ReportID,
			).Scan(&away, &sameFarmer, &otherCrop)
			if err == nil && away != nil && *away < g.spacing {
				taken := takenPoint{
					MetresAway: *away,
					SameFarmer: sameFarmer != nil && *sameFarmer,
				}
				if otherCrop != nil {
					taken.Crop = *otherCrop
				}
				g.takenBy = &taken
			}
			add(GateOwnExtent, g.takenBy == nil)
		}

		var nearest *float64
		err := pool.QueryRow(ctx, `
			SELECT min(ST_Distance(c.geom::geography,
			                       ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography))
			  FROM survey_captures c
			 WHERE c.report_id = $1::uuid AND c.geom IS NOT NULL
			   AND c.station_no IS NOT NULL AND c.station_no <> $4`,
			in.ReportID, in.Lon, in.Lat, *in.StationNo).Scan(&nearest)
		if err == nil && nearest != nil {
			g.nearestOther = nearest
			add(GateMovedOn, *nearest >= g.spacing)
		}
	}

	// Only recorded when the handset actually said. A client too old to
	// report it leaves the gate absent rather than passed: "did not tell us"
	// and "told us it was genuine" are different facts, and only one of them
	// is evidence.
	if in.IsMocked != nil {
		add(GateGenuineFix, !*in.IsMocked)
	}

	if in.AccuracyM != nil {
		add(GatePositionFix, PositionFixUsable(*in.AccuracyM))
	}

	// Timing. Both are about the event the report was filed against.
	if in.WindowStart != "" && in.WindowEnd != "" {
		add(GateWithinWindow, WithinWindow(in.CapturedAt, in.WindowStart))
	}
	if !in.IncidentAt.IsZero() {
		add(GateAfterIncident, AfterIncident(in.CapturedAt, in.IncidentAt))
	}

	add(GateFreshCapture, FreshCapture(in.CapturedAt, time.Now()))

	// The same bytes twice is one photograph submitted for two plots.
	//
	// On a different report. The same bytes on the same report are one
	// photograph sent twice, which Submit answers before this runs; counting
	// them here marked a farmer's own retry as a reused photograph.
	var seen bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM survey_captures
		                 WHERE sha256 = $1 AND report_id <> $2)`,
		hash, in.ReportID).Scan(&seen); err == nil {
		add(GateDistinctImage, !seen)
	}

	return g
}

const captureColumns = `
	c.id, c.report_id, c.line_no, c.kind::text, COALESCE(c.caption, ''),
	c.content_type, c.byte_size, c.sha256,
	c.captured_at, c.lat, c.lon, c.accuracy_m, c.heading_deg,
	COALESCE(c.device_label, ''), c.uploaded_at,
	c.inside_parcel, c.distance_from_parcel_m, c.gates_passed, c.gates_failed,
	COALESCE(c.captured_by::text, ''), c.created_at,
	c.station_no, c.station_offset_m, c.duration_ms,
	COALESCE(c.guidance_asked_for, ''), COALESCE(c.guidance_model, '')`

func scanCapture(row pgx.Row) (*Capture, error) {
	var c Capture
	var kind string
	err := row.Scan(
		&c.ID, &c.ReportID, &c.LineNo, &kind, &c.Caption,
		&c.ContentType, &c.ByteSize, &c.SHA256,
		&c.CapturedAt, &c.Lat, &c.Lon, &c.AccuracyM, &c.HeadingDeg,
		&c.DeviceLabel, &c.UploadedAt,
		&c.InsideParcel, &c.DistanceFromParcelM, &c.GatesPassed, &c.GatesFailed,
		&c.CapturedBy, &c.CreatedAt,
		&c.StationNo, &c.StationOffsetM, &c.DurationMs,
		&c.GuidanceAskedFor, &c.GuidanceModel,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Kind = Kind(kind)
	if c.GatesPassed == nil {
		c.GatesPassed = []string{}
	}
	if c.GatesFailed == nil {
		c.GatesFailed = []string{}
	}
	return &c, nil
}

// Get returns one capture.
func (s *Store) Get(ctx context.Context, id string) (*Capture, error) {
	return scanCapture(s.pool.QueryRow(ctx,
		`SELECT `+captureColumns+` FROM survey_captures c WHERE c.id = $1`, id))
}

// ForReport returns every capture on a report, oldest first.
func (s *Store) ForReport(ctx context.Context, reportID string) ([]Capture, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureColumns+` FROM survey_captures c
		 WHERE c.report_id = $1 ORDER BY c.captured_at`, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading captures: %w", err)
	}
	defer rows.Close()

	out := []Capture{}
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Media opens a capture's bytes for streaming to a client.
func (s *Store) Media(ctx context.Context, id string) (io.ReadCloser, string, error) {
	var rel, contentType string
	err := s.pool.QueryRow(ctx,
		`SELECT media_path, content_type FROM survey_captures WHERE id = $1`,
		id).Scan(&rel, &contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}

	rc, err := s.blobs.Get(ctx, rel)
	if errors.Is(err, blob.ErrNotFound) {
		// The row exists and the bytes do not. Reported as its own thing:
		// this is a data-integrity problem to investigate, not a bad request
		// from whoever happened to open the page.
		return nil, "", fmt.Errorf("%w: capture %s has no stored media", ErrNotFound, id)
	}
	if err != nil {
		return nil, "", fmt.Errorf("opening media: %w", err)
	}
	return rc, contentType, nil
}

// DataURI returns a capture inline, for handing to the assessment agent.
//
// The agent takes images as data URIs, so this is where a file on a volume
// becomes something a multimodal model can look at.
func (s *Store) DataURI(ctx context.Context, id string) (string, error) {
	rc, contentType, err := s.Media(ctx, id)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	limit, ok := acceptedMedia[contentType]
	if !ok {
		limit = MaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(rc, int64(limit)))
	if err != nil {
		return "", err
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

func extensionFor(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ".jpg"
	}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// derefOrEmpty is a nil pointer as the empty string, for a NULLIF insert.
//
// The pair reads as "absent is null", which is what the column means: nothing was
// asked, rather than an empty thing was asked.
func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
