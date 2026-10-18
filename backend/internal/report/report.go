// Package report owns the Crop Loss Assessment Report.
//
// # Where the parameters come from
//
// The Maharashtra crop loss intimation form (पीक नुकसान सूचना फॉर्म, 2019),
// with everything specific to crop insurance removed: no scheme name, no
// loanee status, no bank account or branch, no premium amount or receipt date,
// no proposal or cover note number, and no insured-area column beside the sown
// area. What is left is the description of the loss, which is what this
// platform assesses.
//
// # What the form gets right
//
// It records the revenue circle (महसूल मंडळ) as a location in its own right,
// which is the level LGD gives no code to and the level a Circle Officer is
// posted to.
//
// It separates standing-crop loss from post-harvest loss and applies different
// calamities to each. Flood water in a growing crop and a cyclone through a cut
// crop lying in the field are different events, and an assessment that treats
// them alike measures the wrong thing.
package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Errors.
var (
	ErrNotFound  = errors.New("crop loss report not found")
	ErrBadState  = errors.New("the report is not in a state that allows this")
	ErrBadInput  = errors.New("invalid report")
	ErrNotInArea = errors.New("the parcel is not covered by the survey event")

	// ErrAlreadyFiled is a second claim for the same crop on the same parcel
	// against the same calamity.
	//
	// One loss, one claim. Until now the only duplicate the office refused
	// was the same handset re-sending the same draft, matched on its client
	// reference; a farmer who walked the field twice, or tapped once on each
	// of two phones, filed two claims for the same damage -- two assessments,
	// two approvals, and eventually two figures for one field.
	//
	// The crop is what makes the loss one loss. A farm carries several of
	// them, each sown on its own part of the ground and each losing its own
	// harvest, so a hailstorm over a farmer's soybean and their udad is two
	// losses and two claims. It is the crop and not the parcel that may only
	// be claimed once.
	//
	// A withdrawn claim frees the crop again, because withdrawing is how a
	// farmer takes back a claim they should not have made. Nothing else does:
	// a decided claim is decided, and re-filing it is not a correction but a
	// second ask.
	ErrAlreadyFiled = errors.New("a claim for this crop, parcel and calamity has already been filed")

	// ErrRetry means the report could not be recorded for a reason that has
	// nothing to do with what it says -- two writes colliding, a lock that
	// could not be taken in time. The caller should send it again; answering
	// this as invalid would tell a farmer's phone their claim was refused.
	ErrRetry = errors.New("the report could not be recorded just now; send it again")

	// ErrNotYet means the office is not taking this claim at the moment but
	// expects to: a district has paused its own event and will resume it.
	//
	// Separate from ErrBadState because the handset reads a refusal as final
	// -- it stops sending and tells the farmer their claim was refused. A
	// pause the district means to undo that afternoon is not a verdict on
	// anybody's claim, and a queue that discards work over one has lost
	// evidence nobody intended to lose.
	ErrNotYet = errors.New("the office is not taking this claim yet; send it again later")
)

// createAttempts is how many times a filing that collided is tried again
// before the caller is asked to retry.
const createAttempts = 3

// LossCategory distinguishes the two kinds of loss the form covers.
type LossCategory string

const (
	// StandingCrop — स्थानिक नैसर्गिक आपत्ती, loss to a growing crop.
	StandingCrop LossCategory = "standing_crop"
	// PostHarvest — काढणी पश्चात नुकसान, loss to a cut crop lying in the field.
	PostHarvest LossCategory = "post_harvest"
)

// Season is the cropping season.
type Season string

const (
	Kharif Season = "kharif"
	Rabi   Season = "rabi"
	Summer Season = "summer"
)

// State is a report's lifecycle position.
type State string

const (
	StateDraft        State = "draft"
	StateSubmitted    State = "submitted"
	StateAcknowledged State = "acknowledged"

	// The verification workflow, added with report_verification. These are
	// reached by allocation, by an officer filing findings, and by
	// adjudication -- not by anyone pressing a status button -- but they are
	// named here because a state machine that does not know its own states
	// cannot decide what may follow them.
	StateAssigned          State = "assigned"
	StateUnderVerification State = "under_verification"
	StateVerified          State = "verified"
	StateAdjudicated       State = "adjudicated"

	StateClosed    State = "closed"
	StateWithdrawn State = "withdrawn"
)

// FilingWindow is the interval within which the form requires intimation.
//
// Recorded, not enforced. A village with no signal for three days is not a
// fraudulent claim, and refusing it at the door pushes exactly the farmers
// this system exists for back into a paper queue. The officer sees the flag.
const FilingWindow = 48 * time.Hour

// ClockSkew is how far ahead of this server a handset's clock may be before
// the instant it reports is discarded. Phones drift, and a minute of drift is
// not a claim about anything.
const ClockSkew = 5 * time.Minute

