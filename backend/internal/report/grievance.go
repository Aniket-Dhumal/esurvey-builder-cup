package report

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Grievances: a farmer's right to be heard again.
//
// # What this is not
//
// It is not a second assessment. Re-running the panel over the same evidence
// returns the same answer, and offering that as an appeal would make the
// appeal theatre. A grievance names what the farmer says was missed, and a
// person decides.
//
// # The one rule that makes it an appeal
//
// It is answered by someone other than the officer who decided. An appeal
// heard by the person appealed against is not an appeal, and the code refuses
// it rather than leaving it to the roster.

var (
	ErrGrievanceNotFound = errors.New("no such grievance")
	ErrNotAppealable     = errors.New("this report has no decision to appeal")
	ErrAlreadyOpen       = errors.New("a grievance on this report is already open")
	ErrSelfReview        = errors.New("an appeal cannot be answered by the officer who decided")
	ErrGrievanceState    = errors.New("the grievance is not in a state that allows this")
)

// GrievanceSLA is how long an officer has to answer.
//
// Fifteen days rather than the seven a verification gets: an appeal usually
// needs a field visit that the original did not, and a deadline nobody can
// meet is a deadline that gets ignored rather than met.
const GrievanceSLA = 15 * 24 * time.Hour

type GrievanceGround string

const (
	GroundAreaWrong        GrievanceGround = "area_wrong"
	GroundDamagePercentLow GrievanceGround = "damage_percent_low"
	GroundEvidenceIgnored  GrievanceGround = "evidence_ignored"
	GroundOther            GrievanceGround = "other"

	// GroundVisitNeeded is the stored value 'not_inspected', which used to
	// mean "nobody came".
	//
	// Nobody comes now: assessment is made from what the farmer sends. That
	// makes the old wording a description of the design rather than a
	// complaint, and a farmer choosing it would be told they were right and
	// nothing would follow.
	//
	// The real complaint underneath it survives, and a system that never
	// visits needs it more than one that always did: some losses a
	// photograph cannot carry. Water that drained before the phone came out,
	// root damage under a canopy that still looks green, a plot the farmer
	// could not safely reach. Without a way to say "this needs eyes", those
	// cases have no route at all.
	GroundVisitNeeded GrievanceGround = "not_inspected"
)

// GroundLabels are what a farmer reads when choosing. Marathi first: this is
// the one screen in the system a farmer reaches while already unhappy, and
// making them read English there is its own kind of refusal.
var GroundLabels = map[GrievanceGround]string{
	GroundAreaWrong:        "क्षेत्र चुकीचे आहे — the area assessed is not the land I lost",
	GroundDamagePercentLow: "नुकसान कमी दाखवले — the damage recorded is less than what happened",
	GroundEvidenceIgnored:  "पुरावा विचारात घेतला नाही — my photographs were not considered",
	GroundVisitNeeded: "फोटोत दिसत नाही, प्रत्यक्ष पाहणी हवी — " +
		"the photographs cannot show this; I want somebody to come and see",
	GroundOther: "इतर — something else",
}

// OfficerGroundLabels is the same set of grounds, said to an officer.
//
// GroundLabels above is the farmer's own wording: Marathi first, first person,
// and phrased as the thing they are claiming ("the area assessed is not the
// land I lost"). That is exactly right on the screen where a farmer chooses a
// ground, and wrong in a queue of forty appeals where an officer is scanning
// for what each one is about.
//
// Two maps rather than one truncated at the em dash, because the two readers
// want different sentences and a split would silently produce nonsense the
// first time a label is written without one.
var OfficerGroundLabels = map[GrievanceGround]string{
	GroundAreaWrong:        "the assessed area is disputed",
	GroundDamagePercentLow: "the damage percentage is disputed",
	GroundEvidenceIgnored:  "the farmer's photographs were not considered",
	GroundVisitNeeded:      "a physical inspection is asked for",
	GroundOther:            "another ground",
}

// OfficerGround is the officer-facing wording, falling back to the code.
//
// A ground added to the enum and not to the map above still reads as
// something rather than as an empty sentence.
func OfficerGround(g GrievanceGround) string {
	if s, ok := OfficerGroundLabels[g]; ok {
		return s
	}
	return string(g)
}

func (g GrievanceGround) Valid() bool {
	_, ok := GroundLabels[g]
	return ok
}

type GrievanceState string

const (
	GrievanceOpen         GrievanceState = "open"
	GrievanceUnderReview  GrievanceState = "under_review"
	GrievanceUpheld       GrievanceState = "upheld"
	GrievancePartlyUpheld GrievanceState = "partly_upheld"
	GrievanceRejected     GrievanceState = "rejected"
	GrievanceWithdrawn    GrievanceState = "withdrawn"
)

// Open reports whether a grievance is still someone's work.
func (s GrievanceState) Open() bool {
	return s == GrievanceOpen || s == GrievanceUnderReview
}

// Grievance is one appeal against one report's decision.
type Grievance struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`

	ReportID        string `json:"report_id"`
	ReportReference string `json:"report_reference,omitempty"`

	FarmerID    string `json:"farmer_id"`
	FarmerName  string `json:"farmer_name"`
	VillageCode string `json:"village_lgd_code"`
	VillageName string `json:"village_name,omitempty"`

	Ground    GrievanceGround `json:"ground"`
	Statement string          `json:"statement"`

	ClaimedPercent  *float64   `json:"claimed_percent,omitempty"`
	DisputedPercent *float64   `json:"disputed_percent,omitempty"`
	DisputedAt      *time.Time `json:"disputed_at,omitempty"`

	State GrievanceState `json:"state"`

	FiledAt      time.Time `json:"filed_at"`
	FiledBy      string    `json:"filed_by,omitempty"`
	FiledChannel string    `json:"filed_channel"`

	DueAt   time.Time `json:"due_at"`
	Overdue bool      `json:"overdue"`

	AssignedTo     string `json:"assigned_to,omitempty"`
	AssignedToName string `json:"assigned_to_name,omitempty"`
	EscalatedPast  string `json:"escalated_past,omitempty"`

	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedByName  string     `json:"decided_by_name,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	RevisedPercent *float64   `json:"revised_percent,omitempty"`
}

// NewGrievance is a filing.
type NewGrievance struct {
	ReportID       string
	Ground         GrievanceGround
	Statement      string
	ClaimedPercent *float64
	Channel        string
	FiledBy        string
}

const grievanceColumns = `
	g.id, g.reference, g.report_id, r.reference,
	g.farmer_id, g.farmer_name, g.village_lgd_code, r.village_name,
	g.ground::text, g.statement,
	g.claimed_percent, g.disputed_percent, g.disputed_at,
	g.state::text, g.filed_at, COALESCE(g.filed_by::text, ''), g.filed_channel,
	g.due_at,
	COALESCE(g.assigned_to::text, ''), COALESCE(au.full_name, ''),
	COALESCE(g.escalated_past::text, ''),
	g.decided_at, COALESCE(g.decided_by::text, ''), COALESCE(du.full_name, ''),
	COALESCE(g.decision_reason, ''), g.revised_percent`

const grievanceFrom = `
	FROM grievances g
	JOIN crop_loss_reports r ON r.id = g.report_id
	LEFT JOIN users au ON au.id = g.assigned_to
	LEFT JOIN users du ON du.id = g.decided_by`

func scanGrievance(row pgx.Row) (*Grievance, error) {
	var g Grievance
	var ground, state string
	err := row.Scan(
		&g.ID, &g.Reference, &g.ReportID, &g.ReportReference,
		&g.FarmerID, &g.FarmerName, &g.VillageCode, &g.VillageName,
		&ground, &g.Statement,
		&g.ClaimedPercent, &g.DisputedPercent, &g.DisputedAt,
		&state, &g.FiledAt, &g.FiledBy, &g.FiledChannel,
		&g.DueAt,
		&g.AssignedTo, &g.AssignedToName, &g.EscalatedPast,
		&g.DecidedAt, &g.DecidedBy, &g.DecidedByName,
		&g.DecisionReason, &g.RevisedPercent,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGrievanceNotFound
	}
	if err != nil {
		return nil, err
	}
	g.Ground = GrievanceGround(ground)
	g.State = GrievanceState(state)
	g.Overdue = g.State.Open() && time.Now().After(g.DueAt)
	return &g, nil
}