// filingInstant decides when a claim counts as filed, and says whether the
// handset's own answer was taken.
//
// # Why the handset is asked at all
//
// The window is about when the farmer acted, and the farmer acts in a field.
// Measuring it on arrival records a farmer who filed on the first morning and
// found a signal on the third day as having filed late -- a wrong fact about
// somebody who did nothing wrong, on the one flag an officer reads when
// weighing whether to believe them.
//
// # Why it is not simply believed
//
// A clock is something the holder of the phone can set. Believing it outright
// would let a claim filed a month late present itself as timely, so two
// impossibilities are refused: an instant this server has not reached yet,
// and an instant before the loss it claims to report. Neither is a judgement
// about the farmer -- a phone that has been flat for a week comes back with
// the wrong date and no intent at all -- so the claim is filed either way,
// on the arrival instant, with both kept on the record.
func filingInstant(filedAt *time.Time, occurredAt, arrived time.Time) (time.Time, bool) {
	if filedAt == nil || filedAt.IsZero() {
		return arrived, false
	}
	at := filedAt.UTC()
	if at.After(arrived.Add(ClockSkew)) || at.Before(occurredAt) {
		return arrived, false
	}
	return at, true
}

// Calamities permitted in each category, from the form.
var (
	standingCropCalamities = []string{
		"flood", "hailstorm", "landslide", "cloudburst",
		"drought", "pest_attack", "frost", "fire",
	}
	postHarvestCalamities = []string{"cyclone", "unseasonal_rain", "hailstorm"}
)

// CalamitiesFor returns the calamities valid for a loss category, so a form
// renders the right list rather than hard-coding it.
func CalamitiesFor(c LossCategory) []string {
	switch c {
	case StandingCrop:
		return slices.Clone(standingCropCalamities)
	case PostHarvest:
		return slices.Clone(postHarvestCalamities)
	}
	return nil
}

// ValidCalamity reports whether a calamity may be reported under a category.
func ValidCalamity(c LossCategory, calamity string) bool {
	return slices.Contains(CalamitiesFor(c), calamity)
}

// Item is one line of the crop table: one survey number, one crop.
type Item struct {
	LineNo          int    `json:"line_no"`
	SurveyNumber    string `json:"survey_number"`
	SubSurveyNumber string `json:"sub_survey_number,omitempty"`
	FarmID          string `json:"farm_id,omitempty"`
	CropName        string `json:"crop_name"`

	AreaSownHa     float64 `json:"area_sown_ha"`
	AreaAffectedHa float64 `json:"area_affected_ha"`
	DamagePercent  float64 `json:"damage_percent"`
}

// Place is the four-level location the form asks for.
type Place struct {
	VillageCode string `json:"village_lgd_code"`
	VillageName string `json:"village_name"`
	// Circle is महसूल मंडळ. Empty where the district's circles have not been
	// mapped, which is most of them.
	CircleCode      string `json:"circle_code,omitempty"`
	CircleName      string `json:"circle_name,omitempty"`
	SubDistrictCode string `json:"sub_district_lgd_code"`
	SubDistrictName string `json:"sub_district_name"`
	DistrictCode    string `json:"district_lgd_code"`
	DistrictName    string `json:"district_name"`
}

// Report is one Crop Loss Assessment Report.
type Report struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`

	SurveyEventID        string `json:"survey_event_id"`
	SurveyEventReference string `json:"survey_event_reference,omitempty"`

	FarmerID      string `json:"farmer_id"`
	FarmerName    string `json:"farmer_name"`
	FarmerAddress string `json:"farmer_address,omitempty"`
	FarmerMobile  string `json:"farmer_mobile,omitempty"`
	FarmerEmail   string `json:"farmer_email,omitempty"`

	Place Place `json:"place"`

	Season   Season `json:"season"`
	CropYear string `json:"crop_year"`

	LossCategory LossCategory `json:"loss_category"`
	CalamityType string       `json:"calamity_type"`
	OccurredAt   time.Time    `json:"occurred_at"`

	// CaptureCount is how many photographs and clips are on this claim, and
	// StationsCaptured how many of the four directed points were filmed.
	//
	// Both were maintained by a trigger from the first walk and read by
	// nothing, so an officer opening a claim could not tell whether it rested
	// on four points or one (docs/EVIDENCE_REVALIDATION.md, V8).
	CaptureCount     int `json:"capture_count"`
	StationsCaptured int `json:"stations_captured"`

	ReportedAt      time.Time `json:"reported_at"`
	ReportedBy      string    `json:"reported_by,omitempty"`
	ReportedChannel string    `json:"reported_channel"`

	FiledLate  bool   `json:"filed_late"`
	LateReason string `json:"late_reason,omitempty"`

	// FiledAt is when the farmer completed the form on the handset, where the
	// handset said so and the instant was possible. ReportedAt above is when
	// the claim reached the office, and the gap between them is the queue
	// doing its job -- days of it, where a village has no signal.
	//
	// The intimation window is measured on this. Both are kept so an officer
	// can see the delay rather than infer it (migration 0040).
	FiledAt *time.Time `json:"filed_at,omitempty"`

	// ArrivedAfterClose means this claim was completed before the event
	// stopped taking claims and delivered after it. Taken, because the delay
	// was this system's -- a queue waiting for a network, or for the rest of
	// the evidence -- and not the farmer's.
	//
	// Recorded because the district closed its books on a count that did not
	// include it, and because it is the one case where the filing instant a
	// handset reported decided whether a claim exists at all.
	ArrivedAfterClose bool `json:"arrived_after_close"`

	DeclarationAccepted bool     `json:"declaration_accepted"`
	DocumentsPromised   []string `json:"documents_promised"`

	FarmerSignatureKind string     `json:"farmer_signature_kind,omitempty"`
	FarmerSignedAt      *time.Time `json:"farmer_signed_at,omitempty"`

	AcknowledgedBy     string     `json:"acknowledged_by,omitempty"`
	AcknowledgedByName string     `json:"acknowledged_by_name,omitempty"`
	AcknowledgedAt     *time.Time `json:"acknowledged_at,omitempty"`

	State State `json:"state"`

	// ApprovalLevel is the rung the report has reached, empty before it
	// joins the ladder. On the report itself because who holds it changes
	// what everyone else may do with it -- the village pair may revise a
	// finding while it is still theirs and not once it has climbed.
	ApprovalLevel Level `json:"approval_level,omitempty"`

	Items []Item `json:"items"`

	// The decision, carried on the report itself.
	//
	// Without these a farmer's own report never tells them what was decided
	// -- only that something was. The figure lives on crop_loss_reports
	// because every rollup reads it there, and an appeal that revises it
	// updates this same column, so what a farmer sees is always current.
	AdjudicatedPercent *float64   `json:"adjudicated_percent,omitempty"`
	AdjudicatedAreaHa  *float64   `json:"adjudicated_area_ha,omitempty"`
	AdjudicatedAt      *time.Time `json:"adjudicated_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	// Replayed is set when this report was already filed and a retry found
	// it. Never serialised: it describes this response, not the report.
	Replayed bool `json:"-"`
}