// FileGrievance records an appeal against a report's decision.
//
// The report must have reached a decision. Appealing a report that is still
// being verified is not an appeal, it is impatience -- and answering it would
// pre-empt the officers who are still working on it.
func (s *Store) FileGrievance(
	ctx context.Context, in NewGrievance, scope rbac.Scope,
) (*Grievance, error) {
	if !in.Ground.Valid() {
		return nil, fmt.Errorf("%w: unknown ground %q", ErrBadState, in.Ground)
	}
	if len(strings.TrimSpace(in.Statement)) < 10 {
		return nil, fmt.Errorf(
			"%w: say what was wrong, in a sentence or two", ErrBadState)
	}
	if in.Channel == "" {
		in.Channel = "app"
	}

	// Scoped read: a farmer may appeal their own report, an officer one in
	// their villages, and nobody may appeal a report they cannot see.
	rep, err := s.Get(ctx, in.ReportID, scope)
	if err != nil {
		return nil, err
	}

	switch rep.State {
	case StateAdjudicated, StateVerified, StateClosed:
	default:
		return nil, fmt.Errorf(
			"%w: it is %s, and there is nothing decided to appeal yet",
			ErrNotAppealable, rep.State)
	}

	// One open grievance at a time. A second while the first is unanswered
	// splits the same complaint across two officers.
	var open bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM grievances
		               WHERE report_id = $1 AND state IN ('open', 'under_review'))`,
		in.ReportID).Scan(&open); err != nil {
		return nil, fmt.Errorf("checking for an open grievance: %w", err)
	}
	if open {
		return nil, ErrAlreadyOpen
	}

	// What is being disputed, captured now. A later re-adjudication must not
	// silently rewrite what the farmer actually objected to.
	var disputedPct *float64
	var disputedAt *time.Time
	if adj, err := s.Adjudication(ctx, in.ReportID); err == nil && adj != nil {
		p := adj.AdjudicatedPercent
		disputedPct, disputedAt = &p, &adj.DecidedAt
	}

	row := s.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO grievances (
				reference, report_id, farmer_id, farmer_name, village_lgd_code,
				ground, statement, claimed_percent,
				disputed_percent, disputed_at,
				filed_by, filed_channel, due_at
			) VALUES (
				next_grievance_reference(), $1, $2, $3, $4,
				$5::grievance_ground, $6, $7,
				$8, $9,
				NULLIF($10, '')::uuid, $11, now() + $12::interval
			) RETURNING *
		)
		SELECT `+grievanceColumns+`
		FROM inserted g
		JOIN crop_loss_reports r ON r.id = g.report_id
		LEFT JOIN users au ON au.id = g.assigned_to
		LEFT JOIN users du ON du.id = g.decided_by`,
		rep.ID, rep.FarmerID, rep.FarmerName, rep.Place.VillageCode,
		string(in.Ground), strings.TrimSpace(in.Statement), in.ClaimedPercent,
		disputedPct, disputedAt,
		in.FiledBy, in.Channel, GrievanceSLA.String())

	g, err := scanGrievance(row)
	if err != nil {
		return nil, err
	}

	// Confirmation, with the deadline in it. A farmer who has just disputed a
	// decision needs to know it was received and by when they will hear back
	// -- silence here is what the appeal exists to end.
	s.tellGrievance(ctx, s.pool, notify.EventGrievanceFiled, g, "")

	// And the officers who can answer it. Separate from the farmer's SMS
	// because it is a different recipient with a different failure mode, and
	// because the two must not be able to take each other down.
	if err := s.grievanceFiled(ctx, g.VillageCode, g.Reference,
		OfficerGround(g.Ground), g.ID); err != nil {
		s.noticeFailed("announcing a grievance", g.ReportID, err)
	}
	return g, nil
}