// FarmIDs are the registry parcels this report names, in line order.
//
// A report can span several survey numbers; the geofence checks a capture
// against the parcel the capture says it is of, and this is how a caller
// finds the candidates.
func (r *Report) FarmIDs() []string {
	var out []string
	for _, i := range r.Items {
		if i.FarmID != "" {
			out = append(out, i.FarmID)
		}
	}
	return out
}

// TotalSownHa is the sown area across every line.
func (r *Report) TotalSownHa() float64 {
	var t float64
	for _, i := range r.Items {
		t += i.AreaSownHa
	}
	return t
}

// TotalAffectedHa is the affected area across every line.
func (r *Report) TotalAffectedHa() float64 {
	var t float64
	for _, i := range r.Items {
		t += i.AreaAffectedHa
	}
	return t
}

// WeightedDamagePercent is the affected area weighted by each line's damage,
// as a share of the sown area.
//
// The mean of the per-line percentages would be wrong: a total loss on a tenth
// of a hectare and a scratch on four hectares do not average to half.
func (r *Report) WeightedDamagePercent() float64 {
	sown := r.TotalSownHa()
	if sown <= 0 {
		return 0
	}
	var weighted float64
	for _, i := range r.Items {
		weighted += i.AreaAffectedHa * i.DamagePercent
	}
	return weighted / sown
}

// New is the input for filing a report.
type New struct {
	SurveyEventID string

	FarmerID      string
	FarmerName    string
	FarmerAddress string
	FarmerMobile  string
	FarmerEmail   string

	VillageCode string

	Season   Season
	CropYear string

	LossCategory LossCategory
	CalamityType string
	OccurredAt   time.Time

	ReportedBy      string
	ReportedChannel string
	LateReason      string

	// ExpectedCaptures is how many photographs and clips the handset is going
	// to send for this claim.
	//
	// The claim arrives first and its evidence follows one piece at a time, so
	// without this the office cannot tell "still uploading" from "that was
	// everything" -- and a pipeline that starts assessing on arrival assesses
	// half a walk. The handset knows the full set before it sends anything,
	// because it holds the claim until the walk is whole.
	//
	// Nil for a filing with no queue behind it, which is complete on arrival.
	ExpectedCaptures *int

	// FiledAt is when the farmer completed the form on the handset, which is
	// not when this claim arrived. The gap is the app working as intended: the
	// form is filled in a field with no signal and the queue delivers it when
	// a network appears, later still now that a claim waits for its evidence.
	//
	// The intimation window is measured on this where it is given. Nil for a
	// filing with no queue behind it -- a counter, a call centre -- where the
	// arrival is the filing. See filingInstant for what is done with a handset
	// clock that disagrees with this server's.
	FiledAt *time.Time

	DeclarationAccepted bool
	DocumentsPromised   []string
	FarmerSignatureKind string

	Items []Item

	// CropMismatch means the registry names a crop this event does not cover.
	//
	// Recorded, never enforced. AgriStack returns an empty crop_sown_data for
	// every farmer this platform has reached live, so refusing on a mismatch
	// would lock out the genuine claimant far more often than it would catch
	// anybody. The village pair settle it standing in the field, which is the
	// only place it can honestly be settled.
	CropMismatch bool

	// RegistryCrop is what the registry said, so an officer reading the flag
	// sees the actual disagreement rather than a boolean.
	RegistryCrop string

	// ClientRef is the handset's own reference for this filing -- the
	// Idempotency-Key it sends, kept for the life of the queued report.
	//
	// A report whose reply was lost is sent again, and without this the second
	// send was a second claim for the same parcel. Unique per farmer, enforced
	// by the database inside the same transaction as the insert, so exactly
	// one report exists however many times it arrives and however the server
	// dies in between. Empty for a report filed at a counter, which has no
	// queue behind it.
	ClientRef string
}

// Store persists reports.
type Store struct {
	pool  *db.Pool
	reads *db.Router

	// notify queues messages to the farmer. Optional -- a deployment without
	// it simply tells nobody, which is what this system did before.
	//
	// Held here rather than called from the API layer because the intent to
	// notify has to commit in the same transaction as the thing that caused
	// it. A report acknowledged with no message queued, because the process
	// died between the two, is the failure an outbox exists to prevent.
	notify *notify.Queue

	// officers is the console's bell -- the officer-facing half of the same
	// job. Optional for the same reason and written in the same transaction:
	// a report that moved to the tehsil with nobody told about it is a report
	// sitting in a queue no one is looking at. See ladder_notify.go.
	officers *notify.Officers

	// log records what could not be announced. A notification that failed is
	// worth a line; it is never worth failing the decision that caused it.
	log *slog.Logger
}

// NewStore builds a store.
func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// WithReads sends the aggregate views -- the overview and the state-wide view
// -- to a replica where one is configured and close enough behind.
func (s *Store) WithReads(r *db.Router) *Store {
	s.reads = r
	return s
}

// readPool is the pool for a staleness-tolerant aggregate read.
func (s *Store) readPool() *db.Pool {
	if p := s.reads.Read(); p != nil {
		return p
	}
	return s.pool
}

// WithLogger attaches a logger for the things this store declines to fail on.
func (s *Store) WithLogger(l *slog.Logger) *Store {
	s.log = l
	return s
}

// WithNotifier attaches the outbox.
func (s *Store) WithNotifier(q *notify.Queue) *Store {
	s.notify = q
	return s
}

// tell queues one message, inside the caller's transaction.
//
// Never fails the caller. A farmer whose acknowledgement could not be queued
// still has an acknowledgement, and losing the report to save the message
// would be the wrong way round.
func (s *Store) tell(
	ctx context.Context, tx db.Querier, eventCode string, r *Report, v notify.Vars,
) {
	if s.notify == nil || r == nil {
		return
	}
	body, err := notify.Render(eventCode, v)
	if err != nil {
		return
	}
	_ = s.notify.Enqueue(ctx, tx, notify.Message{
		EventCode:    eventCode,
		FarmerID:     r.FarmerID,
		Recipient:    r.FarmerMobile,
		Body:         body,
		ResourceType: "crop_loss_report",
		ResourceID:   r.ID,
	})
}

// target reads what a message about a report needs, inside the caller's
// transaction.
//
// Not Store.Get: that takes a jurisdiction scope and correctly refuses an
// empty one, and the notifier has no scope to offer -- it is acting on behalf
// of a decision already authorised. Reading inside the transaction also means
// the message describes the state being committed rather than whatever a
// second connection happened to see.
func (s *Store) target(ctx context.Context, tx db.Querier, reportID string) *Report {
	var r Report
	err := tx.QueryRow(ctx, `
		SELECT id, reference, farmer_id, COALESCE(farmer_mobile, ''),
		       village_name, calamity_type::text
		FROM crop_loss_reports WHERE id = $1`, reportID).
		Scan(&r.ID, &r.Reference, &r.FarmerID, &r.FarmerMobile,
			&r.Place.VillageName, &r.CalamityType)
	if err != nil {
		return nil
	}
	return &r
}

// notifyVars fills in what every message about a report needs.
func notifyVars(r *Report) notify.Vars {
	return notify.Vars{
		Reference: r.Reference,
		Village:   r.Place.VillageName,
		Calamity:  r.CalamityType,
	}
}

// figure renders a damage percentage for a farmer, or says there is none.
//
// "नोंद नाही" rather than 0%: an assessment that recorded no figure and one
// that recorded zero are different outcomes, and a farmer reading the second
// when the first is true has been told something false.
func figure(pct *float64) string {
	if pct == nil {
		return "नोंद नाही"
	}
	return fmt.Sprintf("%.1f%%", *pct)
}

// Create files a report.
//
// The place is resolved from the village code by walking the jurisdiction
// tree, rather than being taken from the caller. A report that names its own
// taluka is a report that can name the wrong one.
func (s *Store) Create(ctx context.Context, in New) (*Report, error) {
	// No documents promised is an empty list, never NULL.
	//
	// The column is NOT NULL DEFAULT '{}', and a default applies only when a
	// column is left out of the insert -- a nil slice is not left out, it is
	// sent as NULL. The console always sent []; the farmer's app never sends
	// the field at all. So every claim filed from a phone was refused with
	// "documents_promised is not satisfied", which the app shows as "some
	// details on this report are not usable": found on a real handset, after a
	// farmer had walked all four points and filmed them. Every test fixture
	// set []string{}, which is why nothing caught it.
	if in.DocumentsPromised == nil {
		in.DocumentsPromised = []string{}
	}

	var err error
	for attempt := 0; attempt < createAttempts; attempt++ {
		var r *Report
		r, err = s.create(ctx, in)
		if err == nil || !errors.Is(err, ErrRetry) {
			return r, err
		}
	}
	return nil, err
}