// AssignGrievance puts an appeal in front of someone who did not decide it.
func (s *Store) AssignGrievance(
	ctx context.Context, grievanceID, officerID string,
) (*Grievance, error) {
	g, err := s.grievanceByID(ctx, grievanceID)
	if err != nil {
		return nil, err
	}
	if !g.State.Open() {
		return nil, fmt.Errorf("%w: it is %s", ErrGrievanceState, g.State)
	}

	// The rule that makes this an appeal.
	if decided, err := s.decidedBy(ctx, g.ReportID); err == nil &&
		decided != "" && decided == officerID {
		return nil, ErrSelfReview
	}

	row := s.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE grievances
			SET assigned_to = $2::uuid,
			    escalated_past = (
			        -- The officer whose decision is being appealed: the
			        -- current one, not the original. Once an appeal can
			        -- revise a figure a report has a chain of these, and an
			        -- unqualified subquery would return several and fail.
			        SELECT officer_id FROM report_adjudications a
			        WHERE a.report_id = grievances.report_id
			          AND NOT EXISTS (
			              SELECT 1 FROM report_adjudications later
			              WHERE later.supersedes_id = a.id
			          )
			    ),
			    state = 'under_review'
			WHERE id = $1 AND state IN ('open', 'under_review')
			RETURNING *
		)
		SELECT `+grievanceColumns+`
		FROM updated g
		JOIN crop_loss_reports r ON r.id = g.report_id
		LEFT JOIN users au ON au.id = g.assigned_to
		LEFT JOIN users du ON du.id = g.decided_by`,
		grievanceID, officerID)

	return scanGrievance(row)
}

// GrievanceDecision is an answer to an appeal.
type GrievanceDecision struct {
	GrievanceID    string
	State          GrievanceState
	Reason         string
	RevisedPercent *float64

	// RevisedAreaHa is optional. Most appeals are about the percentage, and
	// an appeal that says nothing about area leaves it where the original
	// decision put it.
	RevisedAreaHa *float64

	OfficerID string
	Role      rbac.Role
}

// DecideGrievance answers an appeal.
func (s *Store) DecideGrievance(
	ctx context.Context, d GrievanceDecision,
) (*Grievance, error) {
	switch d.State {
	case GrievanceUpheld, GrievancePartlyUpheld, GrievanceRejected:
	default:
		return nil, fmt.Errorf(
			"%w: an appeal is upheld, partly upheld or rejected", ErrGrievanceState)
	}
	if len(strings.TrimSpace(d.Reason)) < 10 {
		return nil, fmt.Errorf(
			"%w: the farmer reads this; give them a reason", ErrGrievanceState)
	}
	if d.State != GrievanceRejected && d.RevisedPercent == nil {
		// Saying the farmer was right and leaving the figure where it was is
		// a contradiction the database also refuses.
		return nil, fmt.Errorf(
			"%w: upholding an appeal has to change the figure", ErrGrievanceState)
	}

	g, err := s.grievanceByID(ctx, d.GrievanceID)
	if err != nil {
		return nil, err
	}
	if !g.State.Open() {
		return nil, fmt.Errorf("%w: it is already %s", ErrGrievanceState, g.State)
	}

	if decided, err := s.decidedBy(ctx, g.ReportID); err == nil &&
		decided != "" && decided == d.OfficerID {
		return nil, ErrSelfReview
	}

	// The decision and the figure it changes move together. A grievance
	// recorded as upheld beside a report still carrying the old number is the
	// exact inconsistency this change exists to remove -- and it is the one a
	// farmer would find by reading their own report.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	row := tx.QueryRow(ctx, `
		WITH updated AS (
			UPDATE grievances
			SET state = $2::grievance_state,
			    decision_reason = $3,
			    revised_percent = $4,
			    decided_by = $5::uuid,
			    decided_at = now()
			WHERE id = $1 AND state IN ('open', 'under_review')
			RETURNING *
		)
		SELECT `+grievanceColumns+`
		FROM updated g
		JOIN crop_loss_reports r ON r.id = g.report_id
		LEFT JOIN users au ON au.id = g.assigned_to
		LEFT JOIN users du ON du.id = g.decided_by`,
		d.GrievanceID, string(d.State), strings.TrimSpace(d.Reason),
		d.RevisedPercent, d.OfficerID)

	decided, err := scanGrievance(row)
	if err != nil {
		return nil, err
	}

	// Upheld, in whole or in part, means the figure on the report changes.
	// Rejected leaves it standing, which is what rejecting an appeal means.
	if d.State != GrievanceRejected && d.RevisedPercent != nil {
		current, err := s.Adjudication(ctx, decided.ReportID)
		if err != nil {
			// A report with no adjudication has nothing to revise. That is a
			// grievance against a decision that was never made, and the
			// appeal stands as recorded rather than inventing one.
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		} else {
			area := current.AdjudicatedAreaHa
			if d.RevisedAreaHa != nil {
				area = *d.RevisedAreaHa
			}
			if err := s.revise(ctx, tx, Revision{
				ReportID:       decided.ReportID,
				GrievanceID:    decided.ID,
				OfficerID:      d.OfficerID,
				Role:           d.Role,
				RevisedPercent: *d.RevisedPercent,
				RevisedAreaHa:  area,
				Reason:         d.Reason,
			}, current); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.tellGrievance(ctx, s.pool, notify.EventGrievanceDecided, decided,
		grievanceOutcome(decided))
	return decided, nil
}

// WithdrawGrievance lets a farmer stop pursuing one.
func (s *Store) WithdrawGrievance(
	ctx context.Context, grievanceID string, scope rbac.Scope,
) (*Grievance, error) {
	g, err := s.Grievance(ctx, grievanceID, scope)
	if err != nil {
		return nil, err
	}
	if !g.State.Open() {
		return nil, fmt.Errorf("%w: it is already %s", ErrGrievanceState, g.State)
	}

	row := s.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE grievances SET state = 'withdrawn'
			WHERE id = $1 AND state IN ('open', 'under_review')
			RETURNING *
		)
		SELECT `+grievanceColumns+`
		FROM updated g
		JOIN crop_loss_reports r ON r.id = g.report_id
		LEFT JOIN users au ON au.id = g.assigned_to
		LEFT JOIN users du ON du.id = g.decided_by`, grievanceID)

	return scanGrievance(row)
}