func (s *Store) create(ctx context.Context, in New) (*Report, error) {
	// A report this handset already filed is answered before anything is
	// checked. Its first send was valid when it arrived; the retry may come
	// days later, after the filing window has closed or the event has been
	// paused, and refusing it then would tell a farmer their claim was
	// rejected when it was in fact received on time.
	if in.ClientRef != "" {
		if existing, err := s.byClientRef(ctx, in.FarmerID, in.ClientRef); err != nil {
			return nil, err
		} else if existing != nil {
			return existing, nil
		}
	}

	if err := validate(in); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	place, err := resolvePlace(ctx, tx, in.VillageCode)
	if err != nil {
		return nil, err
	}

	// The event must cover this village and this date. Checked inside the
	// transaction so a closing event cannot slip a report through.
	var covered bool
	if err := tx.QueryRow(ctx,
		`SELECT village_in_event($1, $2)`, in.SurveyEventID, in.VillageCode).
		Scan(&covered); err != nil {
		return nil, fmt.Errorf("checking event coverage: %w", err)
	}
	if !covered {
		return nil, fmt.Errorf(
			"%w: village %s (%s) is not in any taluka or circle the event covers",
			ErrNotInArea, place.VillageName, in.VillageCode)
	}

	var evStart, evEnd time.Time
	var evCloses time.Time
	var evClosedAt *time.Time
	var evState, evRef string
	if err := tx.QueryRow(ctx, `
		SELECT incident_start, incident_end, survey_closes_at, closed_at,
		       state::text, reference
		FROM survey_events WHERE id = $1`, in.SurveyEventID).
		Scan(&evStart, &evEnd, &evCloses, &evClosedAt, &evState, &evRef); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: no such survey event", ErrBadInput)
		}
		return nil, err
	}

	// One claim per crop per parcel per calamity.
	//
	// The crop is part of what makes a claim distinct rather than a label on
	// it. One registered farm carries several crops: a farmer with two
	// hectares on one survey number grows a hectare of soybean and a hectare
	// of udad, and each is sown separately, damaged separately, walked at its
	// own sown extent and claimed separately. Keyed on the parcel alone this
	// refused the second crop as a duplicate of the first, so one hailstorm
	// across such a holding could only ever be half filed -- and the farmer
	// was told their land was already claimed, which was true of the soybean
	// and false of everything they were trying to say.
	//
	// The same reasoning carries the crop into the survey_number fallback
	// below: a parcel the registry has not linked is still a parcel with more
	// than one thing growing on it.
	//
	// Names are folded and trimmed before they are compared, because the name
	// arrives as whatever the registry or the handset spelled -- so "Soybean"
	// filed twice is one crop claimed twice and not two crops.
	//
	// A line with no crop at all never reaches here: validate refuses one. If
	// such a row exists from some older path, an empty name still equals an
	// empty name, so a parcel with nothing recorded on it stays claimable
	// exactly once, which is what it was before the crop entered this.
	//
	// Inside the transaction, like the event checks above, so two handsets
	// sending at the same moment cannot both pass it.
	for _, item := range in.Items {
		var existing, existingRef string
		err := tx.QueryRow(ctx, `
			SELECT r.reference, COALESCE(r.client_ref, '')
			  FROM crop_loss_reports r
			  JOIN crop_loss_report_items i ON i.report_id = r.id
			 WHERE r.survey_event_id = $1
			   AND r.farmer_id = $2
			   AND r.state <> 'withdrawn'
			   AND ( ($3 <> '' AND i.farm_id = $3)
			      OR ($3  = '' AND i.survey_number = $4) )
			   AND lower(btrim(i.crop_name)) = lower(btrim($5))
			 LIMIT 1`,
			in.SurveyEventID, in.FarmerID, item.FarmID, item.SurveyNumber,
			item.CropName).
			Scan(&existing, &existingRef)
		if err == nil {
			// This handset's own claim, arriving again. The check at the top
			// of this function answers a retry before anything else, but two
			// copies sent at the same moment both pass it and only one wins
			// the insert; the loser lands here. It is the same claim, so it
			// gets the same answer the first copy got -- refusing it would
			// mark a farmer's claim failed for having been sent twice.
			if in.ClientRef != "" && existingRef == in.ClientRef {
				return nil, ErrRetry
			}
			// The crop is named as well as the survey number, because on a
			// farm carrying two crops "survey 453 is already claimed" is a
			// sentence the farmer will read as a refusal of the claim they
			// are making rather than of the one they already made. The
			// reference stays, so they are sent to the existing claim instead
			// of left thinking their walk vanished.
			return nil, fmt.Errorf("%w: %s on survey %s is already claimed under %s",
				ErrAlreadyFiled, item.CropName, item.SurveyNumber, existing)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("checking for an earlier claim: %w", err)
		}
	}

	// The season and crop year are the event's, not the handset's. The event
	// is what the claim is filed against, and its incident date places it in
	// the agricultural calendar. A handset's copy can be older than the field:
	// a draft saved from an event cached before events carried a crop year
	// sent none, and was refused for a value the server already had. A copy
	// that disagrees would put the claim's reference and threshold yield in
	// the wrong year.
	season, cropYear := event.SeasonAndCropYear(evStart)
	in.Season, in.CropYear = Season(season), cropYear

	// Whether the form was completed while the event was still taking claims.
	//
	// Decided here, before either refusal below, because both of them are
	// about arrival and this is the question that outranks them: a claim
	// completed in time was made in time, and the days it then spent in a
	// phone's queue waiting for a network -- or for the rest of its evidence
	// -- are this system's doing and not the farmer's.
	//
	// Both deadlines have to be cleared. survey_closes_at is what the farmer
	// was shown; closed_at is when the district actually stopped, which may
	// be earlier. Whichever came first is the one that was real.
	filed, fromHandset := filingInstant(in.FiledAt, in.OccurredAt, time.Now().UTC())
	filedInTime := fromHandset && filed.Before(evCloses) &&
		(evClosedAt == nil || filed.Before(*evClosedAt))
	// A claim admitted after the event stopped taking them. Recorded, because
	// the district closed its books on a count that did not include this one.
	afterClose := false

	if evState != "open" {
		// Paused is the one an officer at a counter will actually meet, and
		// "is paused, not open" tells them nothing about what to do. It is a
		// temporary state their own district put the event into, so say that
		// and say it will come back.
		//
		// ErrNotYet rather than ErrBadState: a pause is not a verdict on the
		// claim, and a handset that read it as one marked the farmer's report
		// refused and stopped sending it -- permanently, for a state the
		// district intended to undo that afternoon.
		if evState == "paused" {
			return nil, fmt.Errorf(
				"%w: filing against %s is paused by the district. It can be "+
					"filed once the district resumes the event; the deadline "+
					"has not changed", ErrNotYet, evRef)
		}
		// A closed event still takes a claim the farmer completed before it
		// closed. The alternative is that this system's own delay -- a queue
		// waiting for a network, or for the rest of the evidence it now
		// insists on -- destroys a claim that was made in time, after the app
		// told the farmer it was safe and would send itself.
		if evState == "closed" && filedInTime {
			afterClose = true
		} else {
			return nil, fmt.Errorf("%w: survey event %s is %s, not open", ErrBadState, evRef, evState)
		}
	}

	// The filing deadline the event advertises.
	//
	// It was read by nobody. An event stays `open` until an officer closes it
	// by hand, so a report could be filed weeks past the date printed on the
	// farmer's own copy and shown to them in the console as binding -- and
	// then be adjudicated against a window it was outside of.
	//
	// Refused here rather than left to the officer who closes the event,
	// because "somebody remembers on the day" is not a deadline. Filing late
	// is still possible where it should be: the district reopens the event,
	// which is a decision with a name on it, and reports already carrying
	// filed_late keep being adjudicated separately.
	//
	// The deadline is the farmer's to meet, and they meet it by completing
	// the form -- not by finding a network. A claim whose form was filled
	// before the close is taken however long the queue then held it; see
	// filedInTime above. Everything else is measured on arrival.
	if time.Now().After(evCloses) {
		if !filedInTime {
			return nil, fmt.Errorf(
				"%w: filing for %s closed on %s. If this claim should still "+
					"be accepted, the district can reopen the event",
				ErrBadState, evRef, evCloses.In(ist).Format("2 January 2006"))
		}
		afterClose = true
	}

	day := in.OccurredAt.UTC().Format("2006-01-02")
	if day < evStart.Format("2006-01-02") || day > evEnd.Format("2006-01-02") {
		return nil, fmt.Errorf(
			"%w: the loss occurred on %s, outside the event window %s to %s",
			ErrBadInput, day,
			evStart.Format("2006-01-02"), evEnd.Format("2006-01-02"))
	}

	// Taken last, just before the insert, so the counter row is held only
	// for the insert and the commit. See nextReference.
	reference, err := nextReference(ctx, tx, place.DistrictCode, in.CropYear)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// When the farmer acted, which for a handset filing is not now. Decided
	// once, above, where the deadlines needed it. An unusable answer from the
	// phone falls back to the arrival instant rather than refusing the claim,
	// and is not written to the record: filed_at means "what the handset
	// said, and it was possible".
	late := filed.Sub(in.OccurredAt) > FilingWindow

	var recordedFiledAt *time.Time
	if fromHandset {
		recordedFiledAt = &filed
	}

	// The accepted declaration is what files a claim.
	//
	// It used to need a signature kind as well, which meant the handset had to
	// supply one -- and it supplied 'thumb' on every claim, recording a thumb
	// impression nobody ever pressed. A digital filing is declared by an
	// authenticated farmer ticking the declaration; how it was signed is a
	// separate fact, true only where an officer stood next to them and watched
	// it happen. See migration 0051.
	state := StateDraft
	if in.DeclarationAccepted {
		state = StateSubmitted
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id,
			farmer_id, farmer_name, farmer_address, farmer_mobile, farmer_email,
			village_lgd_code, village_name, circle_code, circle_name,
			sub_district_lgd_code, sub_district_name,
			district_lgd_code, district_name,
			season, crop_year,
			loss_category, calamity_type, occurred_at,
			reported_by, reported_channel, filed_late, late_reason,
			declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state,
			crop_mismatch, registry_crop_name, client_ref, filed_at,
			arrived_after_close, expected_captures, evidence_complete_at
		) VALUES (
			$1, $2,
			$3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''),
			$8, $9, NULLIF($10, ''), NULLIF($11, ''),
			$12, $13,
			$14, $15,
			$16::crop_season, $17,
			$18::loss_category, $19::calamity_type, $20,
			$21, $22, $23, NULLIF($24, ''),
			$25, $26,
			NULLIF($27, ''), $28, $29,
			$30, NULLIF($31, ''), NULLIF($32, ''), $33, $34, $35,
			-- A claim promising no evidence is complete the moment it lands:
			-- a counter filing, or one whose photographs were taken on the
			-- old path and are already counted.
			CASE WHEN $35::integer IS NULL OR $35::integer = 0
			     THEN now() ELSE NULL END
		)
		-- Two copies of one retry racing each other: the second finds no row
		-- to insert, and is answered with the first one's report below.
		ON CONFLICT (farmer_id, client_ref) WHERE client_ref IS NOT NULL
		DO NOTHING
		RETURNING id`,
		reference, in.SurveyEventID,
		in.FarmerID, in.FarmerName, in.FarmerAddress, in.FarmerMobile, in.FarmerEmail,
		place.VillageCode, place.VillageName, place.CircleCode, place.CircleName,
		place.SubDistrictCode, place.SubDistrictName,
		place.DistrictCode, place.DistrictName,
		string(in.Season), in.CropYear,
		string(in.LossCategory), in.CalamityType, in.OccurredAt,
		nullString(in.ReportedBy), channelOr(in.ReportedChannel), late,
		in.LateReason,
		in.DeclarationAccepted, in.DocumentsPromised,
		in.FarmerSignatureKind, signedAt(in, now), string(state),
		in.CropMismatch, in.RegistryCrop, in.ClientRef, recordedFiledAt,
		afterClose, in.ExpectedCaptures,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && in.ClientRef != "" {
		// Lost the race to an identical retry that committed first.
		_ = tx.Rollback(ctx)
		existing, lerr := s.byClientRef(ctx, in.FarmerID, in.ClientRef)
		if lerr != nil {
			return nil, lerr
		}
		if existing != nil {
			return existing, nil
		}
		return nil, fmt.Errorf("%w: the report could not be recorded; send it again", ErrBadInput)
	}
	if err != nil {
		return nil, classifyWriteErr(err, "")
	}

	for n, item := range in.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO crop_loss_report_items (
				report_id, line_no, survey_number, sub_survey_number, farm_id,
				crop_name, area_sown_ha, area_affected_ha, damage_percent
			) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9)`,
			id, n+1, item.SurveyNumber, item.SubSurveyNumber, item.FarmID,
			item.CropName, item.AreaSownHa, item.AreaAffectedHa, item.DamagePercent,
		); err != nil {
			return nil, classifyWriteErr(err, fmt.Sprintf("line %d: ", n+1))
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, classifyWriteErr(err, "")
	}
	return s.Get(ctx, id, rbac.Scope{JurisdictionIDs: nil, FarmerID: in.FarmerID})
}

func validate(in New) error {
	switch {
	case in.SurveyEventID == "":
		return fmt.Errorf("%w: a report must be filed against a survey event", ErrBadInput)
	case in.FarmerID == "" || in.FarmerName == "":
		return fmt.Errorf("%w: farmer_id and farmer_name are required", ErrBadInput)
	case in.VillageCode == "":
		return fmt.Errorf("%w: village_lgd_code is required", ErrBadInput)
	case in.OccurredAt.IsZero():
		return fmt.Errorf("%w: the date and time of the loss are required", ErrBadInput)
	case in.OccurredAt.After(time.Now().UTC().Add(time.Hour)):
		return fmt.Errorf("%w: the loss is dated in the future", ErrBadInput)
	case len(in.Items) == 0:
		return fmt.Errorf("%w: a report needs at least one survey number", ErrBadInput)
	case in.Season != "" && !slices.Contains([]Season{Kharif, Rabi, Summer}, in.Season):
		return fmt.Errorf("%w: season must be kharif, rabi or summer", ErrBadInput)
	case !ValidCalamity(in.LossCategory, in.CalamityType):
		return fmt.Errorf(
			"%w: %s is not a %s calamity; valid are %s",
			ErrBadInput, in.CalamityType, in.LossCategory,
			strings.Join(CalamitiesFor(in.LossCategory), ", "))
	}

	for n, it := range in.Items {
		switch {
		case it.SurveyNumber == "" || it.CropName == "":
			return fmt.Errorf("%w: line %d needs a survey number and a crop", ErrBadInput, n+1)
		case it.AreaSownHa <= 0:
			return fmt.Errorf("%w: line %d has no sown area", ErrBadInput, n+1)
		case it.AreaAffectedHa > it.AreaSownHa:
			return fmt.Errorf(
				"%w: line %d reports %.4f ha affected of %.4f ha sown",
				ErrBadInput, n+1, it.AreaAffectedHa, it.AreaSownHa)
		case it.DamagePercent < 0 || it.DamagePercent > 100:
			return fmt.Errorf("%w: line %d damage percent is out of range", ErrBadInput, n+1)
		}
	}
	return nil
}