// Grievance returns one, if it lies inside the scope.
func (s *Store) Grievance(
	ctx context.Context, id string, scope rbac.Scope,
) (*Grievance, error) {
	// An empty scope reaches nothing, as it does in every other store.
	//
	// It used to mean "an internal call that has already checked access", and
	// read the grievance with no filter at all. But an officer whose postings
	// have all been ended also has an empty scope, and every grievance route
	// passes the caller's own -- so that officer could open, assign and decide
	// any appeal in any state. The internal callers use grievanceByID, by name.
	if scope.Empty() {
		return nil, ErrGrievanceNotFound
	}

	return scanGrievance(s.pool.QueryRow(ctx,
		`SELECT `+grievanceColumns+grievanceFrom+
			` WHERE g.id = $1 AND `+grievanceScope("$2", "$3"),
		id, nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID)))
}

// grievanceByID reads a grievance with no scope, for the store's own use once
// a route has established the caller may act on it. Never reachable with a
// caller-supplied scope.
func (s *Store) grievanceByID(ctx context.Context, id string) (*Grievance, error) {
	return scanGrievance(s.pool.QueryRow(ctx,
		`SELECT `+grievanceColumns+grievanceFrom+` WHERE g.id = $1`, id))
}

// grievanceScope is the jurisdiction filter, mirroring the report one.
func grievanceScope(jurParam, farmerParam string) string {
	return `(
		(` + farmerParam + `::text IS NOT NULL AND g.farmer_id = ` + farmerParam + `)
		OR (` + jurParam + `::uuid[] IS NOT NULL AND EXISTS (
			SELECT 1 FROM jurisdictions v
			WHERE v.level = 'village' AND v.code = g.village_lgd_code
			  AND (v.id = ANY(` + jurParam + `) OR v.ancestor_ids && ` + jurParam + `)
		))
	)`
}