// resolvePlace walks up from a village to fill in circle, taluka and district.
func resolvePlace(ctx context.Context, tx pgx.Tx, villageCode string) (Place, error) {
	var p Place
	err := tx.QueryRow(ctx, `
		WITH v AS (
			SELECT * FROM jurisdictions WHERE level = 'village' AND code = $1
		),
		chain AS (
			SELECT j.level, j.code, j.name
			FROM v, unnest(v.ancestor_ids) AS a(id)
			JOIN jurisdictions j ON j.id = a.id
		)
		SELECT v.code, v.name,
		       COALESCE((SELECT code FROM chain WHERE level = 'circle'), ''),
		       COALESCE((SELECT name FROM chain WHERE level = 'circle'), ''),
		       COALESCE((SELECT code FROM chain WHERE level = 'sub_district'), ''),
		       COALESCE((SELECT name FROM chain WHERE level = 'sub_district'), ''),
		       COALESCE((SELECT code FROM chain WHERE level = 'district'), ''),
		       COALESCE((SELECT name FROM chain WHERE level = 'district'), '')
		FROM v`, villageCode).Scan(
		&p.VillageCode, &p.VillageName,
		&p.CircleCode, &p.CircleName,
		&p.SubDistrictCode, &p.SubDistrictName,
		&p.DistrictCode, &p.DistrictName)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf(
			"%w: village %s is not in the administrative hierarchy; has the LGD "+
				"reference data been loaded?", ErrBadInput, villageCode)
	}
	if err != nil {
		return p, fmt.Errorf("resolving place: %w", err)
	}
	return p, nil
}

// nextReference builds CLR-<year>-<district>-<seq>.
//
// The number comes from a counter row advanced in one statement, so two
// filings can never be given the same number and the cost does not grow with
// the district. See migration 0033.
//
// # Why inside the filing's transaction
//
// Taking it on a separate connection would release the row at once, but it
// makes every filing hold two connections: under load the pool is exhausted by
// filings each holding one and waiting for a second, and they wait on each
// other until they time out. Measured, not supposed -- the first version of
// this fix hung its own test that way.
//
// Inside the transaction the row stays locked until commit, so filings in one
// district take their numbers one after another. The lock is taken last,
// immediately before the insert, which keeps that to the few milliseconds of
// the insert and commit. A rolled-back filing gives its number back.
func nextReference(ctx context.Context, q db.Querier, districtCode, cropYear string) (string, error) {
	year := cropYear
	if len(year) > 4 {
		year = year[:4]
	}
	var n int
	if err := q.QueryRow(ctx, `
		INSERT INTO report_reference_counters (district_lgd_code, year, last_number)
		VALUES ($1, $2, 1)
		ON CONFLICT (district_lgd_code, year) DO UPDATE
		   SET last_number = report_reference_counters.last_number + 1
		RETURNING last_number`,
		districtCode, year).Scan(&n); err != nil {
		return "", classifyWriteErr(err, "")
	}
	return fmt.Sprintf("CLR-%s-%s-%05d", year, districtCode, n), nil
}

// classifyWriteErr sorts a failed write into the caller's fault or not.
//
// # Why this matters more than it looks
//
// Every error here used to become ErrBadInput, and ErrBadInput is a 400, and a
// 400 is what the farmer's phone reads as "the office refused this claim" --
// after which it stops sending. A unique-key collision, a deadlock or a
// statement timeout says nothing about the claim. Those become ErrRetry.
//
// And the message: a constraint name and the SQL behind it are for the log,
// not for a farmer's screen. Only violations of the data's own rules --
// a check, a missing value, a malformed number -- are described, and then in
// the constraint's terms rather than Postgres's.
func classifyWriteErr(err error, prefix string) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ErrRetry, err)
		}
		return err
	}
	switch {
	case pg.Code == "23505", // unique_violation: two writes collided
		pg.Code == "40001", // serialization_failure
		pg.Code == "40P01", // deadlock_detected
		pg.Code == "55P03", // lock_not_available
		pg.Code == "57014": // query_canceled (statement timeout)
		return fmt.Errorf("%w (%s)", ErrRetry, pg.Code)
	case pg.Code == "23514", pg.Code == "23502", strings.HasPrefix(pg.Code, "22"):
		what := pg.ConstraintName
		if what == "" {
			what = pg.ColumnName
		}
		if what == "" {
			return fmt.Errorf("%w: %sa value is outside what a report allows", ErrBadInput, prefix)
		}
		return fmt.Errorf("%w: %s%s is not satisfied", ErrBadInput, prefix, what)
	}
	return err
}

func signedAt(in New, now time.Time) any {
	if in.FarmerSignatureKind == "" {
		return nil
	}
	return now
}

func channelOr(c string) string {
	if c == "" {
		return "field_officer"
	}
	return c
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func cleanErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "ERROR: "); i >= 0 {
		msg = msg[i+len("ERROR: "):]
	}
	if i := strings.Index(msg, " (SQLSTATE"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// byClientRef finds a report this farmer's handset already filed.
//
// The report is marked Replayed so the handler can answer 200 rather than 201:
// nothing was created by this request, and a client that counts filings
// should not count it twice.
func (s *Store) byClientRef(ctx context.Context, farmerID, ref string) (*Report, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM crop_loss_reports
		 WHERE farmer_id = $1 AND client_ref = $2`, farmerID, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up a report by its client reference: %w", err)
	}
	r, err := s.Get(ctx, id, rbac.Scope{FarmerID: farmerID})
	if err != nil {
		return nil, err
	}
	r.Replayed = true
	return r, nil
}