// GrievanceFilter narrows a listing.
type GrievanceFilter struct {
	Scope    rbac.Scope
	ReportID string
	State    string
	// OpenOnly is the queue an officer works from.
	OpenOnly bool
	Page     page.Params
}

// Grievances lists appeals, oldest deadline first.
//
// Ordered by deadline rather than by filing date: the queue's job is to stop
// one being forgotten, and the one closest to its deadline is the one at risk.
func (s *Store) Grievances(
	ctx context.Context, f GrievanceFilter,
) (page.Result[Grievance], error) {
	if f.Scope.Empty() {
		return page.Wrap([]Grievance{}, 0, f.Page), nil
	}

	jur := nullableUUIDs(f.Scope.JurisdictionIDs)
	farmer := nullString(f.Scope.FarmerID)
	search := ""
	if f.Page.Query != "" {
		search = page.Like(f.Page.Query)
	}

	where := grievanceScope("$1", "$2") + `
		  AND ($3::text = '' OR g.report_id::text = $3)
		  AND ($4::text = '' OR g.state::text = $4)
		  AND (NOT $5::boolean OR g.state IN ('open', 'under_review'))
		  AND ($8::text = '' OR g.reference ILIKE $8 OR g.farmer_name ILIKE $8
		                     OR r.reference ILIKE $8)`

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)`+grievanceFrom+` WHERE `+
			strings.ReplaceAll(where, "$8", "$6"),
		jur, farmer, f.ReportID, f.State, f.OpenOnly, search).Scan(&total); err != nil {
		return page.Result[Grievance]{}, fmt.Errorf("counting grievances: %w", err)
	}
	if total == 0 {
		return page.Wrap([]Grievance{}, 0, f.Page), nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+grievanceColumns+grievanceFrom+` WHERE `+where+`
		 ORDER BY g.due_at, g.id
		 LIMIT $6 OFFSET $7`,
		jur, farmer, f.ReportID, f.State, f.OpenOnly,
		f.Page.Limit, f.Page.Offset, search)
	if err != nil {
		return page.Result[Grievance]{}, fmt.Errorf("listing grievances: %w", err)
	}
	defer rows.Close()

	out := []Grievance{}
	for rows.Next() {
		g, err := scanGrievance(rows)
		if err != nil {
			return page.Result[Grievance]{}, err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return page.Result[Grievance]{}, err
	}
	return page.Wrap(out, total, f.Page), nil
}

// decidedBy names the officer who adjudicated a report, if one has.
func (s *Store) decidedBy(ctx context.Context, reportID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT officer_id::text FROM report_adjudications WHERE report_id = $1`,
		reportID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// tellGrievance queues a message about an appeal.
//
// The recipient is read from the report rather than carried on the grievance:
// a grievance filed at a counter by an officer is still the farmer's, and the
// message goes to the farmer.
func (s *Store) tellGrievance(
	ctx context.Context, tx db.Querier, eventCode string, g *Grievance, outcome string,
) {
	if s.notify == nil || g == nil {
		return
	}

	var mobile string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(farmer_mobile, '') FROM crop_loss_reports WHERE id = $1`,
		g.ReportID).Scan(&mobile); err != nil {
		return
	}

	body, err := notify.Render(eventCode, notify.Vars{
		Reference: g.Reference,
		Village:   g.VillageName,
		Outcome:   outcome,
	})
	if err != nil {
		return
	}

	_ = s.notify.Enqueue(ctx, tx, notify.Message{
		EventCode:    eventCode,
		FarmerID:     g.FarmerID,
		Recipient:    mobile,
		Body:         body,
		ResourceType: "grievance",
		ResourceID:   g.ID,
	})
}

// grievanceOutcome is the one line a farmer reads about the decision.
//
// Marathi, and it says what happened rather than naming a state: "rejected"
// is a status, "your appeal was not accepted" is an answer.
func grievanceOutcome(g *Grievance) string {
	switch g.State {
	case GrievanceUpheld:
		return "हरकत मान्य झाली."
	case GrievancePartlyUpheld:
		return "हरकत अंशतः मान्य झाली."
	case GrievanceRejected:
		return "हरकत मान्य झाली नाही."
	default:
		return ""
	}
}
